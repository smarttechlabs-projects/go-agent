# Agent Setup & Usage Guide

A standalone reference for the Go agent (`go-agent/`): building it, pointing it at any supported LLM endpoint, turning on observability, running useful queries, and the test suite. Companion to the blog series.

**This doc vs. the blog series.** This guide is for someone in front of the code right now, looking up a specific command or schema. The blog series is the long-form tutorial:

- [Part 6: Running It Yourself](blog-part-6-running-it-yourself.md) — narrative walkthrough of every supported backend with full recipes and trade-offs
- [Part 7: Observability](blog-part-7-observability.md) — the four visibility layers in depth, plus investigation walkthroughs
- [Part 3](blog-part-3-agent-loop.md) and [Part 4](blog-part-4-code-walkthrough.md) — how the agent loop works internally

---

## 1. What you get

A single Go binary (`llm-agent`) that:

- Talks to any OpenAI-compatible LLM API (Lemonade, LM Studio, vLLM, Ollama, OpenAI, Groq, Together, Mistral, DeepSeek, OpenRouter) plus native adapters for Google Gemini and Anthropic Claude.
- Spawns MCP tool servers as stdio subprocesses: `@playwright/mcp` (headless browser), `@modelcontextprotocol/server-filesystem` (scoped file I/O), `mcp-server-fetch` (URL → markdown), and the bundled `mcp-server-ports` (port scanner).
- Runs the agent loop (LLM → tool call → result → LLM → ... → final answer) with safety limits, retries, and parallel tool dispatch.
- Optionally exposes a REST API, an SSE event stream, an MCP gateway, and OpenTelemetry traces.

---

## 2. Prerequisites

| Tool | Why | Minimum |
|---|---|---|
| Go | Build the agent | 1.26 |
| Node.js | `npx` spawns Playwright and filesystem MCP servers | 18 |
| `uv` | `uvx` spawns `mcp-server-fetch` | 0.4 |
| LLM backend | Generation | one of the [supported endpoints](#4-llm-provider-endpoints) |

Run `scripts/validate-setup.sh` for a pre-flight check.

---

## 3. Build & configure

### Build

```bash
cd go-agent
make build           # produces ./llm-agent with version stamped via -ldflags
./llm-agent -version
```

### Configure (`agent.json`)

`agent.json` declares the model, endpoint, and which MCP servers to spawn. The shipped file points at Lemonade:

```json
{
  "model": "Qwen3-Coder-30B-A3B-Instruct-GGUF",
  "endpointUrl": "http://localhost:13305/api/v1",
  "servers": [
    { "type": "stdio", "config": { "command": "npx", "args": ["-y", "@playwright/mcp@latest", "--headless"] } },
    { "type": "stdio", "config": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", ".", "${HOME}/Documents"] } },
    { "type": "stdio", "config": { "command": "uvx", "args": ["mcp-server-fetch"] } },
    { "type": "stdio", "config": { "command": "../mcp-servers/ports/mcp-server-ports" } }
  ]
}
```

Key fields (full reference in [`config.go`](../go-agent/config.go)):

| Field | Purpose |
|---|---|
| `model` | Model identifier the backend will load — passed verbatim |
| `endpointUrl` | Base URL of the LLM API; the provider is auto-detected from the host |
| `provider` | Override auto-detection: `openai`, `gemini`, `anthropic`, or `auto` |
| `apiKey` | Optional in config; usually set via `LLM_API_KEY` / `OPENAI_API_KEY` / `GEMINI_API_KEY` / `ANTHROPIC_API_KEY` env vars |
| `toolCallStyle` | `auto` (default), `native`, or `text` — detected from model name |
| `servers[].config.allowTools` | Filter which tools a server exposes (Go agent only) |
| `maxToolRounds`, `timeoutSeconds`, `maxTokenBudget`, ... | Per-query safety limits |
| `requireApproval` | Glob patterns for tools that need human approval before running |

Environment variables in JSON strings are expanded at load time (`${HOME}`, etc.).

---

## 4. LLM Provider Endpoints (quick reference)

For the narrative walkthrough with trade-offs and per-backend caveats, see [Part 6: Running It Yourself](blog-part-6-running-it-yourself.md). The recipes:

| Backend | Command |
|---|---|
| **Lemonade** (default) | `scripts/start-lemonade.sh start` then `./llm-agent` |
| **LM Studio** | `./llm-agent -endpoint http://localhost:1234/v1 -model "qwen2.5-coder-32b-instruct"` |
| **vLLM** | `./llm-agent -endpoint http://localhost:8000/v1 -model "Qwen/Qwen2.5-Coder-32B-Instruct"` (after starting vLLM with `--max-model-len 32768`) |
| **Ollama** | `./llm-agent -endpoint http://localhost:11434/v1 -model qwen2.5-coder:32b` (avoid `-stream`; bump `num_ctx` via Modelfile) |
| **OpenAI / Groq / Together / Mistral / DeepSeek / OpenRouter** | `LLM_API_KEY=… ./llm-agent -endpoint <provider-url>/v1 -model <name>` |
| **Google Gemini** | `GEMINI_API_KEY=… ./llm-agent -endpoint https://generativelanguage.googleapis.com/v1beta -model gemini-2.5-flash` |
| **Anthropic Claude** | `ANTHROPIC_API_KEY=… ./llm-agent -endpoint https://api.anthropic.com -model claude-sonnet-4-5` |

Provider is auto-detected from the URL (`googleapis.com`/`gemini` → Gemini, `anthropic.com` → Anthropic, else OpenAI-compatible). Override with `-provider`.

For a feature-by-feature comparison (streaming format, model management APIs, context detection), see [tool-migration.md](tool-migration.md).

### Common CLI flags

| Flag | Purpose |
|---|---|
| `-config <path>` | Use a non-default `agent.json` |
| `-endpoint <url>` | Override `endpointUrl` (include `/v1` — the override is not re-normalized) |
| `-model <name>` | Override `model` |
| `-api-key <key>` | Override key resolution |
| `-provider <name>` | Force `openai` / `gemini` / `anthropic` instead of URL-based detection |
| `-tool-style <s>` | Force `native` or `text` parsing |
| `-stream` | Enable streaming for TTFT metrics |
| `-v` / `-verbose` | Colorized data-flow trace |
| `-log-format json` | Emit one JSON object per line (for log aggregators) |
| `-web <addr>` | Start dashboard + REST API + MCP gateway |
| `-otel-endpoint <addr>` | OTLP/HTTP traces + metrics (see Part 7) |

---

## 5. Observability (quick reference)

For the narrative walkthrough, span/instrument tables, PromQL examples, and investigation walkthroughs, see [Part 7: Observability](blog-part-7-observability.md). The four layers compose freely:

| Layer | Flag | What it gives you |
|---|---|---|
| Verbose CLI logs | `-v` / `-verbose` | Colorized terminal output, one line per step |
| Web dashboard + REST API + MCP gateway | `-web <addr>` | Live dashboard at `http://addr/`, SSE event stream at `/events`, full REST API under `/api/v1/*`, MCP gateway under `/mcp*` |
| OTel traces | `-otel-endpoint <addr>` | OTLP/HTTP spans for every query, round, LLM call, tool call |
| OTel metrics | `-otel-endpoint <addr>` | Same flag — six instruments (counters + histograms) on a 15-second cadence |

Full-observability one-liner:

```bash
./llm-agent -v -web localhost:3131 -otel-endpoint localhost:4318 "your question"
```

### Web routes (when `-web` is on)

| Path | Use |
|---|---|
| `/` | Live dashboard (embedded SPA, no build step) |
| `/events` | SSE event stream |
| `/api/v1/query` | Synchronous query: `POST {"query": "...", "limits": {...}}` |
| `/api/v1/query/stream` | Same, but SSE-streamed |
| `/api/v1/query/async` + `/api/v1/jobs/{id}` | Queued background jobs |
| `/api/v1/tools` | All MCP tools currently exposed |
| `/api/v1/health` | Liveness + loaded-model info |
| `/api/v1/limits` | Resolved per-query limits |
| `/api/v1/sessions` | Per-session state |
| `/api/v1/approvals` | Human-in-the-loop approval queue |
| `/mcp/sse` + `/mcp/message` | Legacy MCP SSE transport |
| `/mcp` | MCP Streamable HTTP transport |

### OTel metric instruments

Six instruments record the three hot paths. Two transports expose them:

- **OTLP push** when `-otel-endpoint` is set (15-second cadence)
- **Prometheus pull** at `/api/v1/metrics` when `-web` is set — scrape with any Prometheus-compatible tool or plain `curl`

Both readers can coexist.

| Instrument | Kind | Labels |
|---|---|---|
| `agent_queries_total` | counter | `termination_reason` |
| `agent_query_duration_seconds` | histogram | — |
| `llm_calls_total` | counter | `provider`, `status` |
| `llm_call_duration_seconds` | histogram | `provider` |
| `tool_calls_total` | counter | `tool`, `status` |
| `tool_call_duration_seconds` | histogram | `tool` |

When neither `-otel-endpoint` nor `-web` is set, the recorders exit early — zero cost in the unconfigured case.

### `scripts/agent-cli.sh` — curl wrapper for the HTTP surface

A bash CLI that wraps the REST API and metrics endpoints so you don't have to memorize paths:

```bash
scripts/agent-cli.sh health          # liveness + loaded model
scripts/agent-cli.sh tools           # list MCP tools
scripts/agent-cli.sh limits          # resolved per-query safety limits
scripts/agent-cli.sh sessions        # active sessions
scripts/agent-cli.sh query "is port 13305 in use?"   # synchronous query
scripts/agent-cli.sh stream "..."    # live SSE events for one query
scripts/agent-cli.sh events tool_call    # tail /events, filter to one type
scripts/agent-cli.sh metrics         # raw Prometheus exposition
scripts/agent-cli.sh metrics-summary # parsed human-readable counters + histogram sums
scripts/agent-cli.sh help            # full reference
```

Default target is `http://localhost:3131`; override with `AGENT_URL=…`. Requires `jq` for pretty-printing.

---

## 6. Four useful examples

All run against the default `agent.json` (Playwright + filesystem + fetch + ports). Run from `go-agent/`.

### 6.1 Web research (browser)

> Search the web for "Claude Sonnet 4.5 release notes" and summarize the three biggest changes in plain English.

```bash
./llm-agent -v "Search the web for 'Claude Sonnet 4.5 release notes' and summarize the three biggest changes in plain English."
```

Exercises `browser_navigate` → `browser_snapshot` → reasoning. Good for "is the agent loop terminating cleanly after a multi-step browse?".

### 6.2 URL fetch and extraction

> Fetch https://modelcontextprotocol.io and tell me, in two sentences, what MCP is and what problem it solves.

```bash
./llm-agent -v "Fetch https://modelcontextprotocol.io and tell me, in two sentences, what MCP is and what problem it solves."
```

Single `fetch` call, no browser. Cheapest way to verify the tool layer works end-to-end without spinning Playwright up.

### 6.3 Local file analysis (filesystem)

> Read `go-agent/PROMPT.md` and list the behaviors the system prompt enforces, one per line.

```bash
./llm-agent -v "Read go-agent/PROMPT.md and list the behaviors the system prompt enforces, one per line."
```

Stays inside the filesystem server's scoped roots (`.` and `~/Documents`). Useful smoke test for the filesystem MCP.

### 6.4 Multi-tool reasoning

> List the `.go` files in `go-agent/` sorted by size (largest first) and explain in one sentence what the top three do.

```bash
./llm-agent -v "List the .go files in go-agent/ sorted by size (largest first) and explain in one sentence what the top three do."
```

Forces the model to chain filesystem listing → multiple file reads → synthesis in a single loop. Best stress test for the round/budget limits: if you see "max rounds reached", lower the file count or raise `-max-rounds`.

---

## 7. Testing

The agent ships with a comprehensive Go test suite — 26 test files, ~190 tests across both modules — and a small set of management-script smoke tests. None of them touch a real LLM or network, so the full suite runs in a few seconds.

### Run everything

```bash
cd go-agent
make test
```

That executes:

```bash
go test ./... -count=1 -timeout 30s                    # agent: 24 test files
cd ../mcp-servers/ports && go test ./... -count=1 -timeout 30s   # ports MCP: 2 test files
```

### Run a single file or test

```bash
go test ./... -run TestSummarizeMessages -v            # one test by name
go test ./... -run TestHistorySize/empty -v            # one sub-test
go test -v -count=1 ./...                              # verbose, no cache
```

### What's covered

The full per-file table lives in [`go-agent/README.md` § Test Suite](../go-agent/README.md#test-suite). At a glance:

| Area | Tests | Files |
|---|---|---|
| LLM client + adapters (`LLMClient`, Gemini, Anthropic, tool-call parsing, validation) | ~40 | `llm_test`, `gemini_test`, `anthropic_test`, `toolparse_test`, `validate_test` |
| Agent loop & safety (lifecycle, sessions, parallel dispatch, limits, retry, safety heuristics, pure helpers) | ~60 | `agent_test`, `session_test`, `parallel_test`, `limits_test`, `retry_test`, `safety_test`, `util_test` |
| MCP layer (manager, schema round-trip, tool result formatting, env, server labels) | 7 | `mcp_test` |
| Web layer (REST API, async queue, rate limiter, MCP gateway, streamable HTTP, approvals, events) | ~40 | `web_test`, `queue_test`, `ratelimit_test`, `streamable_test`, `approval_test`, `events_test` |
| Observability (logger, redaction, **OTel metrics**, result envelope, config) | ~40 | `logger_test`, `redact_test`, `metrics_test`, `result_test`, `config_test` |
| Ports MCP server (stdio + SSE transports) | 16 | `ports/main_test`, `ports/sse_test` |

### Known gaps

These code paths still lack dedicated tests:

- `mcp.go::StartServers` and `MCPManager.CallTool` — require a live MCP subprocess; covered manually via `make mcp-test`
- `llm.go::ChatCompletion` / `chatStream` / `chatSync` — the actual OpenAI streaming path is exercised only at runtime (the Gemini and Anthropic adapter tests use mock servers)
- `llmstxt.go` — llms.txt detection/caching
- `otel.go::initTracer` — tracer initialization

The split `web_*.go` handlers (`web_query`, `web_admin`, `web_mcp`) are touched by `web_test.go` but not exhaustively.

The split `web_*.go` handlers (`web_query`, `web_admin`, `web_mcp`) are touched by `web_test.go` but not exhaustively. Contributions welcome.

### Smoke tests

Once the Lemonade Server is running, you can also exercise the live HTTP surface:

```bash
scripts/start-lemonade.sh test                         # ~6 health/chat probes against the server
make mcp-test                                          # ad-hoc REST + MCP gateway curl commands (prints instructions)
```

Neither of these is part of the Go test suite — they need a running backend.

---

## 8. Where to go next

**Blog series (long-form tutorial):**

- [Part 1: Introduction & Motivation](blog-part-1-introduction.md)
- [Part 2: Understanding MCP](blog-part-2-understanding-mcp.md)
- [Part 3: The Agent Loop](blog-part-3-agent-loop.md)
- [Part 4: Inside the Codebase](blog-part-4-code-walkthrough.md)
- [Part 5: Operations & Extending](blog-part-5-operations.md)
- [Part 6: Running It Yourself](blog-part-6-running-it-yourself.md) — narrative version of § 4 above with full per-backend recipes and trade-offs
- [Part 7: Observability](blog-part-7-observability.md) — narrative version of § 5 above with span/instrument tables, PromQL examples, and investigation walkthroughs

**Reference docs:**

- [`go-agent/README.md`](../go-agent/README.md) — configuration reference + full test-suite table
- [tool-migration.md](tool-migration.md) — feature-by-feature backend comparison
- [CHEATSHEET.md](CHEATSHEET.md) — quick API reference
