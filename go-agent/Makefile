.PHONY: deps vet build test mcp-test run query clean

# Version: from git tag if available, else the semver baseline 1.0.0.
# `git describe` returns e.g. "v1.0.0" on a tagged commit, "v1.0.0-3-gHASH"
# when ahead of the tag, and "v1.0.0-3-gHASH-dirty" with uncommitted changes.
# The leading "v" is stripped so the displayed string is plain semver.
# Override on the command line: `make build VERSION=1.2.3`.
GITVER  := $(shell git describe --tags --dirty --always 2>/dev/null | sed 's/^v//')
VERSION ?= $(or $(GITVER),1.0.0)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

# Resolve dependencies and generate go.sum
deps:
	go mod tidy

# Static analysis. Cheap; run before every build.
vet:
	go vet ./...
	cd ../mcp-servers/ports && go vet ./...

# Build everything (agent + MCP servers). Depends on vet so a broken build
# fails fast on obvious issues before the long compile step.
build: vet
	go build $(LDFLAGS) -o llm-agent .
	cd ../mcp-servers/ports && go build -o mcp-server-ports .

# Run all tests (agent + MCP servers)
test:
	go test ./... -count=1 -timeout 30s
	cd ../mcp-servers/ports && go test ./... -count=1 -timeout 30s

# Test MCP SSE gateway (agent must be running with --web)
# Usage: make mcp-test  or  make mcp-test MCP_HOST=192.168.1.191:3131
MCP_HOST ?= localhost:3131
mcp-test:
	@echo "=== Health ==="
	@curl -sf http://$(MCP_HOST)/api/v1/health | python3 -m json.tool || echo "FAIL: agent not reachable at $(MCP_HOST)"
	@echo ""
	@echo "=== Tools (REST) ==="
	@curl -sf http://$(MCP_HOST)/api/v1/tools | python3 -c "import sys,json; d=json.load(sys.stdin); print(f'{d[\"count\"]} tools available')" || echo "FAIL"
	@echo ""
	@echo "=== MCP SSE Test ==="
	@echo "Run these in two terminals:"
	@echo ""
	@echo "  Terminal 1 (SSE stream):"
	@echo "    curl -N http://$(MCP_HOST)/mcp/sse"
	@echo ""
	@echo "  Terminal 2 (use sessionId from Terminal 1):"
	@echo "    # List tools (agent_query + raw tools)"
	@echo "    curl -X POST 'http://$(MCP_HOST)/mcp/message?sessionId=mcp-1' -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{}}'"
	@echo ""
	@echo "    # Natural language query (full LLM loop)"
	@echo "    curl -X POST 'http://$(MCP_HOST)/mcp/message?sessionId=mcp-1' -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"agent_query\",\"arguments\":{\"query\":\"is port 13305 in use?\"}}}'"
	@echo ""
	@echo "    # Direct tool call (no LLM)"
	@echo "    curl -X POST 'http://$(MCP_HOST)/mcp/message?sessionId=mcp-1' -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"check_port\",\"arguments\":{\"port\":13305}}}'"

# Build and run in interactive mode
run: build
	./llm-agent

# Build and run a single query
query: build
	@test -n "$(Q)" || (echo "Usage: make query Q='your question'" && exit 1)
	./llm-agent "$(Q)"

# Remove build artifacts
clean:
	rm -f llm-agent
	rm -f ../mcp-servers/ports/mcp-server-ports
