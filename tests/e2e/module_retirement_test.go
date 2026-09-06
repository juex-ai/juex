package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/thread"
)

func TestModuleRetirementCoversInactiveAndArchivedThreadsWithoutReadingBodies(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{WorkDir: work, AgentStateDir: filepath.Join(work, "state")}
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
		goal := workmem.NewGoalStateStore(dir, workmem.GoalStateOptions{})
		notes := workmem.NewNotesStore(dir)
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
		if _, err := os.Stat(workmem.NewGoalStateStore(dir, workmem.GoalStateOptions{}).Path + ".context-renewal-g000001"); err != nil {
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
		goal := workmem.NewGoalStateStore(dir, workmem.GoalStateOptions{})
		if _, err := os.Stat(filepath.Dir(goal.Path)); !os.IsNotExist(err) {
			t.Fatalf("Goal files or renewal backup survived: %v", err)
		}
		notes, err := workmem.NewNotesStore(dir).StatusSnapshot()
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
	goal, notes := runtime.ThreadStateStoresFromModules(reenabled.Engine.ThreadRuntimeSnapshot().Modules)
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
	enabled := []byte("preset: minimal\nmodules:\n  goal:\n    enabled: true\n  notes:\n    enabled: true\n")
	if _, err := config.WriteAgentConfig(enabled, home, resolved.Agent.ID, app.ValidateModuleConfig); err != nil {
		t.Fatal(err)
	}
	load := func() config.Config {
		t.Helper()
		cfg, err := config.LoadWithOptions(config.LoadOptions{HomeDir: home, AgentID: resolved.Agent.ID, AgentState: config.AgentStateExisting})
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	running, err := app.New(app.Options{Config: load(), Provider: &bareScriptProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	goal, notes := runtime.ThreadStateStoresFromModules(running.Engine.ThreadRuntimeSnapshot().Modules)
	if _, err := goal.Create("old writer", "retire only on application"); err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Update("old writer"); err != nil {
		t.Fatal(err)
	}
	off := []byte("preset: minimal\n")
	if _, err := config.ValidateAgentConfig(off, home, resolved.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := config.WriteAgentConfig([]byte("modules:\n  unknown:\n    enabled: false\n"), home, resolved.Agent.ID, app.ValidateModuleConfig); err == nil {
		t.Fatal("invalid configuration was saved")
	}
	if _, err := config.WriteAgentConfig(off, home, resolved.Agent.ID, app.ValidateModuleConfig); err != nil {
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
	if err := applied.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	if got, err := goal.StatusSnapshot(); err != nil || got != nil {
		t.Fatalf("apply retained Goal: %v %v", got, err)
	}
	if got, err := notes.StatusSnapshot(); err != nil || got != nil {
		t.Fatalf("apply retained Notes: %v %v", got, err)
	}
	if _, err := config.WriteAgentConfig(enabled, home, resolved.Agent.ID, app.ValidateModuleConfig); err != nil {
		t.Fatal(err)
	}
	fresh, err := app.New(app.Options{Config: load(), Provider: &bareScriptProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.CloseAndWait() }()
	g, n := fresh.ThreadStateStatus()
	if g != nil || n != nil {
		t.Fatalf("re-enable revived work state: %v %v", g, n)
	}
}
