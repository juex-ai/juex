package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/tools"
)

func TestRuntimeStatusServiceProjectsBuiltinToolCatalog(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{WorkDir: work, ToolTimeout: 1500 * time.Millisecond}
	status, err := NewRuntimeStatusService(cfg).Snapshot(RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}

	wantGroups := []tools.ToolGroup{
		tools.ToolGroupFile,
		tools.ToolGroupChunkedWrite,
		tools.ToolGroupShell,
		tools.ToolGroupSearch,
		tools.ToolGroupSkill,
		tools.ToolGroupMemory,
		tools.ToolGroupSessionState,
		tools.ToolGroupObservable,
	}
	if len(status.Tools.Groups) != len(wantGroups) {
		t.Fatalf("tool groups = %#v, want %v", status.Tools.Groups, wantGroups)
	}
	count := 0
	for i, wantGroup := range wantGroups {
		group := status.Tools.Groups[i]
		if group.Group != string(wantGroup) {
			t.Fatalf("group[%d] = %q, want %q", i, group.Group, wantGroup)
		}
		names := make([]string, 0, len(group.Tools))
		for _, tool := range group.Tools {
			names = append(names, tool.Name)
		}
		if !sort.StringsAreSorted(names) {
			t.Fatalf("group %q tools are not sorted: %v", group.Group, names)
		}
		count += len(group.Tools)
	}
	if status.Tools.Count != count || count != 27 {
		t.Fatalf("tool count = %d, grouped=%d, want 27", status.Tools.Count, count)
	}
}

func TestRuntimeStatusServiceCatalogMatchesRealAppRegistry(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{WorkDir: work, ToolTimeout: 1500 * time.Millisecond}
	a, err := New(Options{Config: cfg, Provider: &stubProvider{}, WorkDir: work, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	status, err := NewRuntimeStatusService(cfg).Snapshot(RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	type catalogEntry struct {
		group string
		info  RuntimeToolInfo
	}
	catalog := make(map[string]catalogEntry, status.Tools.Count)
	for _, group := range status.Tools.Groups {
		for _, info := range group.Tools {
			catalog[info.Name] = catalogEntry{group: group.Group, info: info}
		}
	}

	actualCount := 0
	for _, actual := range a.Engine.Tools.List() {
		if actual.Group == tools.ToolGroupMCP {
			continue
		}
		actualCount++
		entry, ok := catalog[actual.Name]
		if !ok {
			t.Errorf("registered tool %q missing from runtime catalog", actual.Name)
			continue
		}
		info := entry.info
		definition := actual.Definition()
		if info.Description != definition.Description || entry.group != string(definition.Group) || !reflect.DeepEqual(info.Schema, definition.Schema) {
			t.Errorf("catalog %q = %#v, registered definition = %#v", actual.Name, info, definition)
		}
		effective := tools.EffectiveToolTimeout(definition, durationSeconds(cfg.RuntimeLimits().ToolTimeout))
		if info.Timeout.Mode != string(effective.Mode) || info.Timeout.Seconds != effective.Seconds {
			t.Errorf("catalog %q timeout = %#v, want %#v", actual.Name, info.Timeout, effective)
		}
	}
	if actualCount != status.Tools.Count {
		t.Fatalf("registered non-MCP tools = %d, catalog count = %d", actualCount, status.Tools.Count)
	}
}

func TestRuntimeToolsStatusRejectsInvalidBuiltinGroups(t *testing.T) {
	for _, group := range []tools.ToolGroup{"", tools.ToolGroupMCP, "unknown"} {
		t.Run(string(group), func(t *testing.T) {
			_, err := runtimeToolsStatusFromDefinitions([]tools.ToolDefinition{{
				Name:   "bad",
				Group:  group,
				Schema: map[string]any{"type": "object"},
			}}, tools.DefaultTimeoutSeconds)
			if err == nil {
				t.Fatalf("group %q unexpectedly accepted", group)
			}
		})
	}
}

func TestRuntimeMCPToolSchemaMatchesNormalizedRegistryDefinition(t *testing.T) {
	descriptor := mcp.ToolDescriptor{
		Name:        "nullable",
		Description: "Contains nullable schema fragments",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": nil,
			"properties": map[string]any{
				"query": nil,
				"items": map[string]any{
					"type":  "array",
					"items": nil,
				},
			},
		},
	}
	definition := tools.ToolDefinition{
		Name:        descriptor.Name,
		Group:       tools.ToolGroupMCP,
		Description: descriptor.Description,
		Schema:      descriptor.InputSchema,
	}
	registry := tools.NewRegistry()
	if err := registry.Register(definition.Bind(func(context.Context, map[string]any) (string, error) {
		return "", nil
	})); err != nil {
		t.Fatal(err)
	}
	registered, ok := registry.Get(descriptor.Name)
	if !ok {
		t.Fatal("normalized MCP tool was not registered")
	}

	projected := runtimeMCPToolInfos([]mcp.ToolDescriptor{descriptor}, 0)
	if len(projected) != 1 {
		t.Fatalf("projected tools = %#v", projected)
	}
	if !reflect.DeepEqual(projected[0].Schema, registered.Schema) {
		t.Fatalf("catalog schema = %#v, registered schema = %#v", projected[0].Schema, registered.Schema)
	}
}

func TestRuntimeStatusServiceIncludesPromptSkillsAndProvider(t *testing.T) {
	work := t.TempDir()
	homeAgents := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(work, "AGENTS.md"), "你好世界")
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "skills", "review", "SKILL.md"), `---
name: review
description: Review code changes
type: model-invocable
---
body`)
	tools := false
	cfg := config.Config{
		ProviderID:                "openai",
		ProviderProtocol:          "openai/responses",
		APIKey:                    "x",
		Model:                     "gpt-test",
		BaseURL:                   "https://example.test",
		ProviderCapabilities:      llm.CapabilityOverrides{Tools: &tools},
		WorkDir:                   work,
		HomeAgentsDir:             homeAgents,
		EnableUserGlobalResources: true,
	}

	status, err := NewRuntimeStatusService(cfg).Snapshot(RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if status.WorkDir != work {
		t.Fatalf("workdir = %q, want %q", status.WorkDir, work)
	}
	if status.Provider.ID != "openai" || status.Provider.Protocol != "openai/responses" || status.Provider.Model != "gpt-test" || status.Provider.Capabilities.Tools {
		t.Fatalf("provider = %+v", status.Provider)
	}
	if status.Sandbox.FileSystem.OutsideWorkspace != config.OutsideWorkspaceReadWrite || !status.Sandbox.Network.Enabled {
		t.Fatalf("sandbox = %+v", status.Sandbox)
	}
	if status.Skills.Count != 1 || status.Skills.Items[0].Name != "review" || status.Skills.Items[0].Source != "project" {
		t.Fatalf("skills = %+v", status.Skills)
	}
	var agentsEntry *RuntimeSystemPromptEntry
	for i := range status.SystemPrompt.Items {
		if status.SystemPrompt.Items[i].Path == filepath.Join(work, "AGENTS.md") {
			agentsEntry = &status.SystemPrompt.Items[i]
			break
		}
	}
	if agentsEntry == nil {
		t.Fatalf("system prompt missing workspace AGENTS.md: %+v", status.SystemPrompt.Items)
		return
	}
	if !strings.Contains(agentsEntry.Text, "你好世界") {
		t.Fatalf("agents text = %q", agentsEntry.Text)
	}
	if agentsEntry.Tokens != juexruntime.EstimateTextTokens(agentsEntry.Text) {
		t.Fatalf("tokens = %d, want byte-based runtime estimate", agentsEntry.Tokens)
	}
}

func TestRuntimeStatusServiceIncludesSessionScratchpadPrompt(t *testing.T) {
	work := t.TempDir()
	scratchpadDir := filepath.Join(work, ".juex", "sessions", "session-1", "scratchpad")
	status, err := NewRuntimeStatusService(config.Config{WorkDir: work}).Snapshot(RuntimeStatusOptions{
		ScratchpadDir: scratchpadDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range status.SystemPrompt.Items {
		if item.Key == "session_scratchpad" {
			if item.Path != scratchpadDir || !strings.Contains(item.Text, scratchpadDir) {
				t.Fatalf("scratchpad prompt = %+v, want path %q", item, scratchpadDir)
			}
			return
		}
	}
	t.Fatalf("system prompt missing session scratchpad: %+v", status.SystemPrompt.Items)
}

func TestRuntimeStatusServiceMCPStatusSourcesAndOverrides(t *testing.T) {
	work := t.TempDir()
	homeAgents := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(homeAgents, "mcp.json"), `{
  "mcpServers": {
    "shared": { "command": "user-shared" },
    "zeta": { "command": "user-zeta" }
  }
}`)
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "mcp.json"), `{
  "mcpServers": {
    "alpha": { "command": "$WORKDIR/bin/alpha", "args": ["--workdir", "$WORKDIR"] },
    "shared": { "command": "project-shared" }
  }
}`)
	cfg := config.Config{
		WorkDir:                   work,
		HomeAgentsDir:             homeAgents,
		EnableUserGlobalResources: true,
	}

	status, err := NewRuntimeStatusService(cfg).Snapshot(RuntimeStatusOptions{
		MCPToolDescriptors: map[string][]mcp.ToolDescriptor{
			"shared": {
				{Name: "alpha", Description: "first", InputSchema: map[string]any{"type": "object"}},
				{Name: "zeta", Description: "last", InputSchema: map[string]any{"type": "object"}},
			},
		},
		MCPErrors: map[string]string{"zeta": "boom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.MCP.Configured != 3 || status.MCP.Connected != 1 || status.MCP.Errors != 1 {
		t.Fatalf("mcp = %+v", status.MCP)
	}
	if len(status.MCP.Servers) != 3 {
		t.Fatalf("servers = %+v", status.MCP.Servers)
	}
	alpha, shared, zeta := status.MCP.Servers[0], status.MCP.Servers[1], status.MCP.Servers[2]
	if alpha.Name != "alpha" || alpha.Source != "project" || filepath.ToSlash(alpha.Command) != filepath.ToSlash(work)+"/bin/alpha" || alpha.Args[0] != "--workdir" || alpha.Args[1] != work || alpha.Status != "not_started" {
		t.Fatalf("alpha = %+v", alpha)
	}
	if shared.Name != "shared" || shared.Source != "project" || shared.Command != "project-shared" || !shared.Connected || shared.ToolCount != 2 {
		t.Fatalf("shared = %+v", shared)
	}
	if zeta.Name != "zeta" || zeta.Source != "user" || zeta.Status != "error" || zeta.Error != "boom" {
		t.Fatalf("zeta = %+v", zeta)
	}
}

func TestRuntimeStatusServiceTreatsZeroToolDescriptorMembershipAsConnected(t *testing.T) {
	work := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "mcp.json"), `{
  "mcpServers": {
    "empty": { "command": "empty-server" }
  }
}`)
	status, err := NewRuntimeStatusService(config.Config{WorkDir: work}).Snapshot(RuntimeStatusOptions{
		MCPToolDescriptors: map[string][]mcp.ToolDescriptor{"empty": {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.MCP.Connected != 1 || len(status.MCP.Servers) != 1 {
		t.Fatalf("mcp = %+v", status.MCP)
	}
	server := status.MCP.Servers[0]
	if !server.Connected || server.Status != "connected" || server.ToolCount != 0 || len(server.Tools) != 0 {
		t.Fatalf("zero-tool server = %+v", server)
	}
}

func TestRuntimeStatusServiceIncludesHookStatus(t *testing.T) {
	cfg := config.Config{
		WorkDir: t.TempDir(),
		Hooks: hooks.Config{Commands: []hooks.CommandHook{{
			Name:    "protect-write",
			Events:  []hooks.EventName{hooks.EventPreToolUse, hooks.EventStop},
			Tools:   []string{"write"},
			Command: []string{"python3", "hooks/protect.py"},
			Source:  "project",
		}}},
	}

	status, err := NewRuntimeStatusService(cfg).Snapshot(RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Hooks.Configured != 1 || len(status.Hooks.Commands) != 1 {
		t.Fatalf("hooks = %+v", status.Hooks)
	}
	hook := status.Hooks.Commands[0]
	if hook.Name != "protect-write" || hook.Source != "project" {
		t.Fatalf("hook identity = %+v", hook)
	}
	if strings.Join(hook.Events, ",") != "PreToolUse,Stop" {
		t.Fatalf("events = %+v", hook.Events)
	}
	if strings.Join(hook.Tools, ",") != "write" || strings.Join(hook.Command, " ") != "python3 hooks/protect.py" {
		t.Fatalf("hook command = %+v tools=%+v", hook.Command, hook.Tools)
	}
	if hook.TimeoutSeconds != hooks.DefaultTimeoutSeconds || hook.MaxOutputBytes != hooks.DefaultMaxOutputBytes {
		t.Fatalf("effective limits = timeout %d output %d", hook.TimeoutSeconds, hook.MaxOutputBytes)
	}
}

func TestRuntimeStatusServiceIncludesSandboxPolicy(t *testing.T) {
	cfg := config.Config{
		WorkDir: t.TempDir(),
		Sandbox: config.SandboxPolicy{
			Enabled: true,
			FileSystem: config.FileSystemSandboxPolicy{
				OutsideWorkspace: config.OutsideWorkspaceReadOnly,
			},
			Network: config.NetworkSandboxPolicy{Enabled: false},
		},
	}

	status, err := NewRuntimeStatusService(cfg).Snapshot(RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Sandbox.Enabled || status.Sandbox.FileSystem.OutsideWorkspace != config.OutsideWorkspaceReadOnly || status.Sandbox.Network.Enabled {
		t.Fatalf("sandbox = %+v", status.Sandbox)
	}
}

func TestRuntimeStatusServiceCachesSkillsWhenProvided(t *testing.T) {
	work := t.TempDir()
	skillPath := filepath.Join(work, ".agents", "skills", "review", "SKILL.md")
	mustWriteRuntimeStatusFile(t, skillPath, `---
name: review
description: cached
---
body`)
	cfg := config.Config{WorkDir: work}
	cache := NewRuntimeStatusSkillCache()

	first, err := NewRuntimeStatusService(cfg).Snapshot(RuntimeStatusOptions{SkillCache: cache})
	if err != nil {
		t.Fatal(err)
	}
	mustWriteRuntimeStatusFile(t, skillPath, `---
name: review
description: changed
---
body`)
	second, err := NewRuntimeStatusService(cfg).Snapshot(RuntimeStatusOptions{SkillCache: cache})
	if err != nil {
		t.Fatal(err)
	}
	if first.Skills.Items[0].Description != "cached" || second.Skills.Items[0].Description != "cached" {
		t.Fatalf("skills cache did not preserve first load: first=%+v second=%+v", first.Skills.Items, second.Skills.Items)
	}
}

func TestRuntimeStatusServiceRejectsExtensionResourceDuplicates(t *testing.T) {
	t.Run("mcp", func(t *testing.T) {
		work := t.TempDir()
		mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "mcp.json"), `{
  "mcpServers": {
    "shared": { "command": "project" }
  }
}`)
		mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "extensions", "demo", "mcp.json"), `{
  "mcpServers": {
    "shared": { "command": "extension" }
  }
}`)
		_, err := NewRuntimeStatusService(config.Config{WorkDir: work}).Snapshot(RuntimeStatusOptions{})
		if err == nil || !strings.Contains(err.Error(), `duplicate MCP server "shared"`) {
			t.Fatalf("err = %v, want duplicate MCP error", err)
		}
	})

	t.Run("skill", func(t *testing.T) {
		work := t.TempDir()
		mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "skills", "shared", "SKILL.md"), `---
name: shared
description: project
---
body`)
		mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "extensions", "demo", "skills", "shared", "SKILL.md"), `---
name: shared
description: extension
---
body`)
		_, err := NewRuntimeStatusService(config.Config{WorkDir: work}).Snapshot(RuntimeStatusOptions{})
		if err == nil || !strings.Contains(err.Error(), `duplicate skill "shared"`) {
			t.Fatalf("err = %v, want duplicate skill error", err)
		}
	})

	t.Run("hook", func(t *testing.T) {
		work := t.TempDir()
		mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "extensions", "demo", "hooks.yaml"), `trusted: true
commands:
  - name: shared
    events: [Stop]
    command: ["python3", "x.py"]
`)
		cfg := config.Config{
			WorkDir: work,
			Hooks: hooks.Config{Commands: []hooks.CommandHook{{
				Name:    "shared",
				Events:  []hooks.EventName{hooks.EventStop},
				Command: []string{"python3", "base.py"},
				Source:  "project",
			}}},
		}
		_, err := NewRuntimeStatusService(cfg).Snapshot(RuntimeStatusOptions{})
		if err == nil || !strings.Contains(err.Error(), `duplicate hook "shared"`) {
			t.Fatalf("err = %v, want duplicate hook error", err)
		}
	})
}

func mustWriteRuntimeStatusFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
