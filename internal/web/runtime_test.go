package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/tools"
)

func TestGetRuntimeStatus_ReturnsConfiguredMCPAndSkills(t *testing.T) {
	srv := newTestServer(t)
	work := srv.opts.Cfg.WorkDir
	mustWriteWebFakeMCPConfig(t, work, false)
	mustWriteRuntimeFile(t, filepath.Join(work, ".agents", "skills", "review", "SKILL.md"), `---
name: review
description: Review code changes
type: model-invocable
---
body`)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got runtimeStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.MCP.Configured != 1 || got.MCP.Connected != 1 {
		t.Fatalf("mcp = %+v", got.MCP)
	}
	if len(got.MCP.Servers) != 1 || got.MCP.Servers[0].Name != "alpha" || got.MCP.Servers[0].Command != os.Args[0] || got.MCP.Servers[0].Status != "connected" || got.MCP.Servers[0].ToolCount != 1 {
		t.Fatalf("servers = %+v", got.MCP.Servers)
	}
	if got.Skills.Count != 1 || got.Skills.Items[0].Name != "review" {
		t.Fatalf("skills = %+v", got.Skills)
	}
	if got.Provider.ID != "openai" || got.Provider.Protocol != "openai/responses" || got.Provider.Model != "m" {
		t.Fatalf("provider = %+v", got.Provider)
	}
}

func TestGetRuntimeStatus_IgnoresMissingMCPConfig(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got runtimeStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.MCP.Configured != 0 || got.MCP.Connected != 0 {
		t.Fatalf("mcp = %+v", got.MCP)
	}
}

func TestRuntimeStatus_CountsConnectedMCPServersFromActiveTools(t *testing.T) {
	srv := newTestServer(t)
	mustWriteRuntimeFile(t, filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "mcp.json"), `{
  "mcpServers": {
    "alpha": { "command": "alpha-cmd" },
    "beta": { "command": "beta-cmd" }
  }
}`)
	reg := tools.NewRegistry()
	for _, name := range []string{"mcp__alpha__one", "mcp__alpha__two", "mcp__gamma__orphan"} {
		n := name
		if err := reg.Register(tools.Tool{
			Name:    n,
			Schema:  map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (string, error) { return "", nil },
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv.sessions.Store("active", &activeSession{
		app:   &app.App{Engine: &runtime.Engine{Tools: reg}},
		bcast: newBroadcaster(),
	})
	srv.sessions.Store("second", &activeSession{
		app:   &app.App{Engine: &runtime.Engine{Tools: reg}},
		bcast: newBroadcaster(),
	})

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if got.MCP.Configured != 2 || got.MCP.Connected != 1 {
		t.Fatalf("mcp = %+v", got.MCP)
	}
	if got.MCP.Servers[0].Name != "alpha" || !got.MCP.Servers[0].Connected || got.MCP.Servers[0].ToolCount != 2 {
		t.Fatalf("alpha = %+v", got.MCP.Servers[0])
	}
	if got.MCP.Servers[1].Name != "beta" || got.MCP.Servers[1].Connected {
		t.Fatalf("beta = %+v", got.MCP.Servers[1])
	}
}

func TestRuntimeStatusReportsMCPConnectionError(t *testing.T) {
	srv := newTestServer(t)
	mustWriteRuntimeFile(t, filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "mcp.json"), `{
  "mcpServers": {
    "alpha": { "command": "alpha-cmd" }
  }
}`)
	srv.recordMCPError(&mcp.ServerError{Server: "alpha", Op: "connect", Err: errors.New("invalid stdout")})

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if got.MCP.Configured != 1 || got.MCP.Connected != 0 {
		t.Fatalf("mcp = %+v", got.MCP)
	}
	if got.MCP.Servers[0].Status != "error" {
		t.Fatalf("status = %q, want error", got.MCP.Servers[0].Status)
	}
	if got.MCP.Servers[0].Error == "" || got.MCP.Servers[0].Connected {
		t.Fatalf("server = %+v", got.MCP.Servers[0])
	}
}

func TestRuntimeStatusReportsPartialMCPStartup(t *testing.T) {
	srv := newTestServer(t)
	body, err := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"alpha": map[string]any{
				"command": os.Args[0],
				"env":     map[string]string{"JUEX_WEB_FAKE_MCP": "1"},
			},
			"beta": map[string]any{
				"command": "",
			},
		},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mustWriteRuntimeFile(t, filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "mcp.json"), string(body))

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if got.MCP.Configured != 2 || got.MCP.Connected != 1 || got.MCP.Errors != 1 {
		t.Fatalf("mcp = %+v", got.MCP)
	}
	if got.MCP.Servers[0].Name != "alpha" || got.MCP.Servers[0].Status != "connected" || got.MCP.Servers[0].ToolCount != 1 {
		t.Fatalf("alpha = %+v", got.MCP.Servers[0])
	}
	if got.MCP.Servers[1].Name != "beta" || got.MCP.Servers[1].Status != "error" || !strings.Contains(got.MCP.Servers[1].Error, "missing command") {
		t.Fatalf("beta = %+v", got.MCP.Servers[1])
	}
}

func TestOpenSessionKeepsServeUsableWhenMCPStartupFails(t *testing.T) {
	srv := newTestServer(t)
	mustWriteRuntimeFile(t, filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "mcp.json"), `{
  "mcpServers": {
    "alpha": { "command": "" }
  }
}`)
	srv.recordMCPError(&mcp.ServerError{Server: "alpha", Op: "connect", Err: errors.New("old failure")})

	if _, err := srv.openSession(context.Background(), ""); err != nil {
		t.Fatalf("openSession returned error: %v", err)
	}
	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCP.Servers) != 1 {
		t.Fatalf("servers = %+v", got.MCP.Servers)
	}
	if strings.Contains(got.MCP.Servers[0].Error, "old failure") {
		t.Fatalf("stale error was not cleared: %+v", got.MCP.Servers[0])
	}
	if got.MCP.Servers[0].Status != "error" || !strings.Contains(got.MCP.Servers[0].Error, "missing command") {
		t.Fatalf("server = %+v", got.MCP.Servers[0])
	}
}

func mustWriteRuntimeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
