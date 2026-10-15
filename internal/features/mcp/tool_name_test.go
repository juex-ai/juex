package mcp

import (
	"context"
	"testing"
)

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

func TestToolNameRejectsAmbiguousParts(t *testing.T) {
	tests := []struct {
		name   string
		server string
		tool   string
	}{
		{
			name:   "empty server",
			server: "",
			tool:   "echo",
		},
		{
			name:   "server contains separator",
			server: "local__side",
			tool:   "echo",
		},
		{
			name:   "empty tool",
			server: "local",
			tool:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("ToolName(%q, %q) did not panic", tt.server, tt.tool)
				}
			}()
			_ = ToolName(tt.server, tt.tool)
		})
	}
}

func TestNewManagerLayeredSoftRecordsInvalidToolNameServer(t *testing.T) {
	mgr, err := NewManagerLayeredSoft(context.Background(), []Config{{
		MCPServers: map[string]ServerSpec{
			"bad__server": {Command: "unused"},
		},
	}}, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := mgr.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	}()

	errs := mgr.StartupErrors()
	if errs["bad__server"] == "" {
		t.Fatalf("startup errors = %+v, want bad__server error", errs)
	}
	if counts := mgr.ToolCounts(); len(counts) != 0 {
		t.Fatalf("tool counts = %+v, want none", counts)
	}
}
