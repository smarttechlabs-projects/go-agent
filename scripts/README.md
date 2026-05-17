# Scripts

Utility scripts for managing the Lemonade Server and running quick demos. All scripts are designed to be run from the 
repository root.

## start-lemonade.sh

Full lifecycle manager for [Lemonade Server](https://github.com/lemonade-sdk/lemonade). Handles installation, startup (with 32K context by default), model 
downloading, loading, and health checks.

```bash
scripts/start-lemonade.sh start              # start server + load default model
scripts/start-lemonade.sh start Qwen3-8B-GGUF  # start with a specific model
scripts/start-lemonade.sh stop               # graceful shutdown
scripts/start-lemonade.sh status             # health + loaded model
scripts/start-lemonade.sh list               # available / downloaded models
scripts/start-lemonade.sh pull <model>       # download model (cached in ~/.cache/huggingface/)
scripts/start-lemonade.sh load <model>       # load / switch model (instant if cached)
scripts/start-lemonade.sh config ctx-size 32768  # change context window (restarts server)
scripts/start-lemonade.sh restart            # stop + start
scripts/start-lemonade.sh test               # smoke tests
```

**Environment variables:**

| Variable | Default | Description |
|---|---|---|
| `LEMONADE_MODEL` | `Qwen3-Coder-30B-A3B-Instruct-GGUF` | Default model name |
| `LEMONADE_URL` | `http://localhost:13305` | Server URL |
| `LEMONADE_CTX_SIZE` | `32768` | Context window size |
| `LEMONADE_LLAMACPP` | server default | Backend: `vulkan`, `rocm`, `cpu` |
| `LEMONADE_PORT` | `13305` | Server port |

## agent-cli.sh

Thin curl wrapper for a running Go agent (must be started with `-web <addr>`). Targets the REST API, SSE event stream, and the `/api/v1/metrics` Prometheus endpoint without you needing to remember any paths.

```bash
scripts/agent-cli.sh health                    # liveness + loaded model
scripts/agent-cli.sh tools                     # list MCP tools
scripts/agent-cli.sh limits                    # resolved per-query safety limits
scripts/agent-cli.sh sessions                  # active sessions
scripts/agent-cli.sh approvals                 # pending HITL approvals
scripts/agent-cli.sh query "your question"     # synchronous query, print full response
scripts/agent-cli.sh stream "your question"    # query with live SSE event stream
scripts/agent-cli.sh events                    # tail every event
scripts/agent-cli.sh events tool_call          # tail filtered to one event type
scripts/agent-cli.sh metrics                   # raw Prometheus exposition
scripts/agent-cli.sh metrics-summary           # parsed human-readable counters + histograms
scripts/agent-cli.sh help                      # full reference
```

**Environment variables:**

| Variable | Default | Description |
|---|---|---|
| `AGENT_URL` | `http://localhost:3131` | Base URL of the running agent |

Requires `jq` for pretty-printing on most subcommands.

## shutdown.sh

Companion to `start-lemonade.sh`: gracefully stops the agent, the Lemonade Server, or both. Sends `SIGTERM` and waits before escalating to `SIGKILL`, and cleans up orphan MCP children (Playwright headless Chrome in particular) that can outlive a hard kill of the parent agent.

```bash
scripts/shutdown.sh                    # stop both (agent first, then Lemonade) — default
scripts/shutdown.sh all                # same as default, explicit
scripts/shutdown.sh agent              # just the llm-agent + orphan MCP children
scripts/shutdown.sh lemonade           # just the Lemonade Server (also catches stray lemond daemons)
scripts/shutdown.sh status             # report what's running (agent / Lemonade / MCP / orphans)
scripts/shutdown.sh help               # full reference
```

**Order matters for `all`:** the script stops the agent *first*, then Lemonade. This gives any in-flight LLM call a clean end-of-stream rather than a TCP RST when the backend goes away.

**Why orphan cleanup is needed:** in normal operation the agent's MCP children (Playwright, filesystem, fetch, ports) terminate when the agent's `MCPManager.Close()` shuts their stdio. If the agent was force-killed earlier — kernel OOM, `kill -9`, system crash — Playwright's headless Chrome processes can survive. The `agent` and `all` subcommands run a final pass to catch those.

**Why a separate path for `lemond`:** the `lemonade-server serve` wrapper spawns an actual long-running `lemond` daemon. If the wrapper is Ctrl+C'd instead of being stopped via `lemonade-server stop`, the `lemond` child can outlive it and hold port 13305. Both `shutdown.sh lemonade` and the updated `start-lemonade.sh stop` look for and kill stray `lemond` processes as the force-kill fallback.

**Environment variables:**

| Variable | Default | Description |
|---|---|---|
| `AGENT_NAME` | `llm-agent` | Process name to match for the agent |
| `LEMONADE_URL` | `http://localhost:13305` | Base URL for the HTTP liveness check |
| `SHUTDOWN_GRACE_SECS` | `10` | Seconds to wait for graceful exit before SIGKILL |
| `ORPHAN_CLEANUP` | `1` | Set to `0` to skip the Playwright Chrome cleanup pass |

This script only touches processes owned by your own UID; it will not kill a Lemonade or agent owned by another user.

## validate-setup.sh

Pre-flight checker that verifies all prerequisites (Python, Node.js, uv, Lemonade Server) are installed and at the required versions. Run this first if 
something isn't working.

```bash
scripts/validate-setup.sh
```

## agent_demo.py

Programmatic Python example showing how to run the [Tiny Agents](https://huggingface.co/blog/python-tiny-agents) loop from code (no CLI required). Useful for embedding the agent in your own applications.

```bash
# Requires: pip install "huggingface_hub[mcp]>=0.33.2"
python scripts/agent_demo.py "What are the latest developments in WebXR?"
```
