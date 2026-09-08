package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	extensionsmodule "github.com/juex-ai/juex/internal/features/extensions"
	hooksmodule "github.com/juex-ai/juex/internal/features/hooks"
	mcpmodule "github.com/juex-ai/juex/internal/features/mcp"
	"github.com/juex-ai/juex/internal/features/memory"
	skillsmodule "github.com/juex-ai/juex/internal/features/skills"
	workerthreadsmodule "github.com/juex-ai/juex/internal/features/workerthreads"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/homestore"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/agentstate"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

func memoryConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), ProviderID: "openai", Model: "test", Preset: config.PresetMinimal, WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), ContextWindow: 32000,
		Modules: config.ModulePolicy{memory.ModuleID: {Enabled: true}, extensionsmodule.ModuleID: {Enabled: false}, mcpmodule.ModuleID: {Enabled: false}, skillsmodule.ModuleID: {Enabled: false}, hooksmodule.ModuleID: {Enabled: false}},
	}
	cfg.Compaction = config.DefaultCompactionConfig()
	cfg.Compaction.KeepRecentTokens = 1
	return cfg
}

func memoryApp(t *testing.T, cfg config.Config, provider llm.Provider) *app.App {
	t.Helper()
	a, err := app.New(app.Options{Config: cfg, Provider: provider, SummaryProvider: &moduleSummaryProvider{summary: "## Goal\nRemember stable knowledge\n## Critical Context\nNo transient state\n## Next Steps\nContinue"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	return a
}

func memoryCall(id, name string, input map[string]any) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ToolUseID: id, ToolName: name, Input: input}
}

func memoryWriteInput(name, body string) map[string]any {
	return map[string]any{"name": name, "description": "Stable project fact", "type": "project", "body": body}
}

func TestEndToEnd_ManuallyCopiedMemoryKeepsExtensionDataAndOtherProviders(t *testing.T) {
	isolateModuleConfig(t)
	cfg := memoryConfig(t)
	cfg.HomeJuexDir = t.TempDir()
	address, err := agentstate.NewAgentAddress(cfg.HomeJuexDir, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentAddress, cfg.AgentStateDir = address, address.StateDir()
	for _, id := range []string{extensionsmodule.ModuleID, mcpmodule.ModuleID, skillsmodule.ModuleID, hooksmodule.ModuleID} {
		cfg.Modules[id] = config.ModuleSettings{Enabled: true}
	}
	cfg.Extensions = config.ExtensionPolicy{Allow: []string{"catalog"}, Configured: true}
	installCatalogExtensionFixture(t, filepath.Join(cfg.HomeJuexDir, "extensions", "catalog"))
	oldInstall := filepath.Join(cfg.HomeJuexDir, "extensions", "memory")
	oldData := filepath.Join(cfg.AgentStateDir, "extensions", "memory")
	newData := filepath.Join(cfg.AgentStateDir, "modules", "memory")
	for _, dir := range []string{oldInstall, oldData, newData} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	document := "---\nname: durable\ndescription: \"Compatible transferred knowledge\"\ntype: project\ncreated_at: 2026-07-01T00:00:00+00:00\nupdated_at: 2026-07-02T00:00:00+00:00\n---\nKeep the original knowledge.\n"
	retained := map[string]string{
		filepath.Join(oldInstall, "juex.extension.json"):                            `{"manifest_version":1,"name":"memory","version":"1.0.0"}`,
		filepath.Join(oldInstall, "mcp.json"):                                       "invalid obsolete definition",
		filepath.Join(oldData, "durable.md"):                                        document,
		filepath.Join(oldData, "MEMORY.md"):                                         "old derived index",
		filepath.Join(cfg.AgentStateDir, "extensions", "private-addon", "keep.txt"): "unrelated private data",
	}
	for path, data := range retained {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The operator copies selected compatible entries while the Agent is stopped.
	if err := os.WriteFile(filepath.Join(newData, "durable.md"), []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &bareScriptProvider{steps: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			memoryCall("read-transferred", memory.ToolSearch, map[string]any{"query": "original knowledge"}),
			memoryCall("other-provider", "mcp__catalog__catalog_write", map[string]any{"body": "Other extension still works"}),
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "Cutover complete"), StopReason: llm.StopEndTurn},
	}}
	a := memoryApp(t, cfg, provider)
	if out, err := a.Run(t.Context(), "Read the transferred knowledge and exercise the other provider."); err != nil || out != "Cutover complete" {
		t.Fatalf("cutover turn=%q, %v", out, err)
	}
	assertSuccessfulProviderToolResults(t, provider.history[len(provider.history)-1], map[string]string{"read-transferred": "Keep the original knowledge.", "other-provider": "saved catalog"})
	if data, err := os.ReadFile(filepath.Join(cfg.AgentStateDir, "extensions", "catalog", "catalog-entry")); err != nil || string(data) != "Other extension still works" {
		t.Fatalf("other Extension private output=%q, %v", data, err)
	}
	owned := 0
	for _, entry := range a.Engine.RuntimeModules.ToolCatalog().Entries() {
		if entry.ModuleID == memory.ModuleID {
			owned++
		}
	}
	if owned != 3 {
		t.Fatalf("builtin Memory tool count=%d", owned)
	}
	for path, want := range retained {
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Errorf("cutover altered %s: %q, %v", path, data, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(newData, "durable.md")); err != nil || string(data) != document {
		t.Fatalf("copied authority changed: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(newData, "MEMORY.md")); err != nil || !strings.Contains(string(data), "[durable](durable.md)") {
		t.Fatalf("startup rebuilt index=%q, %v", data, err)
	}
}

func TestEndToEnd_MemoryResolvesRelativeEmbeddingStateScope(t *testing.T) {
	isolateModuleConfig(t)
	for _, explicitState := range []bool{false, true} {
		t.Run(map[bool]string{false: "relative-workdir", true: "relative-agent-state"}[explicitState], func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if err := os.Mkdir("work", 0700); err != nil {
				t.Fatal(err)
			}
			cfg := memoryConfig(t)
			cfg.Preset = config.PresetStandard
			delete(cfg.Modules, memory.ModuleID)
			cfg.WorkDir, cfg.AgentStateDir = "work", ""
			if explicitState {
				cfg.WorkDir, cfg.AgentStateDir = filepath.Join(root, "work"), "agent-state"
			}
			a := memoryApp(t, cfg, &bareScriptProvider{})
			index := filepath.Join(cfg.RuntimePaths().StateDir, "modules", "memory", "MEMORY.md")
			if data, err := os.ReadFile(index); err != nil || !strings.Contains(string(data), "# Memory Index") {
				t.Fatalf("relative scope startup index=%q, %v", data, err)
			}
			write, _ := a.Engine.Tools.Get(memory.ToolWrite)
			search, _ := a.Engine.Tools.Get(memory.ToolSearch)
			remove, _ := a.Engine.Tools.Get(memory.ToolDelete)
			if _, err := write.Handler(t.Context(), memoryWriteInput("embedded", "Embedding scope")); err != nil {
				t.Fatal(err)
			}
			if result, err := search.Handler(t.Context(), map[string]any{"query": "Embedding scope"}); err != nil || !strings.Contains(result, "Embedding scope") {
				t.Fatalf("relative scope search=%q, %v", result, err)
			}
			if err := a.Engine.RunThreadStartPolicies(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := remove.Handler(t.Context(), map[string]any{"name": "embedded"}); err != nil {
				t.Fatal(err)
			}
			if result, err := search.Handler(t.Context(), map[string]any{"query": ""}); err != nil || result != `{"memories":[]}` {
				t.Fatalf("relative scope delete=%q, %v", result, err)
			}
		})
	}
}

func TestEndToEnd_MemoryIndependentToolsAndRetainedKnowledge(t *testing.T) {
	isolateModuleConfig(t)
	cfg := memoryConfig(t)
	provider := &bareScriptProvider{steps: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			memoryCall("write", memory.ToolWrite, memoryWriteInput("stable", "Original knowledge")),
			memoryCall("search", memory.ToolSearch, map[string]any{"query": "ORIGINAL"}),
			memoryCall("replace", memory.ToolWrite, memoryWriteInput("stable", "Durable unique fixture content")),
			memoryCall("search-replaced", memory.ToolSearch, map[string]any{"query": "durable unique"}),
			memoryCall("temporary", memory.ToolWrite, memoryWriteInput("delete-me", "Delete only explicitly")),
			memoryCall("delete", memory.ToolDelete, map[string]any{"name": "delete-me"}),
			memoryCall("search-deleted", memory.ToolSearch, map[string]any{"query": "delete-me"}),
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "Memory tools complete"), StopReason: llm.StopEndTurn},
	}}
	a := memoryApp(t, cfg, provider)
	if out, err := a.Run(t.Context(), "Explicitly save and update the stable facts, then delete the requested entry."); err != nil || out != "Memory tools complete" {
		t.Fatalf("turn=%q, %v", out, err)
	}
	results := map[string]llm.Block{}
	for _, message := range a.Thread.History {
		for _, block := range message.Blocks {
			if block.Type == llm.BlockToolResult {
				results[block.ToolUseID] = block
			}
		}
	}
	for id, want := range map[string]string{"write": "Original knowledge", "search": "Original knowledge", "replace": "Durable unique fixture content", "search-replaced": "Durable unique fixture content", "temporary": "Delete only explicitly", "delete": "delete-me", "search-deleted": `"memories":[]`} {
		result, ok := results[id]
		if !ok || result.IsError || !strings.Contains(result.Content, want) || result.ResultFact == nil || result.ResultFact.Owner != memory.ModuleID {
			t.Errorf("%s outcome=%+v", id, result)
		}
	}
	for _, entry := range a.Engine.RuntimeModules.ToolCatalog().Entries() {
		if strings.HasPrefix(entry.Tool.Name, "memory_") && entry.ModuleID != memory.ModuleID {
			t.Errorf("tool owner=%+v", entry)
		}
	}
	prompt, err := a.Engine.SystemPromptWithError()
	if err != nil || !strings.Contains(prompt, "memory_search") || strings.Contains(prompt, "Durable unique fixture content") {
		t.Fatalf("guidance/body projection: %q, %v", prompt, err)
	}
	file := filepath.Join(cfg.AgentStateDir, "modules", "memory", "stable.md")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(filepath.Dir(file), "MEMORY.md")
	if err := os.WriteFile(index, []byte("broken derived index"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompactWithInstructions(t.Context(), "manual", false, ""); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(index); err != nil || !strings.Contains(string(data), "[stable](stable.md)") {
		t.Fatalf("post-compaction index=%q, %v", data, err)
	}
	if err := a.NewContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.CloseAndWait(); err != nil {
		t.Fatal(err)
	}

	cfg.Modules[memory.ModuleID] = config.ModuleSettings{Enabled: false}
	disabled := memoryApp(t, cfg, &bareScriptProvider{})
	if _, ok := disabled.Engine.Tools.Get(memory.ToolSearch); ok {
		t.Fatal("disabled tool exposed")
	}
	disabledPrompt, err := disabled.Engine.SystemPromptWithError()
	if err != nil || strings.Contains(disabledPrompt, "## Memory") {
		t.Fatalf("disabled guidance=%q, %v", disabledPrompt, err)
	}
	if err := disabled.NewContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := disabled.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	cfg.Modules[memory.ModuleID] = config.ModuleSettings{Enabled: true}
	restarted := memoryApp(t, cfg, &bareScriptProvider{})
	search, ok := restarted.Engine.Tools.Get(memory.ToolSearch)
	if !ok {
		t.Fatal("missing restarted search")
	}
	result, err := search.Handler(t.Context(), map[string]any{"query": "durable unique"})
	if err != nil || !strings.Contains(result, "Durable unique fixture content") {
		t.Fatalf("restart=%s, %v", result, err)
	}
	after, err := os.ReadFile(file)
	if err != nil || string(after) != string(before) {
		t.Fatalf("lifecycle altered authority: %v", err)
	}
	other := memoryApp(t, memoryConfig(t), &bareScriptProvider{})
	otherSearch, _ := other.Engine.Tools.Get(memory.ToolSearch)
	if result, err := otherSearch.Handler(t.Context(), map[string]any{"query": ""}); err != nil || result != `{"memories":[]}` {
		t.Fatalf("Agent knowledge leaked: %s, %v", result, err)
	}
}

func TestEndToEnd_MemoryPostCompactionCancellationPreservesCommittedGeneration(t *testing.T) {
	isolateModuleConfig(t)
	cfg := memoryConfig(t)
	a := memoryApp(t, cfg, &bareScriptProvider{steps: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "Stable fact saved"), StopReason: llm.StopEndTurn}}})
	write, _ := a.Engine.Tools.Get(memory.ToolWrite)
	if _, err := write.Handler(t.Context(), memoryWriteInput("retained", "Stable knowledge")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(t.Context(), "Discuss the stable project fact before compaction."); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cfg.AgentStateDir, "modules", "memory", "retained.md")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := homestore.AcquireLock(filepath.Join(filepath.Dir(file), ".lock"), homestore.LockTry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started := false
	unsubscribe := a.Bus.Subscribe("policy.started", func(event events.Event) {
		payload, ok := event.Payload.(runtime.PolicyStartedPayload)
		if ok && payload.ModuleID == memory.ModuleID && payload.PolicyPoint == "compaction_after" {
			started = true
			cancel()
		}
	})
	defer unsubscribe()
	generation := a.Thread.Info().GenerationID
	if _, err := a.CompactWithInstructions(ctx, "manual", false, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-compaction cancellation=%v, want context.Canceled", err)
	}
	if !started || a.Thread.Info().GenerationID == generation {
		t.Fatalf("maintenance started=%t, Generation=%s; expected committed compaction", started, a.Thread.Info().GenerationID)
	}
	if after, err := os.ReadFile(file); err != nil || string(after) != string(before) {
		t.Fatalf("cancellation changed knowledge: %v", err)
	}
}

func TestEndToEnd_MemoryMaintenanceFailureDoesNotBlockAndDisabledDoesNoFileWork(t *testing.T) {
	isolateModuleConfig(t)
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			cfg := memoryConfig(t)
			cfg.Modules[memory.ModuleID] = config.ModuleSettings{Enabled: enabled}
			index := filepath.Join(cfg.AgentStateDir, "modules", "memory", "MEMORY.md")
			if enabled {
				if err := os.MkdirAll(index, 0700); err != nil {
					t.Fatal(err)
				}
			}
			a := memoryApp(t, cfg, &bareScriptProvider{steps: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "continued"), StopReason: llm.StopEndTurn}}})
			startJournal := a.Thread.CurrentGenerationJournalPath()
			if _, err := a.Run(t.Context(), "Continue despite optional index failure."); err != nil {
				t.Fatal(err)
			}
			if _, err := a.CompactWithInstructions(t.Context(), "manual", false, ""); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(startJournal)
			if err != nil {
				t.Fatal(err)
			}
			current, err := os.ReadFile(a.Thread.CurrentGenerationJournalPath())
			if err != nil {
				t.Fatal(err)
			}
			all := string(data) + string(current)
			if enabled {
				for _, point := range []string{"thread_start", "compaction_after"} {
					found := false
					for _, line := range strings.Split(all, "\n") {
						if strings.Contains(line, `"policy.errored"`) && strings.Contains(line, `"module_id":"memory"`) && strings.Contains(line, point) {
							found = true
						}
					}
					if !found {
						t.Errorf("missing durable maintenance failure for %s", point)
					}
				}
			} else {
				if _, err := os.Stat(filepath.Dir(index)); !os.IsNotExist(err) {
					t.Fatalf("disabled initialized memory: %v", err)
				}
				if strings.Contains(all, `"module_id":"memory"`) {
					t.Fatal("disabled maintenance ran")
				}
			}
		})
	}
}

type memoryWorkerProvider struct {
	ready   chan string
	release chan struct{}
}

func (*memoryWorkerProvider) Name() string { return "memory-workers" }

func (p *memoryWorkerProvider) Complete(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	query := lastDirectUserText(history)
	if !historyHasToolResult(history, "shared-write") {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{memoryCall("shared-write", memory.ToolWrite, memoryWriteInput(query, "Shared durable "+query))}}, StopReason: llm.StopToolUse}, nil
	}
	p.ready <- query
	select {
	case <-p.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Worker saved memory"), StopReason: llm.StopEndTurn}, nil
}

func TestEndToEnd_MemoryMainAndConcurrentWorkersShareAgentStore(t *testing.T) {
	isolateModuleConfig(t)
	cfg := memoryConfig(t)
	cfg.Modules[workerthreadsmodule.ModuleID] = config.ModuleSettings{Enabled: true}
	provider := &memoryWorkerProvider{ready: make(chan string, 2), release: make(chan struct{})}
	a := memoryApp(t, cfg, provider)
	defer close(provider.release)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	create, _ := a.Engine.Tools.Get(workerthreadsmodule.ToolCreate)
	var workers []*agent.Agent
	for _, name := range []string{"worker-one", "worker-two"} {
		result, err := create.Handler(ctx, map[string]any{"query": name})
		if err != nil {
			t.Fatal(err)
		}
		var status agent.WorkerThreadStatus
		if err := json.Unmarshal([]byte(result), &status); err != nil {
			t.Fatal(err)
		}
		worker, ok := a.ManagedWorkerAgent(status.ThreadID)
		if !ok {
			t.Fatal("missing Worker")
		}
		workers = append(workers, worker)
	}
	for range 2 {
		select {
		case <-provider.ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	for _, reader := range append(workers, a.Agent) {
		search, ok := reader.Engine.Tools.Get(memory.ToolSearch)
		if !ok {
			t.Fatal("missing shared Memory")
		}
		result, err := search.Handler(ctx, map[string]any{"query": "Shared durable"})
		if err != nil || !strings.Contains(result, "worker-one") || !strings.Contains(result, "worker-two") {
			t.Fatalf("shared search=%q, %v", result, err)
		}
	}
	index, err := os.ReadFile(filepath.Join(cfg.AgentStateDir, "modules", "memory", "MEMORY.md"))
	if err != nil || strings.Count(string(index), "](") != 2 {
		t.Fatalf("shared index=%q, %v", index, err)
	}
}
