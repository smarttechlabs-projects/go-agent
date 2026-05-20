# docs/

Project documentation and reference configuration. The main project README lives in the [repository root](../README.md); the Go agent's deep dive lives in [go-agent/README.md](../go-agent/README.md).

## Files

| File | Description |
|---|---|
| `agent.json` | Declarative agent config for Linux/macOS — MCP servers, model name, endpoint URL. Used by the [Hugging Face Tiny Agents](https://huggingface.co/blog/python-tiny-agents) prototyping alternative. |
| `agent-windows.json` | Windows variant of `agent.json` with full `npx.cmd` paths. |
| `PROMPT.md` | Default system prompt loaded by the agent. |
| `agent-setup.md` | Standalone setup walkthrough — build, configure endpoints, run worked examples. |
| `CHEATSHEET.md` | Quick reference for the Lemonade Server API and useful agent prompts. |
| `tool-migration.md` | Per-backend migration recipes (LM Studio, Ollama, vLLM, …). |
