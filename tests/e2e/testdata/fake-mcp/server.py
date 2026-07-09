"""
Standalone fake MCP server for tests/e2e to verify that the live `juex`
binary can load tools from a real subprocess MCP server.

Uses the official `mcp` Python SDK (most real-world MCP servers are
written in Python). Run through the repo uv environment:

    uv run --project . python tests/e2e/testdata/fake-mcp/server.py

Exposes one tool, `echo`, that mirrors the input string back as text.
"""
import sys

from mcp.server.fastmcp import FastMCP

mcp = FastMCP("fake-mcp")
print("JUEX-FAKE-MCP-STDERR marker", file=sys.stderr)


@mcp.tool()
def echo(text: str) -> str:
    """Echo the supplied text back to the caller."""
    return f"echoed: {text}"


if __name__ == "__main__":
    mcp.run()  # defaults to stdio transport
