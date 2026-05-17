#!/usr/bin/env bash

# This file is part of the SmartTechLabs AI Workshop material.
# Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
# SmartTechLabs is also available for AI projects and consulting.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# See the LICENSE file in the project root or
# http://www.apache.org/licenses/LICENSE-2.0 for the full text.

# shutdown.sh — gracefully stop the llm-agent, the Lemonade Server, and
# any MCP child processes left behind by a hard kill.
#
# Usage:
#   ./shutdown.sh                    # default: stop both (agent first, then Lemonade)
#   ./shutdown.sh all                # same as default, explicit
#   ./shutdown.sh agent              # stop just the llm-agent + orphan MCP children
#   ./shutdown.sh lemonade           # stop just the Lemonade Server (+ stray lemond daemons)
#   ./shutdown.sh status             # report what's currently running
#   ./shutdown.sh help               # this menu
#
# Environment variables:
#   AGENT_NAME           process name to match for the agent (default: llm-agent)
#   LEMONADE_URL         Lemonade health URL (default: http://localhost:13305)
#   SHUTDOWN_GRACE_SECS  seconds to wait for graceful exit before SIGKILL (default: 10)
#   ORPHAN_CLEANUP       1 to also kill stray playwright-mcp Chrome processes (default: 1)

set -euo pipefail

AGENT_NAME="${AGENT_NAME:-llm-agent}"
LEMONADE_URL="${LEMONADE_URL:-http://localhost:13305}"
LEMONADE_API="${LEMONADE_URL}/api/v1"
SHUTDOWN_GRACE_SECS="${SHUTDOWN_GRACE_SECS:-10}"
ORPHAN_CLEANUP="${ORPHAN_CLEANUP:-1}"

# ── Colors (ANSI-C quoted so headers render in heredocs/printf) ───────────────
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

# Return all PIDs for an exact-name match (own user only, ignoring this script's PID).
pids_for() {
  local name="$1"
  pgrep -u "$(id -u)" -x "$name" 2>/dev/null | grep -v "^$$\$" || true
}

# Return PIDs for a substring match against the full command line (own user).
pids_grep() {
  local pattern="$1"
  pgrep -u "$(id -u)" -f "$pattern" 2>/dev/null | grep -v "^$$\$" || true
}

# Wait up to N seconds for a list of PIDs to disappear. Returns 0 if all gone.
wait_for_exit() {
  local timeout="$1"; shift
  local pids=("$@")
  local elapsed=0
  while [ "$elapsed" -lt "$timeout" ]; do
    local alive=()
    for pid in "${pids[@]}"; do
      kill -0 "$pid" 2>/dev/null && alive+=("$pid")
    done
    [ "${#alive[@]}" -eq 0 ] && return 0
    sleep 1
    elapsed=$((elapsed + 1))
    printf '.'
  done
  printf '\n'
  return 1
}

# Send signal to all PIDs (silently skips already-dead ones).
send_signal() {
  local sig="$1"; shift
  for pid in "$@"; do
    kill "-$sig" "$pid" 2>/dev/null || true
  done
}

lemonade_running() {
  curl -s --max-time 3 "${LEMONADE_API}/health" >/dev/null 2>&1
}

# ── Subcommands ────────────────────────────────────────────────────────────────

cmd_agent() {
  header "Stopping the llm-agent"

  local agent_pids
  agent_pids=$(pids_for "$AGENT_NAME")

  if [ -z "$agent_pids" ]; then
    ok "No '${AGENT_NAME}' process found"
  else
    # shellcheck disable=SC2086
    info "Found PIDs: $(echo $agent_pids | tr '\n' ' ')"
    # shellcheck disable=SC2086
    send_signal TERM $agent_pids
    info "Sent SIGTERM, waiting up to ${SHUTDOWN_GRACE_SECS}s for graceful exit"
    # shellcheck disable=SC2086
    if wait_for_exit "$SHUTDOWN_GRACE_SECS" $agent_pids; then
      ok "Agent stopped cleanly"
    else
      warn "Agent did not exit within ${SHUTDOWN_GRACE_SECS}s — escalating to SIGKILL"
      # shellcheck disable=SC2086
      send_signal KILL $agent_pids
      sleep 1
      ok "Agent force-killed"
    fi
  fi

  if [ "$ORPHAN_CLEANUP" = "1" ]; then
    # Playwright MCP keeps a headless Chrome around. If the agent was hard-killed
    # earlier, those Chrome processes can survive. Kill anything that looks like
    # a Playwright-spawned Chromium (matches only our own UID).
    local pw_pids
    pw_pids=$(pids_grep '/\.cache/ms-playwright/.*chrome\|playwright-mcp.*chrom\|chrome.*--remote-debugging-port' || true)
    if [ -n "$pw_pids" ]; then
      info "Cleaning up orphan Playwright Chrome processes"
      # shellcheck disable=SC2086
      send_signal TERM $pw_pids
      sleep 2
      # shellcheck disable=SC2086
      pw_pids=$(pids_grep '/\.cache/ms-playwright/.*chrome\|playwright-mcp.*chrom\|chrome.*--remote-debugging-port' || true)
      if [ -n "$pw_pids" ]; then
        # shellcheck disable=SC2086
        send_signal KILL $pw_pids
        ok "Orphan Chrome processes force-killed"
      else
        ok "Orphan Chrome processes stopped"
      fi
    fi

    # mcp-server-ports binary, if spawned directly (not via npx).
    local ports_pids
    ports_pids=$(pids_for "mcp-server-ports")
    if [ -n "$ports_pids" ]; then
      info "Cleaning up orphan mcp-server-ports"
      # shellcheck disable=SC2086
      send_signal TERM $ports_pids
      sleep 1
      # shellcheck disable=SC2086
      ports_pids=$(pids_for "mcp-server-ports")
      # shellcheck disable=SC2086
      [ -n "$ports_pids" ] && send_signal KILL $ports_pids
      ok "Orphan mcp-server-ports cleaned"
    fi
  fi
}

cmd_lemonade() {
  header "Stopping the Lemonade Server"

  if ! lemonade_running && [ -z "$(pids_for lemond)" ]; then
    ok "Lemonade is not running"
    return 0
  fi

  # Preferred: the official stop path. Idempotent; safe even if already down.
  if command -v lemonade-server >/dev/null 2>&1; then
    info "Sending official 'lemonade-server stop'"
    lemonade-server stop 2>/dev/null || true
  elif command -v lemonade >/dev/null 2>&1; then
    info "Sending official 'lemonade serve stop'"
    lemonade serve stop 2>/dev/null || true
  else
    warn "Neither lemonade-server nor lemonade CLI on PATH; will skip the official stop and rely on signal-kill"
  fi

  # Wait for the HTTP endpoint to go dark.
  local elapsed=0
  while lemonade_running; do
    sleep 1
    elapsed=$((elapsed + 1))
    if [ "$elapsed" -ge "$SHUTDOWN_GRACE_SECS" ]; then
      warn "Lemonade still serving HTTP after ${SHUTDOWN_GRACE_SECS}s — escalating"
      break
    fi
    printf '.'
  done
  [ "$elapsed" -gt 0 ] && printf '\n'

  # Force-kill any straggling processes. Catches BOTH 'lemonade-server serve'
  # wrappers AND the actual long-running lemond daemon (which the wrapper spawns
  # and which can survive a Ctrl+C of the wrapper).
  local stragglers
  stragglers=$(pids_grep "lemonade-server serve" || true)
  stragglers+=" $(pids_for lemond || true)"
  stragglers=$(echo "$stragglers" | tr ' ' '\n' | grep -v '^$' | sort -u | tr '\n' ' ')
  if [ -n "${stragglers// /}" ]; then
    info "Force-killing stragglers: ${stragglers}"
    # shellcheck disable=SC2086
    send_signal TERM $stragglers
    sleep 2
    # shellcheck disable=SC2086
    send_signal KILL $stragglers
  fi

  if lemonade_running || [ -n "$(pids_for lemond)" ]; then
    fail "Lemonade still appears to be running after all attempts. Check 'ps aux | grep lemond' manually."
    return 1
  fi

  ok "Lemonade stopped"
}

cmd_all() {
  # Agent first: gives any in-flight LLM call a chance to settle before we yank
  # the backend out from under it.
  cmd_agent
  cmd_lemonade
}

cmd_status() {
  header "Process status (own user only)"

  printf '  %sAgent (%s)%s\n' "$BOLD" "$AGENT_NAME" "$NC"
  local agent_pids
  agent_pids=$(pids_for "$AGENT_NAME")
  if [ -n "$agent_pids" ]; then
    # shellcheck disable=SC2086
    ps -o pid,etime,cmd -p $agent_pids | sed 's/^/    /'
  else
    printf '    %s(not running)%s\n' "$DIM" "$NC"
  fi
  echo ""

  printf '  %sLemonade%s\n' "$BOLD" "$NC"
  if lemonade_running; then
    local ver
    ver=$(curl -s --max-time 3 "${LEMONADE_API}/health" \
      | python3 -c "import sys,json; print(json.load(sys.stdin).get('version','?'))" 2>/dev/null || echo "?")
    printf '    HTTP at %s — v%s\n' "$LEMONADE_URL" "$ver"
  else
    printf '    %s(HTTP endpoint not responding at %s)%s\n' "$DIM" "$LEMONADE_URL" "$NC"
  fi
  local lemond_pids
  lemond_pids=$(pids_for "lemond")
  if [ -n "$lemond_pids" ]; then
    # shellcheck disable=SC2086
    ps -o pid,etime,cmd -p $lemond_pids | sed 's/^/    /'
  fi
  echo ""

  printf '  %sMCP children (own user)%s\n' "$BOLD" "$NC"
  local mcp_pids
  mcp_pids=$(pids_grep 'playwright/mcp\|server-filesystem\|mcp-server-fetch\|mcp-server-ports' || true)
  if [ -n "$mcp_pids" ]; then
    # shellcheck disable=SC2086
    ps -o pid,etime,cmd -p $mcp_pids | sed 's/^/    /'
  else
    printf '    %s(none running)%s\n' "$DIM" "$NC"
  fi
  echo ""

  printf '  %sOrphan Playwright Chrome%s\n' "$BOLD" "$NC"
  local pw_pids
  pw_pids=$(pids_grep '/\.cache/ms-playwright/.*chrome\|playwright-mcp.*chrom\|chrome.*--remote-debugging-port' || true)
  if [ -n "$pw_pids" ]; then
    # shellcheck disable=SC2086
    ps -o pid,etime,cmd -p $pw_pids | sed 's/^/    /'
  else
    printf '    %s(none)%s\n' "$DIM" "$NC"
  fi
}

cmd_help() {
  cat <<EOF
${BOLD}shutdown.sh${NC} — stop the llm-agent and/or the Lemonade Server

${BOLD}Usage:${NC}
  $0 [command]

${BOLD}Commands:${NC}
  ${BOLD}all${NC}           Stop both (default). Order: agent first, then Lemonade.
  ${BOLD}agent${NC}         Stop the llm-agent process(es), plus orphan MCP/Playwright Chrome.
  ${BOLD}lemonade${NC}      Stop the Lemonade Server (HTTP + stray lemond daemons).
  ${BOLD}status${NC}        Report what's currently running (agent / Lemonade / MCP / orphans).
  ${BOLD}help${NC}          This menu.

${BOLD}Environment:${NC}
  AGENT_NAME            ${DIM}process name to match (${AGENT_NAME})${NC}
  LEMONADE_URL          ${DIM}Lemonade base URL (${LEMONADE_URL})${NC}
  SHUTDOWN_GRACE_SECS   ${DIM}wait before SIGKILL escalation (${SHUTDOWN_GRACE_SECS})${NC}
  ORPHAN_CLEANUP        ${DIM}1 to clean Playwright Chrome leftovers (${ORPHAN_CLEANUP})${NC}

${BOLD}Notes:${NC}
  - The agent's MCP children (Playwright, filesystem, fetch, ports) are stdio
    subprocesses owned by the agent. A clean SIGTERM to the agent normally takes
    them down too. The orphan-cleanup pass exists for cases where the agent was
    SIGKILL'd or the system crashed mid-query — Playwright in particular leaks
    headless Chrome processes when its parent dies hard.
  - The ${BOLD}all${NC} command stops the agent ${BOLD}before${NC} Lemonade so that any in-flight LLM
    call gets a clean end-of-stream rather than a TCP RST.
  - This script only touches processes owned by your own UID. It will not kill
    a Lemonade or agent owned by another user.
EOF
}

# ── Main ───────────────────────────────────────────────────────────────────────

COMMAND="${1:-all}"

if [ "$COMMAND" != "help" ] && [ "$COMMAND" != "--help" ] && [ "$COMMAND" != "-h" ]; then
  printf '%sshutdown.sh%s — Lemonade: %s%s%s\n' "$BOLD" "$NC" "$DIM" "$LEMONADE_URL" "$NC"
  printf '%s─────────────────────────────────────────────%s\n' "$DIM" "$NC"
fi

case "$COMMAND" in
  all|"")           cmd_all ;;
  agent)            cmd_agent ;;
  lemonade)         cmd_lemonade ;;
  status)           cmd_status ;;
  help|--help|-h)   cmd_help ;;
  *)
    fail "Unknown command: ${COMMAND}"
    cmd_help
    exit 1
    ;;
esac
