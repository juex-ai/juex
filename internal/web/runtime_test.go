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
	"github.com/juex-ai/juex/internal/config"
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
	if got.SystemPrompt.Count == 0 || len(got.SystemPrompt.Items) != got.SystemPrompt.Count {
		t.Fatalf("system prompt = %+v", got.SystemPrompt)
	}
	if got.Provider.ID != "openai" || got.Provider.Protocol != "openai/responses" || got.Provider.Model != "m" {
		t.Fatalf("provider = %+v", got.Provider)
	}
	if got.WorkDir != work {
		t.Fatalf("work_dir = %q, want %q", got.WorkDir, work)
	}
}

func TestRuntimeStatusIncludesActiveGoal(t *testing.T) {
	srv := newTestServer(t)
	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	if err := as.app.Engine.GoalState.BeginTurn("ship runtime goal status"); err != nil {
		t.Fatal(err)
	}
	if err := as.app.Engine.GoalState.ApplyPatch(runtime.GoalStatePatch{
		Status:       runtime.GoalStatusContinue,
		LastProgress: "waiting on e2e",
		CompletionCheck: &runtime.CompletionCheck{
			Status:  runtime.GoalStatusContinue,
			Summary: "e2e still missing",
		},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if got.Goal == nil || got.Goal.Objective != "ship runtime goal status" || got.Goal.LastCheck == nil {
		t.Fatalf("goal = %+v", got.Goal)
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

func TestRuntimeStatusReportsAbsoluteWorkDir(t *testing.T) {
	srv := newTestServer(t)
	parent := t.TempDir()
	workName := "workspace"
	work := filepath.Join(parent, workName)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)
	srv.opts.Cfg.WorkDir = workName

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkDir != work {
		t.Fatalf("work_dir = %q, want %q", got.WorkDir, work)
	}
}

func TestRuntimeStatusIncludesShellProfile(t *testing.T) {
	srv := newTestServer(t)
	srv.opts.Cfg.Shell = config.ShellProfile{
		Profile:   "powershell",
		Family:    "powershell",
		Binary:    "pwsh",
		Args:      []string{"-NoProfile", "-Command"},
		PathStyle: "windows",
		Source:    "test",
		RuntimeOS: "windows",
	}

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if got.Shell.Profile != "powershell" || got.Shell.Family != "powershell" || got.Shell.PathStyle != "windows" {
		t.Fatalf("shell = %+v", got.Shell)
	}
	foundPromptShell := false
	for _, item := range got.SystemPrompt.Items {
		if item.Key == "operating_context" && strings.Contains(item.Text, "- shell: powershell (pwsh)") {
			foundPromptShell = true
		}
	}
	if !foundPromptShell {
		t.Fatalf("system prompt did not include shell profile: %+v", got.SystemPrompt.Items)
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

func TestRuntimeStatusExpandsMCPWorkDirVariables(t *testing.T) {
	srv := newTestServer(t)
	work := srv.opts.Cfg.WorkDir
	mustWriteRuntimeFile(t, filepath.Join(work, ".agents", "mcp.json"), `{
  "mcpServers": {
    "alpha": {
      "command": "${WORKDIR}/bin/server",
      "args": ["--workdir", "${WORKDIR}", "--juex-workdir", "$JUEX_WORKDIR"]
    }
  }
}`)

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCP.Servers) != 1 {
		t.Fatalf("servers = %+v", got.MCP.Servers)
	}
	server := got.MCP.Servers[0]
	if server.Command != work+"/bin/server" {
		t.Fatalf("command = %q", server.Command)
	}
	wantArgs := []string{"--workdir", work, "--juex-workdir", work}
	if strings.Join(server.Args, "\n") != strings.Join(wantArgs, "\n") {
		t.Fatalf("args = %#v, want %#v", server.Args, wantArgs)
	}
}

func TestRuntimeStatusIncludesSystemPromptEntries(t *testing.T) {
	srv := newTestServer(t)
	work := srv.opts.Cfg.WorkDir
	homeAgents := t.TempDir()
	srv.opts.Cfg.HomeAgentsDir = homeAgents
	srv.opts.Cfg.EnableUserGlobalResources = true
	mustWriteRuntimeFile(t, filepath.Join(homeAgents, "AGENTS.md"), "global runtime rule")
	mustWriteRuntimeFile(t, filepath.Join(work, "AGENTS.md"), "workspace root rule")
	mustWriteRuntimeFile(t, filepath.Join(work, ".agents", "AGENTS.md"), "workspace agents rule")

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if got.SystemPrompt.Count != 4 {
		t.Fatalf("system prompt = %+v", got.SystemPrompt)
	}
	want := []struct {
		label  string
		source string
		path   string
		text   string
	}{
		{label: "Global AGENTS.md", source: "user", path: filepath.Join(homeAgents, "AGENTS.md"), text: "global runtime rule"},
		{label: "Workspace AGENTS.md", source: "project", path: filepath.Join(work, "AGENTS.md"), text: "workspace root rule"},
		{label: ".agents/AGENTS.md", source: "project", path: filepath.Join(work, ".agents", "AGENTS.md"), text: "workspace agents rule"},
		{label: "Operating Context", source: "runtime", path: "", text: "Operating Context"},
	}
	for i, w := range want {
		gotEntry := got.SystemPrompt.Items[i]
		if gotEntry.Label != w.label || gotEntry.Source != w.source || gotEntry.Path != w.path || !strings.Contains(gotEntry.Text, w.text) || gotEntry.Tokens <= 0 {
			t.Fatalf("entry[%d] = %+v, want label=%q source=%q path=%q text containing %q with tokens", i, gotEntry, w.label, w.source, w.path, w.text)
		}
	}
}

func TestRuntimeStatusOrdersProjectBeforeUserSources(t *testing.T) {
	srv := newTestServer(t)
	homeAgents := t.TempDir()
	srv.opts.Cfg.HomeAgentsDir = homeAgents
	srv.opts.Cfg.EnableUserGlobalResources = true
	mustWriteRuntimeFile(t, filepath.Join(homeAgents, "mcp.json"), `{
  "mcpServers": {
    "shared": { "command": "user-shared" },
    "zeta": { "command": "user-zeta" }
  }
}`)
	mustWriteRuntimeFile(t, filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "mcp.json"), `{
  "mcpServers": {
    "alpha": { "command": "project-alpha" },
    "shared": { "command": "project-shared" }
  }
}`)
	mustWriteRuntimeFile(t, filepath.Join(homeAgents, "skills", "zeta", "SKILL.md"), `---
name: zeta
description: user zeta
---
body`)
	mustWriteRuntimeFile(t, filepath.Join(homeAgents, "skills", "shared", "SKILL.md"), `---
name: shared
description: user shared
---
body`)
	mustWriteRuntimeFile(t, filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "skills", "alpha", "SKILL.md"), `---
name: alpha
description: project alpha
---
body`)
	mustWriteRuntimeFile(t, filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "skills", "shared", "SKILL.md"), `---
name: shared
description: project shared
---
body`)

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	wantServers := []struct {
		name    string
		source  string
		command string
	}{
		{name: "alpha", source: "project", command: "project-alpha"},
		{name: "shared", source: "project", command: "project-shared"},
		{name: "zeta", source: "user", command: "user-zeta"},
	}
	if len(got.MCP.Servers) != len(wantServers) {
		t.Fatalf("servers = %+v", got.MCP.Servers)
	}
	for i, want := range wantServers {
		gotServer := got.MCP.Servers[i]
		if gotServer.Name != want.name || gotServer.Source != want.source || gotServer.Command != want.command {
			t.Fatalf("server[%d] = %+v, want %+v", i, gotServer, want)
		}
	}
	wantSkills := []struct {
		name        string
		source      string
		description string
	}{
		{name: "alpha", source: "project", description: "project alpha"},
		{name: "shared", source: "project", description: "project shared"},
		{name: "zeta", source: "user", description: "user zeta"},
	}
	if len(got.Skills.Items) != len(wantSkills) {
		t.Fatalf("skills = %+v", got.Skills.Items)
	}
	for i, want := range wantSkills {
		gotSkill := got.Skills.Items[i]
		if gotSkill.Name != want.name || gotSkill.Source != want.source || gotSkill.Description != want.description {
			t.Fatalf("skill[%d] = %+v, want %+v", i, gotSkill, want)
		}
	}
}

func TestRuntimeStatusSkipsUserGlobalResourcesWhenDisabled(t *testing.T) {
	srv := newTestServer(t)
	homeAgents := t.TempDir()
	srv.opts.Cfg.HomeAgentsDir = homeAgents
	srv.opts.Cfg.EnableUserGlobalResources = false
	work := srv.opts.Cfg.WorkDir

	mustWriteRuntimeFile(t, filepath.Join(homeAgents, "AGENTS.md"), "global runtime rule")
	mustWriteRuntimeFile(t, filepath.Join(homeAgents, "mcp.json"), `{
  "mcpServers": {
    "user": { "command": "user-command" }
  }
}`)
	mustWriteRuntimeFile(t, filepath.Join(homeAgents, "skills", "user", "SKILL.md"), `---
name: user
description: user skill
---
body`)
	mustWriteRuntimeFile(t, filepath.Join(work, ".agents", "AGENTS.md"), "workspace agents rule")
	mustWriteRuntimeFile(t, filepath.Join(work, ".agents", "mcp.json"), `{
  "mcpServers": {
    "project": { "command": "project-command" }
  }
}`)
	mustWriteRuntimeFile(t, filepath.Join(work, ".agents", "skills", "project", "SKILL.md"), `---
name: project
description: project skill
---
body`)

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SystemPrompt.Items) != 3 {
		t.Fatalf("system prompt = %+v", got.SystemPrompt)
	}
	if got.SystemPrompt.Items[0].Label != ".agents/AGENTS.md" || strings.Contains(got.SystemPrompt.Items[0].Text, "global runtime rule") {
		t.Fatalf("system prompt should skip global AGENTS.md and keep project entry: %+v", got.SystemPrompt.Items)
	}
	for _, item := range got.SystemPrompt.Items {
		if strings.Contains(item.Text, "global runtime rule") || strings.Contains(item.Text, "user skill") {
			t.Fatalf("system prompt includes disabled user-global resource: %+v", item)
		}
	}
	if len(got.MCP.Servers) != 1 || got.MCP.Servers[0].Name != "project" || got.MCP.Servers[0].Source != "project" {
		t.Fatalf("servers = %+v", got.MCP.Servers)
	}
	if len(got.Skills.Items) != 1 || got.Skills.Items[0].Name != "project" || got.Skills.Items[0].Source != "project" {
		t.Fatalf("skills = %+v", got.Skills.Items)
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

	if _, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary); err != nil {
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
