package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/tests/testsupport/modulestate"

	goalmodule "github.com/juex-ai/juex/internal/features/goal"

	notesmodule "github.com/juex-ai/juex/internal/features/notes"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agentstate"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestModuleRetirementCoversInactiveAndArchivedThreadsWithoutReadingBodies(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: work, AgentStateDir: filepath.Join(work, "state")}
	main, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	dirs := []string{main.Thread.Dir}
	for _, archive := range []bool{false, true} {
		worker, err := main.ThreadStore.CreateWorker(thread.MainID, "")
		if err != nil {
			t.Fatal(err)
		}
		if archive {
			if err := main.ThreadStore.Archive(worker); err != nil {
				t.Fatal(err)
			}
			dirs = append(dirs, filepath.Join(main.ThreadStore.ArchiveDir(), worker.ID))
		} else {
			dirs = append(dirs, worker.Dir)
		}
		if err := worker.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range dirs {
		goal := goalmodule.NewGoalStateStore(dir, goalmodule.GoalStateOptions{})
		notes := notesmodule.NewNotesStore(dir)
		if _, err := goal.Create("retire", "include inactive scopes"); err != nil {
			t.Fatal(err)
		}
		if _, err := notes.Update("keep Notes"); err != nil {
			t.Fatal(err)
		}
		stageModuleRenewalCrash(t, dir, "g000001", goal.Path)
		if err := os.WriteFile(goal.Path+".context-renewal-g000001", []byte("broken body: cleanup must not parse"), 0600); err != nil {
			t.Fatal(err)
		}
		// Pre-deployment unowned state is an explicit manual boundary.
		if err := os.WriteFile(filepath.Join(dir, "goal_state.json"), []byte("unowned old file"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "scratchpad"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "scratchpad", "keep"), []byte("working file"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := main.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	disabled := cfg
	disabled.Modules = config.ModulePolicy{"goal": {Enabled: false}}
	if err := app.ValidateModuleConfig(disabled); err != nil {
		t.Fatal(err)
	}
	invalid := disabled
	invalid.Modules = config.ModulePolicy{"unknown": {Enabled: false}}
	if rejected, err := app.New(app.Options{Config: invalid, Provider: &bareScriptProvider{}}); err == nil {
		_ = rejected.CloseAndWait()
		t.Fatal("invalid candidate accepted")
	}
	for _, dir := range dirs {
		if _, err := os.Stat(goalmodule.NewGoalStateStore(dir, goalmodule.GoalStateOptions{}).Path + ".context-renewal-g000001"); err != nil {
			t.Fatal("preview/rejection changed state", err)
		}
	}
	applied, err := app.New(app.Options{Config: disabled, Provider: &bareScriptProvider{}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := applied.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		goal := goalmodule.NewGoalStateStore(dir, goalmodule.GoalStateOptions{})
		if _, err := os.Stat(filepath.Dir(goal.Path)); !os.IsNotExist(err) {
			t.Fatalf("Goal files or renewal backup survived: %v", err)
		}
		notes, err := notesmodule.NewNotesStore(dir).StatusSnapshot()
		if err != nil || notes == nil || notes.Content != "keep Notes" {
			t.Fatalf("other owner altered: %+v %v", notes, err)
		}
		for _, path := range []string{"goal_state.json", "scratchpad/keep", "thread.json", "generations/g000001.jsonl"} {
			if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
				t.Fatalf("retained %s: %v", path, err)
			}
		}
	}
	reenabled, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reenabled.CloseAndWait() }()
	goal, notes := modulestate.Stores(reenabled.Engine.ThreadRuntimeSnapshot().Modules)
	if got, err := goal.StatusSnapshot(); err != nil || got != nil {
		t.Fatalf("retired state revived: %v %v", got, err)
	}
	if _, err := notes.Update("new Notes"); err != nil {
		t.Fatal(err)
	}
	if err := reenabled.NewContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestModuleRetirementWaitsForAppliedAgentConfiguration(t *testing.T) {
	root, work := isolateModuleConfig(t), t.TempDir()
	home := filepath.Join(root, ".juex")
	resolved, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	enabled := []byte("preset: minimal\nmodules:\n  goal:\n    enabled: true\n  notes:\n    enabled: true\n  memory:\n    enabled: true\n  scratchpad:\n    enabled: true\n")
	if _, err := config.WriteAgentConfig(modulecatalog.Inventory(), enabled, home, resolved.Agent.ID, app.ValidateModuleConfig); err != nil {
		t.Fatal(err)
	}
	load := func() config.Config {
		t.Helper()
		cfg, err := config.LoadWithOptions(config.LoadOptions{ModuleInventory: modulecatalog.Inventory(), HomeDir: home, AgentID: resolved.Agent.ID, AgentState: config.AgentStateExisting})
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	running, err := app.New(app.Options{Config: load(), Provider: &bareScriptProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	goal, notes := modulestate.Stores(running.Engine.ThreadRuntimeSnapshot().Modules)
	if _, err := goal.Create("old writer", "retire only on application"); err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Update("old writer"); err != nil {
		t.Fatal(err)
	}
	memoryWrite, ok := running.Engine.Tools.Get("memory_write")
	if !ok {
		t.Fatal("enabled Memory tool unavailable")
	}
	if _, err := memoryWrite.Handler(t.Context(), memoryWriteInput("retained", "Durable acceptance knowledge")); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(running.Thread.Dir, "scratchpad", "retained.txt")
	if err := os.WriteFile(draft, []byte("durable working file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := running.Thread.Append(llm.TextMessage(llm.RoleUser, "durable acceptance history")); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(running.Thread.Dir, "generations", running.Thread.Info().GenerationID+".jsonl")
	if err := running.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	// Restart while enabled before applying the same Agent's sparse disablement.
	running, err = app.New(app.Options{Config: load(), Provider: &bareScriptProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	if g, n := modulestate.Status(running.Engine.ThreadRuntimeSnapshot().Modules); g == nil || n == nil {
		t.Fatalf("enabled restart lost work state: %v %v", g, n)
	}
	off := []byte("preset: minimal\n")
	if _, err := config.ValidateAgentConfig(modulecatalog.Inventory(), off, home, resolved.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := config.WriteAgentConfig(modulecatalog.Inventory(), []byte("modules:\n  unknown:\n    enabled: false\n"), home, resolved.Agent.ID, app.ValidateModuleConfig); err == nil {
		t.Fatal("invalid configuration was saved")
	}
	if _, err := config.WriteAgentConfig(modulecatalog.Inventory(), off, home, resolved.Agent.ID, app.ValidateModuleConfig); err != nil {
		t.Fatal(err)
	}
	if candidate, err := app.New(app.Options{Config: load(), Provider: &bareScriptProvider{}}); err == nil {
		_ = candidate.CloseAndWait()
		t.Fatal("applied while old writer still alive")
	}
	if got, err := goal.StatusSnapshot(); err != nil || got == nil {
		t.Fatalf("preview/save retired live state: %v %v", got, err)
	}
	if err := running.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	applied, err := app.New(app.Options{Config: load(), Provider: &bareScriptProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := applied.NewContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if applied.Thread.Info().GenerationID != "g000002" {
		t.Fatalf("disabled host /new generation=%s", applied.Thread.Info().GenerationID)
	}
	if err := applied.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	if got, err := goal.StatusSnapshot(); err != nil || got != nil {
		t.Fatalf("apply retained Goal: %v %v", got, err)
	}
	if got, err := notes.StatusSnapshot(); err != nil || got != nil {
		t.Fatalf("apply retained Notes: %v %v", got, err)
	}
	if _, err := config.WriteAgentConfig(modulecatalog.Inventory(), enabled, home, resolved.Agent.ID, app.ValidateModuleConfig); err != nil {
		t.Fatal(err)
	}
	fresh, err := app.New(app.Options{Config: load(), Provider: &bareScriptProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.CloseAndWait() }()
	g, n := modulestate.Status(fresh.Engine.ThreadRuntimeSnapshot().Modules)
	if g != nil || n != nil {
		t.Fatalf("re-enable revived work state: %v %v", g, n)
	}
	search, ok := fresh.Engine.Tools.Get("memory_search")
	if !ok {
		t.Fatal("re-enabled Memory tool unavailable")
	}
	if result, err := search.Handler(t.Context(), map[string]any{"query": "Durable acceptance knowledge"}); err != nil || !strings.Contains(result, "Durable acceptance knowledge") {
		t.Fatalf("retained Memory=%q, %v", result, err)
	}
	for path, marker := range map[string]string{draft: "durable working file", journal: "durable acceptance history"} {
		if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), marker) {
			t.Fatalf("retained %s=%q, %v", path, data, err)
		}
	}
}
