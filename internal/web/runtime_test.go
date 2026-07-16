package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/tools"
)

func TestGetRuntimeStatus_ReturnsConfiguredMCPAndSkills(t *testing.T) {
	srv := newTestServer(t)
	work := srv.opts.Cfg.WorkDir
	srv.opts.Cfg.Hooks = hooks.Config{Commands: []hooks.CommandHook{{
		Name:    "guard",
		Events:  []hooks.EventName{hooks.EventPreToolUse},
		Command: []string{"python3", "guard.py"},
		Source:  "project",
	}}}
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
	if got.Tools.Count != 28 || len(got.Tools.Groups) != 8 {
		t.Fatalf("tools = %+v", got.Tools)
	}
	var observableToolNames []string
	for _, group := range got.Tools.Groups {
		if group.Group != string(tools.ToolGroupObservable) {
			continue
		}
		for _, tool := range group.Tools {
			observableToolNames = append(observableToolNames, tool.Name)
		}
	}
	if len(observableToolNames) != 7 || !slices.Contains(observableToolNames, "schedule_create") {
		t.Fatalf("observable tools = %v, want seven including schedule_create", observableToolNames)
	}
	mcpTools := got.MCP.Servers[0].Tools
	if len(mcpTools) != 1 || mcpTools[0].Name != "echo" || mcpTools[0].Description != "Echo input" {
		t.Fatalf("mcp tools = %+v", mcpTools)
	}
	if mcpTools[0].Schema["type"] != "object" || mcpTools[0].Timeout.Mode != "bounded" || mcpTools[0].Timeout.Seconds != tools.DefaultTimeoutSeconds {
		t.Fatalf("mcp echo metadata = %+v", mcpTools[0])
	}
	if got.Skills.Count != 1 || got.Skills.Items[0].Name != "review" {
		t.Fatalf("skills = %+v", got.Skills)
	}
	if got.Hooks.Configured != 1 || len(got.Hooks.Commands) != 1 || got.Hooks.Commands[0].Name != "guard" {
		t.Fatalf("hooks = %+v", got.Hooks)
	}
	if got.SystemPrompt.Count == 0 || len(got.SystemPrompt.Items) != got.SystemPrompt.Count {
		t.Fatalf("system prompt = %+v", got.SystemPrompt)
	}
	if got.Provider.ID != "openai" || got.Provider.Protocol != "openai/responses" || got.Provider.Model != "m" {
		t.Fatalf("provider = %+v", got.Provider)
	}
	if got.Sandbox.FileSystem.OutsideWorkspace != config.OutsideWorkspaceReadWrite || !got.Sandbox.Network.Enabled {
		t.Fatalf("sandbox = %+v", got.Sandbox)
	}
	if got.WorkDir != work {
		t.Fatalf("work_dir = %q, want %q", got.WorkDir, work)
	}
}

func TestRuntimeStatusResponseSerializesEmptyCatalogCollectionsAsArrays(t *testing.T) {
	response := runtimeStatusResponseFromApp(app.RuntimeStatus{
		Tools: app.RuntimeToolsStatus{},
		MCP: app.RuntimeMCPStatus{Servers: []app.RuntimeMCPServerStatus{{
			Name: "failed",
		}}},
	})
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, response)

	var got struct {
		Tools struct {
			Groups json.RawMessage `json:"groups"`
		} `json:"tools"`
		MCP struct {
			Servers []struct {
				Tools json.RawMessage `json:"tools"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Tools.Groups) != "[]" {
		t.Fatalf("empty groups JSON = %s, body=%s", got.Tools.Groups, recorder.Body.Bytes())
	}
	if len(got.MCP.Servers) != 1 {
		t.Fatalf("MCP servers JSON = %+v, body=%s", got.MCP.Servers, recorder.Body.Bytes())
	}
	if string(got.MCP.Servers[0].Tools) != "[]" {
		t.Fatalf("empty MCP tools JSON = %s, body=%s", got.MCP.Servers[0].Tools, recorder.Body.Bytes())
	}
}

func TestRuntimeStatusOmitsActiveSessionState(t *testing.T) {
	srv := newTestServer(t)
	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := as.app.Engine.GoalState.Create("ship runtime goal status", "waiting on e2e"); err != nil {
		t.Fatal(err)
	}
	if _, err := as.app.Engine.Notes.Update("- [ ] show runtime state in the UI"); err != nil {
		t.Fatal(err)
	}

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["goal"]; ok {
		t.Fatalf("runtime status leaked session goal: %s", encoded)
	}
	if _, ok := fields["notes"]; ok {
		t.Fatalf("runtime status leaked session notes: %s", encoded)
	}
}

func TestRuntimeStatusIncludesActiveSessionScratchpadPrompt(t *testing.T) {
	srv := newTestServer(t)
	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got.SystemPrompt.Items {
		if item.Key == "session_scratchpad" {
			wantPath := as.app.Session.ScratchpadDir()
			if item.Path != wantPath || !strings.Contains(item.Text, wantPath) {
				t.Fatalf("scratchpad prompt = %+v, want path %q", item, wantPath)
			}
			return
		}
	}
	t.Fatalf("runtime system prompt missing session scratchpad: %+v", got.SystemPrompt.Items)
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

func TestRuntimeStatusIncludesSandboxPolicy(t *testing.T) {
	srv := newTestServer(t)
	srv.opts.Cfg.Sandbox = config.SandboxPolicy{
		Enabled: true,
		FileSystem: config.FileSystemSandboxPolicy{
			OutsideWorkspace: config.OutsideWorkspaceReadOnly,
		},
		Network: config.NetworkSandboxPolicy{Enabled: false},
	}

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Sandbox.Enabled || got.Sandbox.FileSystem.OutsideWorkspace != config.OutsideWorkspaceReadOnly || got.Sandbox.Network.Enabled {
		t.Fatalf("sandbox = %+v", got.Sandbox)
	}
}

func TestRuntimeStatusIgnoresActiveSessionRegistryForMCPCatalog(t *testing.T) {
	srv := newTestServer(t)
	mustWriteRuntimeFile(t, filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "mcp.json"), `{
  "mcpServers": {
	"alpha": { "command": "" }
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
	if got.MCP.Configured != 1 || got.MCP.Connected != 0 || got.MCP.Errors != 1 {
		t.Fatalf("mcp = %+v", got.MCP)
	}
	if got.MCP.Servers[0].Name != "alpha" || got.MCP.Servers[0].Connected || got.MCP.Servers[0].ToolCount != 0 || len(got.MCP.Servers[0].Tools) != 0 || got.MCP.Servers[0].Status != "error" {
		t.Fatalf("alpha = %+v", got.MCP.Servers[0])
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

func TestRuntimeStatusIncludesExtensionSources(t *testing.T) {
	srv := newTestServer(t)
	work := srv.opts.Cfg.WorkDir
	extDir := filepath.Join(work, ".juex", "extensions", "demo")
	mustWriteRuntimeFile(t, filepath.Join(extDir, "mcp.json"), `{
  "mcpServers": {
    "extsrv": {
      "command": "${JUEX_EXT_DIR}/bin/server",
      "args": ["--ext", "$JUEX_EXT_DIR"]
    }
  }
}`)
	mustWriteRuntimeFile(t, filepath.Join(extDir, "skills", "ext-skill", "SKILL.md"), `---
name: ext-skill
description: extension skill
---
body`)
	mustWriteRuntimeFile(t, filepath.Join(extDir, "hooks.yaml"), `trusted: true
commands:
  - name: ext-stop
    events: [Stop]
    command: ["python3", "hooks/check.py"]
`)

	got, err := srv.runtimeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCP.Servers) != 1 || got.MCP.Servers[0].Name != "extsrv" || got.MCP.Servers[0].Source != "ext:demo" {
		t.Fatalf("mcp servers = %+v", got.MCP.Servers)
	}
	if filepath.ToSlash(got.MCP.Servers[0].Command) != filepath.ToSlash(filepath.Join(extDir, "bin", "server")) {
		t.Fatalf("mcp command = %q", got.MCP.Servers[0].Command)
	}
	if len(got.MCP.Servers[0].Args) != 2 || got.MCP.Servers[0].Args[1] != extDir {
		t.Fatalf("mcp args = %+v", got.MCP.Servers[0].Args)
	}
	if len(got.Skills.Items) != 1 || got.Skills.Items[0].Name != "ext-skill" || got.Skills.Items[0].Source != "ext:demo" {
		t.Fatalf("skills = %+v", got.Skills.Items)
	}
	if got.Hooks.Configured != 1 || got.Hooks.Commands[0].Name != "ext-stop" || got.Hooks.Commands[0].Source != "ext:demo" {
		t.Fatalf("hooks = %+v", got.Hooks)
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
	if len(got.MCP.Servers[1].Tools) != 0 {
		t.Fatalf("failed beta tools = %+v", got.MCP.Servers[1].Tools)
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
