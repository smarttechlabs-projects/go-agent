#!/usr/bin/env bash

# This file is part of the SmartTechLabs AI Workshop material.
# Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
# SmartTechLabs is also available for AI projects and consulting.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# See the LICENSE file in the project root or
# http://www.apache.org/licenses/LICENSE-2.0 for the full text.

# agent-cli.sh — curl-based CLI for the running llm-agent web/API surface.
#
# Targets a Go agent that was started with `-web <addr>` (default localhost:3131).
# Every subcommand is a thin curl wrapper; nothing here knows about the binary
# itself, so it works against any reachable agent instance.
#
# Usage:
#   ./agent-cli.sh                                # show command menu
#   ./agent-cli.sh health                         # liveness + loaded model
#   ./agent-cli.sh tools                          # list MCP tools
#   ./agent-cli.sh limits                         # resolved per-query safety limits
#   ./agent-cli.sh sessions                       # active sessions
#   ./agent-cli.sh approvals                      # pending HITL approvals
#   ./agent-cli.sh query "your question"          # synchronous query, full response
#   ./agent-cli.sh stream "your question"         # query with live event stream
#   ./agent-cli.sh events [type]                  # tail live SSE events, optional type filter
#   ./agent-cli.sh metrics                        # raw Prometheus exposition
#   ./agent-cli.sh metrics-summary                # human-readable summary of key metrics
#
# Environment variables:
#   AGENT_URL      base URL of the agent (default: http://localhost:3131)

set -euo pipefail

AGENT_URL="${AGENT_URL:-http://localhost:3131}"
API="${AGENT_URL}/api/v1"

# ── Colors ─────────────────────────────────────────────────────────────────────
# ANSI-C quoting so the variables hold the actual ESC byte; this lets us drop
# them into heredocs / cat without needing echo -e everywhere.
RED=$'\033[0;31m'
GREEN=$'\033[0;32m'
YELLOW=$'\033[1;33m'
CYAN=$'\033[0;36m'
BOLD=$'\033[1m'
DIM=$'\033[2m'
NC=$'\033[0m'

info()   { printf '%s[INFO]%s  %s\n' "$CYAN" "$NC" "$1"; }
ok()     { printf '%s[OK]%s    %s\n' "$GREEN" "$NC" "$1"; }
warn()   { printf '%s[WARN]%s  %s\n' "$YELLOW" "$NC" "$1"; }
fail()   { printf '%s[FAIL]%s  %s\n' "$RED" "$NC" "$1"; }
header() { printf '\n%s═══ %s ═══%s\n\n' "$BOLD" "$1" "$NC"; }

# ── Helpers ────────────────────────────────────────────────────────────────────

require_jq() {
  if ! command -v jq &>/dev/null; then
    fail "jq is required for pretty-printing. Install it (apt install jq, brew install jq)."
    exit 1
  fi
}

# fetch <method> <path> [data]  → echoes the response body, exits on transport error
fetch() {
  local method="$1"
  local path="$2"
  local data="${3:-}"

  local args=(-sS --fail-with-body --max-time 30 -X "$method")
  if [ -n "$data" ]; then
    args+=(-H 'Content-Type: application/json' -d "$data")
  fi
  if ! curl "${args[@]}" "$path"; then
    fail "request failed: $method $path"
    exit 1
  fi
}

# ── Subcommands ────────────────────────────────────────────────────────────────

cmd_health() {
  require_jq
  header "Agent health (${AGENT_URL})"
  fetch GET "${API}/health" | jq .
}

cmd_tools() {
  require_jq
  header "MCP tools available"
  local resp
  resp=$(fetch GET "${API}/tools")
  echo "$resp" | jq -r '"  total: \(.count)\n"'
  echo "$resp" | jq -r '.tools[].name' | sort | sed 's/^/  - /'
}

cmd_limits() {
  require_jq
  header "Per-query safety limits"
  fetch GET "${API}/limits" | jq .
}

cmd_sessions() {
  require_jq
  header "Active sessions"
  local resp
  resp=$(fetch GET "${API}/sessions")
  echo "$resp" | jq -r '"  count: \(.count)"'
  echo "$resp" | jq -r '.sessions[] | "  - \(.id)  history=\(.history_len) chars  idle=\(.idle_seconds)s  default=\(.is_default)"'
}

cmd_approvals() {
  require_jq
  header "Pending HITL approvals"
  fetch GET "${API}/approvals" | jq .
}

cmd_query() {
  local q="${1:-}"
  if [ -z "$q" ]; then
    fail "Usage: $0 query \"your question\""
    exit 1
  fi
  require_jq
  header "Query"
  info "$q"
  echo ""

  local body
  body=$(printf '{"query": %s}' "$(printf '%s' "$q" | jq -Rs .)")

  local resp
  resp=$(fetch POST "${API}/query" "$body")

  # Print the salient bits in a stable layout.
  echo "$resp" | jq -r '
    "  termination_reason: \(.termination_reason // "?")",
    "  rounds_used:        \(.rounds_used // "?")",
    "  duration_ms:        \(.duration_ms // "?")",
    "  tokens_used:        \(.tokens_used // {} | "prompt=\(.prompt_tokens // 0) completion=\(.completion_tokens // 0) total=\(.total_tokens // 0)")",
    "",
    "  Answer:",
    "    \(.answer // .error // "(no answer)")"
  '
}

cmd_stream() {
  local q="${1:-}"
  if [ -z "$q" ]; then
    fail "Usage: $0 stream \"your question\""
    exit 1
  fi
  header "Streaming query: $q"
  local body
  body=$(printf '{"query": %s}' "$(printf '%s' "$q" | jq -Rs .)")
  curl -sN --max-time 600 -H 'Content-Type: application/json' \
       -d "$body" "${API}/query/stream"
}

cmd_events() {
  local filter="${1:-}"
  if [ -n "$filter" ]; then
    header "Tailing /events (type=${filter}). Ctrl-C to stop."
    if command -v jq &>/dev/null; then
      curl -sN --max-time 0 "${AGENT_URL}/events" \
        | sed -n 's/^data: //p' \
        | jq -c "select(.type == \"$filter\")"
    else
      curl -sN --max-time 0 "${AGENT_URL}/events" \
        | sed -n 's/^data: //p' \
        | grep --line-buffered "\"type\":\"${filter}\""
    fi
  else
    header "Tailing /events. Ctrl-C to stop."
    curl -sN --max-time 0 "${AGENT_URL}/events"
  fi
}

cmd_metrics() {
  header "Prometheus exposition (raw)"
  if ! curl -sS --fail-with-body --max-time 10 "${API}/metrics"; then
    fail "metrics endpoint not reachable. Did you start the agent with -web?"
    exit 1
  fi
}

cmd_metrics_summary() {
  header "Metrics summary (parsed from ${API}/metrics)"

  local raw
  if ! raw=$(curl -sS --fail-with-body --max-time 10 "${API}/metrics"); then
    fail "metrics endpoint not reachable. Did you start the agent with -web?"
    exit 1
  fi

  # Helper: sum all samples of a counter (no labels), one decimal place
  sum_counter() {
    local name="$1"
    echo "$raw" \
      | awk -v n="$name" '
          $0 ~ "^"n"_total\\{" || $0 ~ "^"n"\\{" || $0 ~ "^"n"_total " || $0 ~ "^"n" " {
            v = $NF; total += v
          }
          END { printf "%.0f", total + 0 }'
  }

  # Helper: extract _count from a histogram (sum of all label sets)
  hist_count() {
    local name="$1"
    echo "$raw" \
      | awk -v n="$name" '
          $0 ~ "^"n"_count\\{" || $0 ~ "^"n"_count " {
            v = $NF; total += v
          }
          END { printf "%.0f", total + 0 }'
  }

  # Helper: extract _sum from a histogram
  hist_sum() {
    local name="$1"
    echo "$raw" \
      | awk -v n="$name" '
          $0 ~ "^"n"_sum\\{" || $0 ~ "^"n"_sum " {
            v = $NF; total += v
          }
          END { printf "%.3f", total + 0 }'
  }

  echo -e "  ${BOLD}Agent queries${NC}"
  echo "    total:               $(sum_counter agent_queries)"
  echo "    duration observations: $(hist_count agent_query_duration_seconds)"
  echo "    duration sum (s):    $(hist_sum agent_query_duration_seconds)"

  echo ""
  echo -e "  ${BOLD}LLM calls${NC}"
  echo "    total:               $(sum_counter llm_calls)"
  echo "    duration observations: $(hist_count llm_call_duration_seconds)"
  echo "    duration sum (s):    $(hist_sum llm_call_duration_seconds)"

  echo ""
  echo -e "  ${BOLD}Tool calls${NC}"
  echo "    total:               $(sum_counter tool_calls)"
  echo "    duration observations: $(hist_count tool_call_duration_seconds)"
  echo "    duration sum (s):    $(hist_sum tool_call_duration_seconds)"

  echo ""
  echo -e "  ${DIM}(For PromQL queries against this output, scrape ${API}/metrics from Prometheus / Grafana Agent / OTel collector.)${NC}"
}

cmd_help() {
  cat <<EOF
${BOLD}agent-cli.sh${NC} — curl-based CLI for the running llm-agent

${BOLD}Usage:${NC}
  $0 [command] [args]

${BOLD}Commands:${NC}
  health                       Agent liveness + loaded model
  tools                        List MCP tools
  limits                       Resolved per-query safety limits
  sessions                     Active session list
  approvals                    Pending HITL approvals
  query "<text>"               Synchronous query, print full response
  stream "<text>"              Query with live SSE event stream
  events [type]                Tail live /events, optional type filter
  metrics                      Raw Prometheus exposition from /api/v1/metrics
  metrics-summary              Parsed human-readable metrics summary
  help                         This help

${BOLD}Environment:${NC}
  AGENT_URL                    Base URL of the agent (${AGENT_URL})

${BOLD}Notes:${NC}
  - The agent must be running with ${BOLD}-web${NC} (e.g. ${BOLD}-web localhost:3131${NC}) for any
    of these to work. Without ${BOLD}-web${NC}, no HTTP surface is exposed.
  - Most subcommands require ${BOLD}jq${NC} for pretty-printing.
  - ${BOLD}metrics${NC} requires the agent to have been built with the Prometheus
    exporter wired in (current default).
EOF
}

# ── Main ───────────────────────────────────────────────────────────────────────

COMMAND="${1:-help}"

if [ "$COMMAND" != "help" ] && [ "$COMMAND" != "--help" ] && [ "$COMMAND" != "-h" ]; then
  echo -e "${BOLD}agent-cli.sh${NC} — agent: ${DIM}${AGENT_URL}${NC}"
  echo -e "${DIM}─────────────────────────────────────────────${NC}"
fi

case "$COMMAND" in
  health|status)       shift; cmd_health "$@" ;;
  tools)               shift; cmd_tools "$@" ;;
  limits)              shift; cmd_limits "$@" ;;
  sessions)            shift; cmd_sessions "$@" ;;
  approvals)           shift; cmd_approvals "$@" ;;
  query|ask)           shift; cmd_query "$@" ;;
  stream)              shift; cmd_stream "$@" ;;
  events|tail)         shift; cmd_events "$@" ;;
  metrics)             shift; cmd_metrics "$@" ;;
  metrics-summary)     shift; cmd_metrics_summary "$@" ;;
  help|--help|-h)      cmd_help ;;
  *)
    fail "Unknown command: ${COMMAND}"
    cmd_help
    exit 1
    ;;
esac
