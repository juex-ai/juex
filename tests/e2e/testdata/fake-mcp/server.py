#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.10"
# dependencies = ["mcp>=1.2"]
# ///
"""
Standalone fake MCP server for tests/e2e to verify that the live `juex`
binary can load tools from a real subprocess MCP server.

Uses the official `mcp` Python SDK (most real-world MCP servers are
written in Python). Run via:

    uv run tests/e2e/testdata/fake-mcp/server.py

The PEP 723 header above lets `uv` resolve and cache the `mcp` dep into
an ephemeral venv on first invocation; subsequent calls reuse the cache.

Exposes one tool, `echo`, that mirrors the input string back as text.
"""
from mcp.server.fastmcp import FastMCP

mcp = FastMCP("fake-mcp")


@mcp.tool()
def echo(text: str) -> str:
    """Echo the supplied text back to the caller."""
    return f"echoed: {text}"


if __name__ == "__main__":
    mcp.run()  # defaults to stdio transport
