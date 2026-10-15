package mcp

import (
	"fmt"
	"strings"
)

const (
	toolNamePrefix    = "mcp__"
	toolNameSeparator = "__"
)

// ToolName returns the registry name for one MCP server tool. It panics if the
// server/tool pair cannot be represented unambiguously by the registry scheme.
func ToolName(server, tool string) string {
	if err := validateToolNameParts(server, tool); err != nil {
		panic(err)
	}
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

func validateToolNameParts(server, tool string) error {
	if err := validateToolNameServer(server); err != nil {
		return err
	}
	if tool == "" {
		return fmt.Errorf("mcp: tool name cannot be empty")
	}
	return nil
}

func validateToolNameServer(server string) error {
	if server == "" {
		return fmt.Errorf("mcp: server name cannot be empty")
	}
	if strings.Contains(server, toolNameSeparator) {
		return fmt.Errorf("mcp: server name cannot contain %q", toolNameSeparator)
	}
	return nil
}
