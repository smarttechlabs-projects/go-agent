#!/usr/bin/env python3

# This file is part of the SmartTechLabs AI Workshop material.
# Contact: ai-lab@smarttechlabs.de — https://www.smarttechlabs.de
# SmartTechLabs is also available for AI projects and consulting.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# See the LICENSE file in the project root or
# http://www.apache.org/licenses/LICENSE-2.0 for the full text.

"""
agent_demo.py — Programmatic Lemonade + MCP agent (no CLI required)

Shows how to run the Tiny Agents loop from Python code,
useful for embedding in your own applications or workshop demos.

Usage:
    python agent_demo.py "What are the latest developments in WebXR?"

Links:
    Lemonade Server:  https://github.com/lemonade-sdk/lemonade
    Tiny Agents:      https://huggingface.co/blog/python-tiny-agents
    Playwright MCP:   https://github.com/playwright-community/mcp
    MCP Filesystem:   https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem
    MCP Fetch:        https://github.com/modelcontextprotocol/servers/tree/main/src/fetch
"""

import asyncio
import json
import sys
import os

from huggingface_hub import Agent


AGENT_CONFIG = {
    "model": "Qwen3-Coder-30B-A3B-Instruct-GGUF",
    "endpointUrl": "http://localhost:13305/api/",
    "servers": [
        {
            "type": "stdio",
            "config": {
                "command": "npx",
                "args": ["-y", "@playwright/mcp@latest", "--headless"],
            },
        },
        {
            "type": "stdio",
            "config": {
                "command": "npx",
                "args": [
                    "-y",
                    "@modelcontextprotocol/server-filesystem",
                    ".",
                ],
            },
        },
        {
            "type": "stdio",
            "config": {
                "command": "uvx",
                "args": ["mcp-server-fetch"],
            },
        },
    ],
}

SYSTEM_PROMPT = """You are a helpful research assistant with web search, \
filesystem, and URL fetch capabilities. When answering questions, \
search the web for current information, fetch relevant pages for \
detail, and provide well-sourced answers. Be concise."""


async def run_agent(query: str):
    """Run a single query through the agent and stream output."""

    agent = Agent(
        model=AGENT_CONFIG["model"],
        endpoint_url=AGENT_CONFIG["endpointUrl"],
        servers=AGENT_CONFIG["servers"],
        system_prompt=SYSTEM_PROMPT,
    )

    await agent.load_tools()

    print(f"\n{'='*60}")
    print(f"Query: {query}")
    print(f"Model: {AGENT_CONFIG['model']}")
    print(f"Tools: {len(agent.tools)} loaded")
    print(f"{'='*60}\n")

    # Stream the response
    async for chunk in agent.run(query):
        if hasattr(chunk, "choices") and chunk.choices:
            delta = chunk.choices[0].delta
            if hasattr(delta, "content") and delta.content:
                print(delta.content, end="", flush=True)
            if hasattr(delta, "tool_calls") and delta.tool_calls:
                for tc in delta.tool_calls:
                    if hasattr(tc, "function") and tc.function:
                        print(
                            f"\n  → Calling tool: {tc.function.name}",
                            flush=True,
                        )

    print("\n")


def main():
    if len(sys.argv) < 2:
        print("Usage: python agent_demo.py \"your question here\"")
        print()
        print("Examples:")
        print('  python agent_demo.py "What is the current status of WebGPU support?"')
        print('  python agent_demo.py "List the files in the current directory and summarize any markdown files"')
        print('  python agent_demo.py "Search for AMD ROCm 6.x release notes and save a summary to rocm-notes.md"')
        sys.exit(1)

    query = " ".join(sys.argv[1:])
    asyncio.run(run_agent(query))


if __name__ == "__main__":
    main()
