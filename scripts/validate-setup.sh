#!/usr/bin/env bash

# This file is part of the SmartTechLabs AI Workshop material.
# Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
# SmartTechLabs is also available for AI projects and consulting.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# See the LICENSE file in the project root or
# http://www.apache.org/licenses/LICENSE-2.0 for the full text.

# validate-setup.sh — Check that all prerequisites are in place
# Run: chmod +x validate-setup.sh && ./validate-setup.sh

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "  ${GREEN}✓${NC} $1"; }
fail() { echo -e "  ${RED}✗${NC} $1"; ERRORS=$((ERRORS + 1)); }
warn() { echo -e "  ${YELLOW}⚠${NC} $1"; }

ERRORS=0

echo ""
echo "=== Lemonade MCP Setup Validator ==="
echo ""

# --- Node.js ---
echo "Checking Node.js..."
if command -v node &>/dev/null; then
    NODE_VER=$(node --version)
    NODE_MAJOR=$(echo "$NODE_VER" | sed 's/v//' | cut -d. -f1)
    if [ "$NODE_MAJOR" -ge 18 ]; then
        pass "Node.js $NODE_VER (>= 18 required)"
    else
        fail "Node.js $NODE_VER found but >= 18 required"
    fi
else
    fail "Node.js not found — install from https://nodejs.org"
fi

# --- npx ---
echo "Checking npx..."
if command -v npx &>/dev/null; then
    pass "npx available"
else
    fail "npx not found — comes with Node.js"
fi

# --- Python ---
echo "Checking Python..."
if command -v python3 &>/dev/null; then
    PY_VER=$(python3 --version 2>&1)
    pass "$PY_VER"
elif command -v python &>/dev/null; then
    PY_VER=$(python --version 2>&1)
    pass "$PY_VER"
else
    fail "Python not found — install Python >= 3.10"
fi

# --- huggingface_hub with MCP ---
echo "Checking huggingface_hub[mcp]..."
if python3 -c "from huggingface_hub import Agent" 2>/dev/null || \
   python3 -c "import huggingface_hub; print(huggingface_hub.__version__)" 2>/dev/null; then
    HF_VER=$(python3 -c "import huggingface_hub; print(huggingface_hub.__version__)" 2>/dev/null || echo "unknown")
    pass "huggingface_hub $HF_VER installed"
else
    fail "huggingface_hub[mcp] not installed — run: pip install 'huggingface_hub[mcp]>=0.33.2'"
fi

# --- tiny-agents CLI ---
echo "Checking tiny-agents CLI..."
if command -v tiny-agents &>/dev/null; then
    pass "tiny-agents CLI available"
else
    warn "tiny-agents CLI not found in PATH (may still work via 'python -m huggingface_hub.cli.tiny_agents')"
fi

# --- Lemonade Server ---
echo "Checking Lemonade Server..."
if curl -s --max-time 3 http://localhost:13305/api/v1/health >/dev/null 2>&1; then
    HEALTH=$(curl -s http://localhost:13305/api/v1/health)
    VERSION=$(echo "$HEALTH" | python3 -c "import sys,json; print(json.load(sys.stdin).get('version','?'))" 2>/dev/null || echo "?")
    MODEL=$(echo "$HEALTH" | python3 -c "import sys,json; print(json.load(sys.stdin).get('model_loaded','none'))" 2>/dev/null || echo "none")
    pass "Lemonade Server v$VERSION running"
    if [ "$MODEL" != "none" ] && [ "$MODEL" != "null" ] && [ -n "$MODEL" ]; then
        pass "Model loaded: $MODEL"
    else
        warn "No model currently loaded — will auto-load on first request"
    fi
else
    fail "Lemonade Server not reachable at http://localhost:13305 — start with: lemonade-server serve"
fi

# --- agent.json ---
echo "Checking agent.json..."
if [ -f "agent.json" ]; then
    pass "agent.json found"
    # Quick syntax check
    if python3 -c "import json; json.load(open('agent.json'))" 2>/dev/null; then
        pass "agent.json is valid JSON"
    else
        fail "agent.json has syntax errors"
    fi
else
    fail "agent.json not found in current directory"
fi

# --- Summary ---
echo ""
echo "=== Results ==="
if [ "$ERRORS" -eq 0 ]; then
    echo -e "${GREEN}All checks passed!${NC} Run: tiny-agents run ./agent.json"
else
    echo -e "${RED}$ERRORS issue(s) found.${NC} Fix the items above, then re-run this script."
fi
echo ""
