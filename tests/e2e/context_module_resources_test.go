package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type contextResourceProvider struct {
	t     *testing.T
	cfg   config.Config
	path  string
	calls int
}

func (*contextResourceProvider) Name() string { return "context-resources" }

func (p *contextResourceProvider) Complete(_ context.Context, system string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.t.Helper()
	for module, marker := range map[string]string{
		modulecatalog.OperatingContext: "## Operating Context",
		modulecatalog.Scratchpad:       "## Thread Scratchpad",
		modulecatalog.AgentsMD:         "guidance-fixture-725",
		modulecatalog.Shell:            "Use the `exec_command` tool",
	} {
		if strings.Contains(system, marker) != p.cfg.ModuleEnabled(module) {
			p.t.Errorf("%s context does not match enabled=%v: %s", module, p.cfg.ModuleEnabled(module), system)
		}
	}
	if !p.cfg.ModuleEnabled(modulecatalog.Scratchpad) && strings.Contains(system, p.path) {
		p.t.Errorf("disabled scratchpad path published: %s", system)
	}
	if strings.Contains(system, "private-draft-725") {
		p.t.Error("scratchpad body injected automatically")
	}
	p.calls++
	if p.calls == 1 {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolName: "read", ToolUseID: "explicit-guidance", Input: map[string]any{"path": "AGENTS.md"}}}}, StopReason: llm.StopToolUse}, nil
	}
	for _, message := range history {
		for _, block := range message.Blocks {
			if block.ToolUseID == "explicit-guidance" && block.Type == llm.BlockToolResult && !block.IsError && strings.Contains(block.Content, "guidance-fixture-725") {
				return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "verified"), StopReason: llm.StopEndTurn}, nil
			}
		}
	}
	return llm.Response{}, fmt.Errorf("explicit AGENTS.md read did not succeed")
}

func TestContextModulesOwnThreadResources(t *testing.T) {
	for _, worker := range []bool{false, true} {
		for _, enabled := range []struct {
			name                              string
			scratch, agents, operating, shell bool
		}{
			{name: "files_and_environment", operating: true},
			{name: "scratchpad_only", scratch: true},
			{name: "guidance_only", agents: true},
			{name: "all", scratch: true, agents: true, operating: true, shell: true},
		} {
			t.Run(fmt.Sprintf("%s/worker=%v", enabled.name, worker), func(t *testing.T) {
				work := t.TempDir()
				if err := os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte("guidance-fixture-725"), 0600); err != nil {
					t.Fatal(err)
				}
				cfg := config.Config{Preset: config.PresetMinimal, WorkDir: work, AgentStateDir: filepath.Join(work, "state"), Modules: config.ModulePolicy{
					modulecatalog.Scratchpad: {Enabled: enabled.scratch}, modulecatalog.AgentsMD: {Enabled: enabled.agents}, modulecatalog.OperatingContext: {Enabled: enabled.operating}, modulecatalog.Shell: {Enabled: enabled.shell},
				}}
				cfg.Compaction = config.DefaultCompactionConfig()
				cfg.Compaction.KeepRecentTokens = 0
				id := thread.MainID
				if worker {
					store := thread.NewStore(cfg.AgentStateDir)
					main, err := store.EnsureMain()
					if err != nil {
						t.Fatal(err)
					}
					if err = main.Close(); err != nil {
						t.Fatal(err)
					}
					target, err := store.CreateWorker(thread.MainID, "resource-worker")
					if err != nil {
						t.Fatal(err)
					}
					id = target.ID
					if err = target.Close(); err != nil {
						t.Fatal(err)
					}
				}
				var path string
				for cycle := 0; cycle < 2; cycle++ {
					provider := &contextResourceProvider{t: t, cfg: cfg, path: path}
					application, err := app.New(app.Options{Config: cfg, Provider: provider, ThreadID: id, DisableMCP: true, SummaryProvider: &bareScriptProvider{steps: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "resource-check summary"), StopReason: llm.StopEndTurn}}}})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = application.CloseAndWait() })
					path = filepath.Join(application.Thread.Dir, "scratchpad")
					provider.path = path
					found := map[string]bool{}
					for _, set := range []*runtimemodule.Set{application.Engine.RuntimeModules, application.Engine.ThreadRuntimeSnapshot().Modules} {
						for _, descriptor := range set.Descriptors() {
							found[string(descriptor.ID)] = true
						}
					}
					for _, id := range []string{modulecatalog.OperatingContext, modulecatalog.Scratchpad, modulecatalog.AgentsMD, modulecatalog.Shell} {
						if found[id] != cfg.ModuleEnabled(id) {
							t.Errorf("module descriptor %s=%v enabled=%v", id, found[id], cfg.ModuleEnabled(id))
						}
					}
					info, statErr := os.Stat(path)
					if enabled.scratch {
						if statErr != nil || !info.IsDir() {
							t.Fatalf("enabled scratchpad = %v, %v", info, statErr)
						}
						if cycle == 0 {
							if err := os.WriteFile(filepath.Join(path, "draft.txt"), []byte("private-draft-725"), 0600); err != nil {
								t.Fatal(err)
							}
						}
					} else if cycle == 0 && !os.IsNotExist(statErr) {
						t.Fatalf("disabled scratchpad was created: %v, %v", info, statErr)
					}
					if out, err := application.Engine.Turn(t.Context(), "Read the guidance file explicitly."); err != nil || out != "verified" {
						t.Fatalf("turn=%q err=%v", out, err)
					}
					if _, err := application.CompactWithInstructions(t.Context(), "manual", false, ""); err != nil {
						t.Fatal(err)
					}
					if err := application.NewContext(t.Context()); err != nil {
						t.Fatal(err)
					}
					if enabled.scratch {
						if data, err := os.ReadFile(filepath.Join(path, "draft.txt")); err != nil || string(data) != "private-draft-725" {
							t.Fatalf("retained draft=%q, %v", data, err)
						}
					}
					if err := application.CloseAndWait(); err != nil {
						t.Fatal(err)
					}
					if !enabled.scratch && cycle == 0 {
						if err := os.WriteFile(path, []byte("broken-old-resource"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func TestDisabledScratchpadPreservesUnavailableExistingDirectory(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprintf("worker=%v", worker), func(t *testing.T) {
			work := t.TempDir()
			if err := os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte("guidance-fixture-725"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{Preset: config.PresetMinimal, WorkDir: work, AgentStateDir: filepath.Join(work, "state"), Modules: config.ModulePolicy{modulecatalog.Scratchpad: {Enabled: true}}}
			id := thread.MainID
			if worker {
				store := thread.NewStore(cfg.AgentStateDir)
				main, err := store.EnsureMain()
				if err != nil {
					t.Fatal(err)
				}
				if err = main.Close(); err != nil {
					t.Fatal(err)
				}
				target, err := store.CreateWorker(thread.MainID, "retained-worker")
				if err != nil {
					t.Fatal(err)
				}
				id = target.ID
				if err = target.Close(); err != nil {
					t.Fatal(err)
				}
			}
			initial, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, ThreadID: id, DisableMCP: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = initial.CloseAndWait() })
			path := filepath.Join(initial.Thread.Dir, "scratchpad")
			draft := filepath.Join(path, "draft.md")
			if err := os.WriteFile(draft, []byte("private-draft-725"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := initial.CloseAndWait(); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0755) })
			cfg.Modules[modulecatalog.Scratchpad] = config.ModuleSettings{Enabled: false}
			provider := &contextResourceProvider{t: t, cfg: cfg, path: path}
			disabled, err := app.New(app.Options{Config: cfg, Provider: provider, ThreadID: id, DisableMCP: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = disabled.CloseAndWait() })
			if out, err := disabled.Engine.Turn(t.Context(), "Read the guidance file."); err != nil || out != "verified" {
				t.Fatalf("turn=%q err=%v", out, err)
			}
			if err := disabled.NewContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := disabled.CloseAndWait(); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0755); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(draft); err != nil || string(data) != "private-draft-725" {
				t.Fatalf("disabled directory contents=%q, %v", data, err)
			}
		})
	}
}
