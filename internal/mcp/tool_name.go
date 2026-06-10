package mcp

import "strings"

const (
	toolNamePrefix    = "mcp__"
	toolNameSeparator = "__"
)

// ToolName returns the registry name for one MCP server tool.
func ToolName(server, tool string) string {
	return toolNamePrefix + server + toolNameSeparator + tool
}

// ParseToolName splits an MCP registry tool name into server and tool names.
func ParseToolName(name string) (server, tool string, ok bool) {
	if !strings.HasPrefix(name, toolNamePrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, toolNamePrefix)
	server, tool, ok = strings.Cut(rest, toolNameSeparator)
	if !ok || server == "" || tool == "" {
		return "", "", false
	}
	return server, tool, true
}
