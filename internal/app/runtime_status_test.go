package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
)

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
		MCPToolCounts: map[string]int{"shared": 2},
		MCPErrors:     map[string]string{"zeta": "boom"},
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

func mustWriteRuntimeStatusFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
