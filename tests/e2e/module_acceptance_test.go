package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	chunkedwritemodule "github.com/juex-ai/juex/internal/features/chunkedwrite"
	workerthreadsmodule "github.com/juex-ai/juex/internal/features/workerthreads"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agentstate"
	"github.com/juex-ai/juex/internal/framework/runtime/contextbudget"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type moduleRequestBudget struct {
	SystemBytes         int `json:"system_bytes"`
	RuntimeMessageBytes int `json:"runtime_message_bytes"`
	ToolSchemaBytes     int `json:"tool_schema_bytes"`
	ResultBytes         int `json:"result_bytes"`
	ErrorBytes          int `json:"error_bytes"`
	EstimatedTokens     int `json:"estimated_input_tokens"`
}

type measuredModuleProvider struct {
	*moduleCapabilityProvider
	first, last moduleRequestBudget
}

func (p *measuredModuleProvider) Complete(ctx context.Context, system string, history []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	if !strings.Contains(system, "## Operating Context") {
		p.t.Error("required operating context missing")
	}
	encoded, err := json.Marshal(specs)
	if err != nil {
		return llm.Response{}, err
	}
	budget := moduleRequestBudget{SystemBytes: len(system), ToolSchemaBytes: len(encoded), EstimatedTokens: contextbudget.EstimateContextTokens(system, specs, history)}
	for _, message := range history {
		if message.Kind == llm.MessageKindRuntimeContext {
			encoded, err := json.Marshal(message)
			if err != nil {
				return llm.Response{}, err
			}
			budget.RuntimeMessageBytes += len(encoded)
		}
		for _, block := range message.Blocks {
			if block.Type == llm.BlockToolResult {
				if block.IsError {
					budget.ErrorBytes += len(block.Content)
				} else {
					budget.ResultBytes += len(block.Content)
				}
			}
		}
	}
	if p.calls == 0 {
		p.first = budget
	}
	p.last = budget
	return p.moduleCapabilityProvider.Complete(ctx, system, history, specs)
}

// This fixture traverses user, Home, Workspace/import and Agent/import layers
// before constructing the same App used by the resident runtime.
func acceptanceConfig(t *testing.T, preset string, workers bool) config.Config {
	t.Helper()
	user := isolateModuleConfig(t)
	home, work := t.TempDir(), t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(user, ".juex", "juex.yaml"), "modules:\n  shell:\n    enabled: false\n")
	write(filepath.Join(home, "juex.yaml"), "preset: standard\n")
	write(filepath.Join(work, ".juex", "preset.yaml"), "preset: "+preset+"\n")
	write(filepath.Join(work, ".juex", "juex.yaml"), "imports:\n  - source: preset.yaml\n")
	resolved, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	overrides := "modules:\n  shell:\n    enabled: true\n"
	if preset == config.PresetMinimal && workers {
		overrides += "  worker-threads:\n    enabled: true\n"
	}
	write(filepath.Join(resolved.Address.StateDir(), "switches.yaml"), overrides)
	if _, err := config.WriteAgentConfig(modulecatalog.Inventory(), []byte("imports:\n  - source: switches.yaml\n"), home, resolved.Agent.ID, app.ValidateModuleConfig); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{ModuleInventory: modulecatalog.Inventory(), HomeDir: home, AgentID: resolved.Agent.ID, AgentState: config.AgentStateExisting})
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range modulecatalog.Inventory().Definitions() {
		want := preset == config.PresetStandard || definition.Minimal || (workers && definition.ID == workerthreadsmodule.ModuleID)
		if cfg.ModuleEnabled(definition.ID) != want {
			t.Fatalf("layered %s=%v, want %v", definition.ID, cfg.ModuleEnabled(definition.ID), want)
		}
	}
	return cfg
}

func TestModuleAcceptanceLayeredMainWorkerRequests(t *testing.T) {
	for _, tc := range []struct {
		name, preset string
		workers      bool
	}{
		{"minimal", config.PresetMinimal, false},
		{"minimal_with_workers", config.PresetMinimal, true},
		{"standard", config.PresetStandard, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := acceptanceConfig(t, tc.preset, tc.workers)
			var mainNames []string
			id := thread.MainID
			for _, role := range []string{"Main", "Worker"} {
				if role == "Worker" && !tc.workers {
					continue
				}
				t.Run(role, func(t *testing.T) {
					provider := &measuredModuleProvider{moduleCapabilityProvider: &moduleCapabilityProvider{t: t, cfg: cfg, written: "draft", replacement: strings.Repeat("edited long draft\n", 220)}}
					if !cfg.ModuleEnabled(chunkedwritemodule.ModuleID) {
						provider.written = strings.Repeat("long draft\n", 300)
					}
					provider.planActions()
					for _, action := range provider.actions {
						if path, ok := action.Input["path"].(string); ok {
							action.Input["path"] = filepath.Join(strings.ToLower(role), path)
						}
						if patch, ok := action.Input["patch_text"].(string); ok {
							action.Input["patch_text"] = strings.ReplaceAll(patch, "patched.txt", strings.ToLower(role)+"/patched.txt")
						}
					}
					application, err := app.New(app.Options{Config: cfg, Provider: provider, ThreadID: id})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := application.CloseAndWait(); err != nil {
							t.Error(err)
						}
					})
					if out, err := application.Engine.Turn(t.Context(), "Verify available file tools."); err != nil || out != "verified" {
						t.Fatalf("turn=%q, %v", out, err)
					}
					var names []string
					for _, spec := range provider.specs {
						names = append(names, spec.Name)
					}
					slices.Sort(names)
					if role == "Main" {
						mainNames = names
					} else {
						// External observation management belongs to Main; all other
						// enabled capabilities use the same composition in Workers.
						workerNames := slices.DeleteFunc(slices.Clone(mainNames), func(name string) bool {
							return strings.HasPrefix(name, "observable_") || name == "schedule_create"
						})
						if !slices.Equal(names, workerNames) {
							t.Fatalf("Worker tools=%v, expected=%v", names, workerNames)
						}
					}
					if tc.name == "minimal" {
						want := []string{"edit", "exec_command", "list_shell_sessions", "read", "write", "write_stdin"}
						if !slices.Equal(names, want) {
							t.Fatalf("minimal tools=%v", names)
						}
					}
					for _, name := range []string{"memory_search", "memory_write", "memory_delete", "get_goal", "update_notes", "context_new"} {
						if slices.Contains(names, name) != (tc.preset == config.PresetStandard) {
							t.Errorf("%s capability does not follow preset: %v", name, names)
						}
					}
					if provider.first.ResultBytes != 0 || provider.first.ErrorBytes != 0 || provider.last.ResultBytes == 0 || provider.last.ErrorBytes == 0 {
						t.Fatalf("capture missed actual results/errors: first=%+v last=%+v", provider.first, provider.last)
					}
					for stage, budget := range map[string]moduleRequestBudget{"initial": provider.first, "after_tools": provider.last} {
						data, err := json.Marshal(budget)
						if err != nil {
							t.Fatal(err)
						}
						t.Logf("MODULE_BUDGET %s/%s/%s tools=%d %s", tc.name, role, stage, len(names), data)
					}
					if role == "Main" && tc.workers {
						worker, err := application.ThreadStore.CreateWorker(thread.MainID, fmt.Sprintf("%s-worker", tc.name))
						if err != nil {
							t.Fatal(err)
						}
						id = worker.ID
						if err := worker.Close(); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		})
	}
}

func TestModuleAcceptanceMinimalExternalizedRead(t *testing.T) {
	cfg := acceptanceConfig(t, config.PresetMinimal, false)
	cfg.ToolOutput.InlineMaxBytes = 64
	cfg.ToolOutput.PreviewHeadBytes = 8
	cfg.ToolOutput.PreviewTailBytes = 8
	original := "artifact-read-success\n" + strings.Repeat("externalized detail\n", 20)
	path := filepath.Join(cfg.WorkDir, "large.txt")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &spoolReadProvider{t: t, firstReadPath: path}
	application, err := app.New(app.Options{Config: cfg, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	if out, err := application.Engine.Turn(t.Context(), "Read the externalized result through the available read tool."); err != nil || out != "TASK COMPLETE: projected Artifact read" {
		t.Fatalf("turn=%q, %v", out, err)
	}
	if data, err := os.ReadFile(provider.readURI); err != nil || string(data) != original {
		t.Fatalf("externalized payload=%q, %v", data, err)
	}
}

func TestModuleAcceptanceMinimalShellSessionCancellation(t *testing.T) {
	application, err := app.New(app.Options{Config: acceptanceConfig(t, config.PresetMinimal, false), Provider: &bareScriptProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	registry := application.Engine.Tools
	output, err := registry.Call(t.Context(), "exec_command", map[string]any{"cmd": slowShellYieldCommand(), "yield_time_ms": 250})
	if err != nil {
		t.Fatal(err)
	}
	var sessionID int
	for _, line := range strings.Split(output, "\n") {
		if _, err := fmt.Sscanf(line, "Process running with session ID %d", &sessionID); err == nil {
			break
		}
	}
	if sessionID == 0 {
		t.Fatalf("missing asynchronous session: %s", output)
	}
	listed, err := registry.Call(t.Context(), "list_shell_sessions", map[string]any{})
	if err != nil || !strings.Contains(listed, fmt.Sprintf("session_id=%d", sessionID)) {
		t.Fatalf("running session listing=%q, %v", listed, err)
	}
	output, err = registry.Call(t.Context(), "write_stdin", map[string]any{"session_id": sessionID, "chars": "\x03", "yield_time_ms": 1500})
	if err == nil || !strings.Contains(output, "Process exited with code") || strings.Contains(output, "slow done") {
		t.Fatalf("interrupted session=%q, %v", output, err)
	}
	listed, err = registry.Call(t.Context(), "list_shell_sessions", map[string]any{})
	if err != nil || strings.Contains(listed, fmt.Sprintf("session_id=%d", sessionID)) {
		t.Fatalf("completed session still running=%q, %v", listed, err)
	}
}
