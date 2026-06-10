package mcp

import "testing"

func TestToolNameRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		server string
		tool   string
		want   string
	}{
		{
			name:   "simple",
			server: "local",
			tool:   "echo",
			want:   "mcp__local__echo",
		},
		{
			name:   "tool contains separator",
			server: "local",
			tool:   "tool__with__separator",
			want:   "mcp__local__tool__with__separator",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToolName(tt.server, tt.tool)
			if got != tt.want {
				t.Fatalf("ToolName() = %q, want %q", got, tt.want)
			}
			server, tool, ok := ParseToolName(got)
			if !ok {
				t.Fatalf("ParseToolName(%q) returned !ok", got)
			}
			if server != tt.server || tool != tt.tool {
				t.Fatalf("ParseToolName(%q) = (%q, %q), want (%q, %q)", got, server, tool, tt.server, tt.tool)
			}
		})
	}
}

func TestParseToolNameMalformed(t *testing.T) {
	tests := []string{
		"read",
		"mcp__local",
		"mcp____echo",
		"mcp__local__",
		"mcp__",
		"",
	}

	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			server, tool, ok := ParseToolName(name)
			if ok {
				t.Fatalf("ParseToolName(%q) = (%q, %q, true), want !ok", name, server, tool)
			}
		})
	}
}
