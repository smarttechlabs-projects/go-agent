# docs/

This directory contains the Python agent setup (Tiny Agents), server management scripts, and project documentation.

For the main project documentation, see the [root README](../README.md).

For the Go agent (recommended), see [go-agent/README.md](../go-agent/README.md).

## Files

| File | Description |
|---|---|
| `agent.json` | Agent config for Linux/macOS (Playwright + filesystem + fetch) |
| `agent-windows.json` | Windows variant with full `npx.cmd` paths |
| `PROMPT.md` | System prompt for the agent |
| `agent_demo.py` | Programmatic Python example using `huggingface_hub.Agent` |
| `start-lemonade.sh` | Lemonade Server management (start/stop/config/pull/load/test) |
| `validate-setup.sh` | Pre-flight dependency checker |
| `CHEATSHEET.md` | Quick reference for API calls, CLI commands, and REST API |
| `tool-migration.md` | Migration guide for LM Studio, Ollama, vLLM backends |
| `blog-post.md` | Substack article: building a local AI agent with MCP |
