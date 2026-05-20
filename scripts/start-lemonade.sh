#!/usr/bin/env bash

# This file is part of the SmartTechLabs AI Workshop material.
# Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
# SmartTechLabs is also available for AI projects and consulting.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# See the LICENSE file in the project root or
# http://www.apache.org/licenses/LICENSE-2.0 for the full text.

# start-lemonade.sh — Manage Lemonade Server: start, stop, status, pull, load, test
#
# Usage:
#   ./start-lemonade.sh                    # start server + load default model
#   ./start-lemonade.sh start              # start server + load default model
#   ./start-lemonade.sh start Qwen3-8B     # start server + load specific model
#   ./start-lemonade.sh stop               # gracefully stop server
#   ./start-lemonade.sh status             # show server health + loaded model
#   ./start-lemonade.sh list               # list available/downloaded models
#   ./start-lemonade.sh pull <model>       # download model without loading
#   ./start-lemonade.sh load <model>       # load a model (server must be running)
#   ./start-lemonade.sh test               # run smoke tests
#   ./start-lemonade.sh restart            # stop + start
#
# Environment variables:
#   LEMONADE_MODEL       default model name
#   LEMONADE_URL         server URL (default: http://localhost:13305)
#   LEMONADE_CTX_SIZE    context window size (default: 32768)
#   LEMONADE_LLAMACPP    backend: vulkan, rocm, cpu (default: server default)
#   LEMONADE_PORT        server port (default: 13305)

set -euo pipefail

# ── Configuration ──────────────────────────────────────────────────────────────
DEFAULT_MODEL="Qwen3-Coder-30B-A3B-Instruct-GGUF"
LEMONADE_URL="${LEMONADE_URL:-http://localhost:13305}"
API="${LEMONADE_URL}/api/v1"
CTX_SIZE="${LEMONADE_CTX_SIZE:-32768}"
STARTUP_TIMEOUT=60
LOAD_TIMEOUT=300

# ── Colors ─────────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

info()  { echo -e "${CYAN}[INFO]${NC}  $1"; }
ok()    { echo -e "${GREEN}[OK]${NC}    $1"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $1"; }
fail()  { echo -e "${RED}[FAIL]${NC}  $1"; }
header(){ echo -e "\n${BOLD}═══ $1 ═══${NC}\n"; }

# ── Helpers ────────────────────────────────────────────────────────────────────

# Check if lemonade-server is installed, install if not.
ensure_installed() {
  if command -v lemonade-server &>/dev/null; then
    return 0
  fi

  warn "lemonade-server not found in PATH"
  info "Installing lemonade-server via pip..."

  local pip_cmd=""
  if command -v pip &>/dev/null; then
    pip_cmd="pip"
  elif command -v pip3 &>/dev/null; then
    pip_cmd="pip3"
  else
    fail "Neither pip nor pip3 found. Install Python >= 3.10 first."
    exit 1
  fi

  $pip_cmd install lemonade-server
  if ! command -v lemonade-server &>/dev/null; then
    fail "Installation completed but lemonade-server not found in PATH."
    fail "  See https://lemonade-server.ai/docs/server/"
    exit 1
  fi
  ok "lemonade-server installed successfully"
}

# Check if the server is running and reachable.
is_running() {
  curl -s --max-time 3 "${API}/health" &>/dev/null
}

# Get the currently loaded model name (empty string if none).
get_loaded_model() {
  local health
  health=$(curl -s --max-time 5 "${API}/health" 2>/dev/null) || true
  echo "$health" | python3 -c "
import sys, json
try:
    h = json.load(sys.stdin)
    loaded = h.get('all_models_loaded', [])
    if loaded:
        entry = loaded[0]
        if isinstance(entry, dict):
            print(entry.get('model_name', ''))
        else:
            print(entry)
    else:
        m = h.get('model_loaded') or h.get('model', '') or ''
        if m and m != 'null' and m != 'None':
            print(m)
except:
    pass
" 2>/dev/null || true
}

# Wait for the server to become responsive.
wait_for_server() {
  info "Waiting for server to start (timeout: ${STARTUP_TIMEOUT}s)..."
  local elapsed=0
  while ! is_running; do
    sleep 2
    elapsed=$((elapsed + 2))
    if [ "$elapsed" -ge "$STARTUP_TIMEOUT" ]; then
      fail "Server did not start within ${STARTUP_TIMEOUT}s"
      return 1
    fi
    printf "."
  done
  echo ""
  ok "Lemonade Server is up at ${LEMONADE_URL}"
}

# ── Commands ───────────────────────────────────────────────────────────────────

cmd_start() {
  local model="${1:-${LEMONADE_MODEL:-$DEFAULT_MODEL}}"
  ensure_installed

  header "Starting Lemonade Server"

  local ver
  ver=$(lemonade-server --version 2>/dev/null || echo "unknown")
  info "lemonade-server $ver"

  if is_running; then
    ok "Server already running at ${LEMONADE_URL}"
  else
    local serve_args=("serve" "--ctx-size" "$CTX_SIZE")
    [ -n "${LEMONADE_PORT:-}" ] && serve_args+=("--port" "$LEMONADE_PORT")
    [ -n "${LEMONADE_LLAMACPP:-}" ] && serve_args+=("--llamacpp" "$LEMONADE_LLAMACPP")

    local default_log_dir="${XDG_CACHE_HOME:-$HOME/.cache}/lemonade"
    mkdir -p "$default_log_dir"
    LEMONADE_LOG="${LEMONADE_LOG:-$default_log_dir/server.log}"
    info "Starting: lemonade-server ${serve_args[*]}"
    info "Log file: ${LEMONADE_LOG}"
    lemonade-server "${serve_args[@]}" >> "$LEMONADE_LOG" 2>&1 &
    local pid=$!
    disown "$pid" 2>/dev/null || true
    info "Server PID: ${pid}"

    if ! wait_for_server; then
      kill "$pid" 2>/dev/null || true
      exit 1
    fi
  fi

  # Load model.
  cmd_load "$model"
}

cmd_stop() {
  header "Stopping Lemonade Server"

  # Even if the HTTP endpoint isn't responding, there may be a stray lemond
  # daemon left over from an earlier 'serve' that was killed before its child
  # could shut down. So we don't early-return here; we just note the state.
  if ! is_running && ! pgrep -u "$(id -u)" -x lemond >/dev/null 2>&1; then
    ok "Server is not running"
    return 0
  fi

  if is_running; then
    info "Sending stop command..."
    lemonade-server stop 2>/dev/null || true

    # Wait for the HTTP endpoint to go dark.
    local elapsed=0
    while is_running; do
      sleep 1
      elapsed=$((elapsed + 1))
      if [ "$elapsed" -ge 15 ]; then
        warn "Server did not stop gracefully within 15s, escalating"
        break
      fi
      printf "."
    done
    echo ""
  fi

  # Force-kill any straggling processes. The lemonade-server wrapper spawns
  # an actual long-running 'lemond' daemon that can outlive its parent if
  # Ctrl+C is used instead of 'lemonade-server stop'. Catch both patterns.
  local stragglers
  stragglers=$(pgrep -u "$(id -u)" -f "lemonade-server serve" 2>/dev/null || true)
  stragglers+=" $(pgrep -u "$(id -u)" -x lemond 2>/dev/null || true)"
  stragglers=$(echo "$stragglers" | tr ' ' '\n' | grep -v '^$' | sort -u | tr '\n' ' ')
  if [ -n "${stragglers// /}" ]; then
    warn "Stray processes still alive (${stragglers}) — force-killing"
    # shellcheck disable=SC2086
    kill -TERM $stragglers 2>/dev/null || true
    sleep 2
    # shellcheck disable=SC2086
    kill -KILL $stragglers 2>/dev/null || true
  fi

  if is_running || pgrep -u "$(id -u)" -x lemond >/dev/null 2>&1; then
    fail "Lemonade still appears to be running after all attempts. Try 'scripts/shutdown.sh status' for details."
    return 1
  fi

  ok "Server stopped"
}

cmd_restart() {
  local model="${1:-${LEMONADE_MODEL:-$DEFAULT_MODEL}}"
  cmd_stop
  cmd_start "$model"
}

cmd_status() {
  header "Lemonade Server Status"

  if ! is_running; then
    fail "Server is not running at ${LEMONADE_URL}"
    return 1
  fi

  ok "Server is running at ${LEMONADE_URL}"

  local health
  health=$(curl -s --max-time 5 "${API}/health")
  echo "$health" | python3 -c "
import sys, json
h = json.load(sys.stdin)
print(f'  Version:  {h.get(\"version\", \"unknown\")}')
print(f'  Status:   {h.get(\"status\", \"unknown\")}')
loaded = h.get('all_models_loaded', [])
model = h.get('model_loaded')
if loaded:
    print(f'  Models:   {', '.join(loaded)}')
elif model and model != 'null':
    print(f'  Model:    {model}')
else:
    print(f'  Model:    (none loaded)')
" 2>/dev/null || echo "$health"
}

cmd_list() {
  header "Available Models"

  if ! is_running; then
    fail "Server is not running. Start it first: $0 start"
    return 1
  fi

  local models
  models=$(curl -s --max-time 10 "${API}/models")

  echo "$models" | python3 -c "
import sys, json
d = json.load(sys.stdin)
models = d.get('data', d) if isinstance(d, dict) else d
if not models:
    print('  (no models available)')
    sys.exit(0)
for m in models:
    if isinstance(m, dict):
        name = m.get('id', m.get('name', 'unknown'))
        print(f'  - {name}')
    else:
        print(f'  - {m}')
" 2>/dev/null || echo "$models"
}

cmd_pull() {
  local model="${1:-}"
  if [ -z "$model" ]; then
    fail "Usage: $0 pull <model-name>"
    exit 1
  fi

  header "Pulling Model: ${model}"

  if ! is_running; then
    fail "Server is not running. Start it first: $0 start"
    return 1
  fi

  info "Downloading ${model} (this may take a while for large models)..."
  local response
  response=$(curl -s --max-time "$LOAD_TIMEOUT" -X POST "${API}/pull" \
    -H "Content-Type: application/json" \
    -d "{\"model_name\": \"${model}\", \"stream\": false}" 2>&1) || true

  if echo "$response" | python3 -c "
import sys, json
r = json.load(sys.stdin)
if r.get('error'):
    print(r['error'])
    sys.exit(1)
" 2>/dev/null; then
    ok "Model ${model} pulled successfully"
  else
    fail "Pull failed: ${response}"
    return 1
  fi
}

cmd_load() {
  local model="${1:-${LEMONADE_MODEL:-$DEFAULT_MODEL}}"

  header "Loading Model: ${model}"

  if ! is_running; then
    fail "Server is not running. Start it first: $0 start"
    return 1
  fi

  # Check if this model is already loaded.
  local loaded
  loaded=$(get_loaded_model)
  if [ "$loaded" = "$model" ]; then
    ok "Model ${model} is already loaded"
    return 0
  fi

  if [ -n "$loaded" ]; then
    info "Currently loaded: ${loaded}"
    info "Switching to: ${model}"
  fi

  info "Loading ${model} (context: ${CTX_SIZE} tokens)..."
  local response
  response=$(curl -s --max-time "$LOAD_TIMEOUT" -X POST "${API}/load" \
    -H "Content-Type: application/json" \
    -d "{\"model_name\": \"${model}\", \"context_length\": ${CTX_SIZE}}" 2>&1) || true

  # Check for success.
  local status
  status=$(echo "$response" | python3 -c "
import sys, json
try:
    r = json.load(sys.stdin)
    if r.get('error'):
        print('error:' + str(r['error']))
    elif r.get('status') == 'success':
        print('ok')
    else:
        print('ok')
except:
    print('error:parse')
" 2>/dev/null || echo "error:unknown")

  if [[ "$status" == "ok" ]]; then
    ok "Model ${model} loaded successfully"
    return 0
  fi

  # Load failed -- try pulling first, then retry.
  warn "Load failed (${status}), attempting to pull model first..."
  cmd_pull "$model"

  info "Retrying load..."
  response=$(curl -s --max-time "$LOAD_TIMEOUT" -X POST "${API}/load" \
    -H "Content-Type: application/json" \
    -d "{\"model_name\": \"${model}\", \"context_length\": ${CTX_SIZE}}" 2>&1) || true

  status=$(echo "$response" | python3 -c "
import sys, json
try:
    r = json.load(sys.stdin)
    if r.get('error'):
        print('error:' + str(r['error']))
    else:
        print('ok')
except:
    print('error:parse')
" 2>/dev/null || echo "error:unknown")

  if [[ "$status" == "ok" ]]; then
    ok "Model ${model} loaded successfully"
  else
    fail "Failed to load ${model}: ${response}"
    return 1
  fi
}

cmd_test() {
  header "Running Smoke Tests"

  if ! is_running; then
    fail "Server is not running. Start it first: $0 start"
    return 1
  fi

  local model
  model=$(get_loaded_model)
  if [ -z "$model" ]; then
    fail "No model loaded. Load one first: $0 load <model>"
    return 1
  fi
  info "Testing with model: ${model}"

  local passed=0
  local failed=0

  run_test() {
    local name="$1"
    local cmd="$2"
    local check="$3"

    echo -e "\n${BOLD}Test: ${name}${NC}"
    local response
    response=$(eval "$cmd" 2>&1) || true

    if eval "$check" <<< "$response" &>/dev/null; then
      ok "${name}"
      passed=$((passed + 1))
    else
      fail "${name}"
      warn "Response: $(echo "$response" | head -3)"
      failed=$((failed + 1))
    fi
  }

  run_test "Health endpoint" \
    "curl -s '${API}/health'" \
    "python3 -c 'import sys,json; json.load(sys.stdin)'"

  run_test "Models endpoint" \
    "curl -s '${API}/models'" \
    "python3 -c 'import sys,json; d=json.load(sys.stdin); assert isinstance(d, (list, dict))'"

  run_test "System info" \
    "curl -s '${API}/system-info'" \
    "python3 -c 'import sys,json; json.load(sys.stdin)'"

  run_test "Chat completion" \
    "curl -s --max-time 120 -X POST '${API}/chat/completions' \
      -H 'Content-Type: application/json' \
      -d '{
        \"model\": \"${model}\",
        \"messages\": [{\"role\": \"user\", \"content\": \"Say hello in exactly 5 words.\"}],
        \"stream\": false
      }'" \
    "python3 -c 'import sys,json; r=json.load(sys.stdin); assert r[\"choices\"][0][\"message\"][\"content\"]'"

  run_test "Chat with system message" \
    "curl -s --max-time 120 -X POST '${API}/chat/completions' \
      -H 'Content-Type: application/json' \
      -d '{
        \"model\": \"${model}\",
        \"messages\": [
          {\"role\": \"system\", \"content\": \"You are a helpful assistant.\"},
          {\"role\": \"user\", \"content\": \"What is 2+2? Reply with just the number.\"}
        ],
        \"stream\": false
      }'" \
    "python3 -c 'import sys,json; r=json.load(sys.stdin); c=r[\"choices\"][0][\"message\"][\"content\"]; assert \"4\" in c'"

  run_test "Stats endpoint" \
    "curl -s '${API}/stats'" \
    "python3 -c 'import sys,json; json.load(sys.stdin)'"

  header "Test Results"
  echo -e "  ${GREEN}Passed: ${passed}${NC}"
  echo -e "  ${RED}Failed: ${failed}${NC}"
  echo -e "  Total:  $((passed + failed))"

  [ "$failed" -eq 0 ] && ok "All smoke tests passed!" || warn "Some tests failed."
}

cmd_config() {
  local param="${1:-}"
  local value="${2:-}"

  if [ -z "$param" ]; then
    # Show current configuration.
    header "Current Configuration"

    if ! is_running; then
      fail "Server is not running"
      return 1
    fi

    local health
    health=$(curl -s --max-time 5 "${API}/health")
    local model
    model=$(get_loaded_model)

    echo -e "  ${BOLD}Server${NC}"
    echo -e "    URL:         ${LEMONADE_URL}"
    echo -e "    CTX_SIZE:    ${CTX_SIZE} (startup default)"

    if [ -n "$model" ]; then
      echo ""
      echo -e "  ${BOLD}Loaded Model${NC}"
      echo -e "    Name:        ${model}"

      # Query the backend for actual context size.
      local backend_url
      backend_url=$(echo "$health" | python3 -c "
import sys, json
h = json.load(sys.stdin)
loaded = h.get('all_models_loaded', [])
if loaded:
    print(loaded[0].get('backend_url', ''))
" 2>/dev/null || true)

      if [ -n "$backend_url" ]; then
        echo -e "    Backend:     ${backend_url}"
      fi
    else
      echo -e "\n  ${DIM}(no model loaded)${NC}"
    fi

    echo ""
    echo -e "  ${BOLD}Configurable Parameters${NC}"
    echo -e "    ctx-size     Context window size in tokens"
    echo ""
    echo -e "  ${DIM}Usage: $0 config ctx-size 32768${NC}"
    return 0
  fi

  case "$param" in
    ctx-size|context|ctx)
      if [ -z "$value" ]; then
        fail "Usage: $0 config ctx-size <tokens>"
        fail "  Example: $0 config ctx-size 32768"
        return 1
      fi

      # Validate it's a number.
      if ! [[ "$value" =~ ^[0-9]+$ ]]; then
        fail "Context size must be a number, got: ${value}"
        return 1
      fi

      # Context size is a server-level setting (--ctx-size flag).
      # It cannot be changed via the load API -- requires a server restart.
      local model
      model=$(get_loaded_model)

      header "Updating context size: ${value} tokens"
      warn "Context size is a server-level setting. This requires a server restart."

      if is_running; then
        info "Stopping server..."
        cmd_stop
      fi

      # Override CTX_SIZE for the restart.
      CTX_SIZE="$value"
      export LEMONADE_CTX_SIZE="$value"

      info "Restarting server with --ctx-size ${value}..."
      cmd_start "${model:-${LEMONADE_MODEL:-$DEFAULT_MODEL}}"
      ;;
    *)
      fail "Unknown parameter: ${param}"
      info "Available parameters: ctx-size"
      return 1
      ;;
  esac
}

cmd_help() {
  cat <<EOF
${BOLD}start-lemonade.sh${NC} — Manage Lemonade Server

${BOLD}Usage:${NC}
  $0 [command] [args]

${BOLD}Commands:${NC}
  start [model]          Start server and load model (default: ${DEFAULT_MODEL})
  stop                   Gracefully stop the server
  restart [model]        Stop and restart with optional model
  status                 Show server health and loaded model
  list                   List available/downloaded models
  pull <model>           Download a model without loading it
  load <model>           Load a model (server must be running)
  config                 Show current configuration
  config ctx-size <n>    Change context window size (restarts the server)
  test                   Run smoke tests against the server
  help                   Show this help

${BOLD}Examples:${NC}
  $0                                    # start + load default model
  $0 start Qwen3-8B-GGUF               # start + load specific model
  $0 pull Qwen3-4B-GGUF                # pre-download a model
  $0 load Qwen3-4B-GGUF                # switch to a different model
  $0 config ctx-size 32768              # increase context window (restarts server)
  $0 stop                               # shut down the server
  $0 status                              # check what's running

${BOLD}Environment:${NC}
  LEMONADE_MODEL       Default model (${DEFAULT_MODEL})
  LEMONADE_URL         Server URL (${LEMONADE_URL})
  LEMONADE_CTX_SIZE    Context window tokens (${CTX_SIZE})
  LEMONADE_PORT        Server port
  LEMONADE_LLAMACPP    Backend: vulkan, rocm, cpu
EOF
}

# ── Main ───────────────────────────────────────────────────────────────────────

COMMAND="${1:-start}"

# Always show the command menu.
echo -e "${BOLD}start-lemonade.sh${NC} — Lemonade Server Manager"
echo -e "${DIM}─────────────────────────────────────────────${NC}"
echo -e "  start ${DIM}[model]${NC}    Start server + load model"
echo -e "  stop             Stop the server"
echo -e "  restart ${DIM}[model]${NC}  Restart with optional model"
echo -e "  status           Show health + loaded model"
echo -e "  list             List available models"
echo -e "  pull ${DIM}<model>${NC}     Download a model"
echo -e "  load ${DIM}<model>${NC}     Load a model"
echo -e "  config ${DIM}[param]${NC}   Show/change config (e.g. ctx-size)"
echo -e "  test             Run smoke tests"
echo -e "  help             Full help with examples"
echo -e "${DIM}─────────────────────────────────────────────${NC}"
echo ""

# If first arg looks like a model name (not a command), treat as: start <model>
case "$COMMAND" in
  start|stop|restart|status|list|pull|load|config|test|help|--help|-h)
    shift || true
    ;;
  *)
    # First arg is a model name.
    COMMAND="start"
    ;;
esac

case "$COMMAND" in
  start)    cmd_start "$@" ;;
  stop)     cmd_stop ;;
  restart)  cmd_restart "$@" ;;
  status)   cmd_status ;;
  list)     cmd_list ;;
  pull)     cmd_pull "$@" ;;
  load)     cmd_load "$@" ;;
  config)   cmd_config "$@" ;;
  test)     cmd_test ;;
  help|--help|-h) cmd_help ;;
  *)
    fail "Unknown command: ${COMMAND}"
    cmd_help
    exit 1
    ;;
esac
