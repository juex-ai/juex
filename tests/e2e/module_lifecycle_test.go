package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/thread"
)

func TestModuleLifecycle_AllCompiledModulesDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}
	modules := config.ModulePolicy{}
	for _, id := range []string{
		"builtin-tools",
		"project-guidance",
		"skills",
		"worker-threads",
		"observables",
		"mcp",
		"context-control",
		"thread-context",
		"goal",
		"notes",
		"hooks",
	} {
		modules[id] = config.ModuleSettings{Enabled: false}
	}
	work := t.TempDir()
	application, err := app.New(app.Options{
		Config: config.Config{WorkDir: work, Modules: modules}, Provider: &bareScriptProvider{}, WorkDir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.CloseAndWait() })

	if descriptors := application.Engine.RuntimeModules.Descriptors(); len(descriptors) != 0 {
		t.Fatalf("Runtime Modules = %#v, want none", descriptors)
	}
	if descriptors := application.Engine.ThreadRuntimeSnapshot().Modules.Descriptors(); len(descriptors) != 0 {
		t.Fatalf("Thread Modules = %#v, want none", descriptors)
	}
	if serving := application.Engine.Tools.List(); len(serving) != 0 {
		t.Fatalf("serving Tools = %#v, want none", serving)
	}
}

func TestModuleLifecycle_NewGenerationKeepsThreadScopedSet(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}
	work := t.TempDir()
	application, err := app.New(app.Options{
		Config:   config.Config{WorkDir: work, AgentStateDir: filepath.Join(work, ".juex")},
		Provider: &bareScriptProvider{}, WorkDir: work, DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.CloseAndWait() })

	before := application.Engine.ThreadRuntimeSnapshot()
	if before.Thread == nil || before.Thread.ID != thread.MainID || before.Modules == nil {
		t.Fatalf("initial Thread runtime = %+v", before)
	}
	if err := os.WriteFile(filepath.Join(before.ScratchpadDir, "durable.txt"), []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	goal, notes := runtime.ThreadStateStoresFromModules(before.Modules)
	if goal == nil || notes == nil {
		t.Fatal("Goal and Notes Modules did not expose their stores")
	}
	if _, err := goal.Create("finish the current context", "new Generation is empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Update("- [x] preserve Scratchpad\n- [ ] start fresh"); err != nil {
		t.Fatal(err)
	}
	if err := application.NewContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := application.Engine.ThreadRuntimeSnapshot()
	if after.Thread != before.Thread || after.Modules != before.Modules {
		t.Fatalf("/new replaced Thread-scoped runtime: before=%p/%p after=%p/%p", before.Thread, before.Modules, after.Thread, after.Modules)
	}
	if info := after.Thread.Info(); info.GenerationID != "g000002" {
		t.Fatalf("generation = %q, want g000002", info.GenerationID)
	}
	if data, err := os.ReadFile(filepath.Join(after.ScratchpadDir, "durable.txt")); err != nil || string(data) != "retained" {
		t.Fatalf("scratchpad after /new = %q, %v", data, err)
	}
	if snapshot, err := goal.StatusSnapshot(); err != nil || snapshot != nil {
		t.Fatalf("Goal after /new = %+v, %v", snapshot, err)
	}
	if snapshot, err := notes.StatusSnapshot(); err != nil || snapshot != nil {
		t.Fatalf("Notes after /new = %+v, %v", snapshot, err)
	}
	for _, path := range []string{goal.Path, filepath.Join(after.Thread.Dir, "notes.md")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("module state file survived /new: %s: %v", path, err)
		}
	}

	threadContext := runtimemodule.ThreadContext{ID: after.Thread.ID, Dir: after.Thread.Dir, ScratchpadDir: after.ScratchpadDir}
	sections, err := after.Modules.Context(context.Background(), runtimemodule.ContextRequest{
		Purpose: runtimemodule.ContextPurposeProviderIteration,
		Thread:  &threadContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) == 0 {
		t.Fatal("Thread set contributed no provider context")
	}
	for _, section := range sections {
		if section.ModuleID == "" || section.Scope != runtimemodule.ScopeThread || strings.TrimSpace(section.Source) == "" {
			t.Errorf("Thread context lacks provenance: %+v", section)
		}
	}
}

func TestModuleLifecycle_DisabledGoalAndNotesSurviveNewAndReloadWhenEnabled(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}
	work := t.TempDir()
	stateDir := filepath.Join(work, ".juex")
	cfg := config.Config{WorkDir: work, AgentStateDir: stateDir}

	first, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, WorkDir: work, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	goal, notes := runtime.ThreadStateStoresFromModules(first.Engine.ThreadRuntimeSnapshot().Modules)
	if goal == nil || notes == nil {
		t.Fatal("Goal and Notes Modules did not expose their stores")
	}
	if _, err := goal.Create("retain while disabled", "reload the exact file"); err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Update("- [ ] retained while disabled"); err != nil {
		t.Fatal(err)
	}
	goalBefore, err := os.ReadFile(goal.Path)
	if err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(first.Thread.Dir, "notes.md")
	notesBefore, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}

	disabled := cfg
	disabled.Modules = config.ModulePolicy{
		"goal":  {Enabled: false},
		"notes": {Enabled: false},
	}
	second, err := app.New(app.Options{Config: disabled, Provider: &bareScriptProvider{}, WorkDir: work, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	goalStatus, notesStatus := second.ThreadStateStatus()
	if goalStatus != nil || notesStatus != nil {
		t.Fatalf("disabled module state leaked through status: Goal=%+v Notes=%+v", goalStatus, notesStatus)
	}
	if err := second.NewContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(goal.Path); err != nil || string(got) != string(goalBefore) {
		t.Fatalf("disabled Goal file changed across /new: %q, %v", got, err)
	}
	if got, err := os.ReadFile(notesPath); err != nil || string(got) != string(notesBefore) {
		t.Fatalf("disabled Notes file changed across /new: %q, %v", got, err)
	}

	third, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, WorkDir: work, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = third.CloseAndWait() })
	goalStatus, notesStatus = third.ThreadStateStatus()
	if goalStatus == nil || goalStatus.Description != "retain while disabled" {
		t.Fatalf("re-enabled Goal = %+v", goalStatus)
	}
	if notesStatus == nil || notesStatus.Content != "- [ ] retained while disabled" {
		t.Fatalf("re-enabled Notes = %+v", notesStatus)
	}
	if got := third.Thread.Info().GenerationID; got != "g000002" {
		t.Fatalf("Generation after disabled /new = %q", got)
	}
}

func TestModuleLifecycle_InterruptedRenewalRecoversBeforeArchive(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}
	work := t.TempDir()
	application, err := app.New(app.Options{
		Config:   config.Config{WorkDir: work, AgentStateDir: filepath.Join(work, ".juex")},
		Provider: &bareScriptProvider{}, WorkDir: work, DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.CloseAndWait() })

	worker, err := application.ThreadStore.CreateWorker(thread.MainID, "recover-before-archive")
	if err != nil {
		t.Fatal(err)
	}
	workerID := worker.ID
	goal := workmem.NewGoalStateStore(worker.Dir, workmem.GoalStateOptions{})
	notes := workmem.NewNotesStore(worker.Dir)
	if _, err := goal.Create("preserve interrupted state", "archive after recovery"); err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Update("- [ ] preserve before archive"); err != nil {
		t.Fatal(err)
	}
	generationID := worker.Projection().CurrentGeneration.ID
	if _, _, err := goal.StageClearForContextRenewal(generationID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := notes.StageClearForContextRenewal(generationID); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := application.ThreadStore.OpenActive(workerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.ThreadStore.Archive(reopened); err != nil {
		t.Fatal(err)
	}
	archived, err := application.ThreadStore.OpenArchived(workerID)
	if err != nil {
		t.Fatal(err)
	}
	defer archived.Close()
	goalSnapshot, goalErr := workmem.NewGoalStateStore(archived.Dir, workmem.GoalStateOptions{}).StatusSnapshot()
	notesSnapshot, notesErr := workmem.NewNotesStore(archived.Dir).StatusSnapshot()
	if goalErr != nil || goalSnapshot == nil || goalSnapshot.Description != "preserve interrupted state" {
		t.Fatalf("archived Goal = %+v, %v", goalSnapshot, goalErr)
	}
	if notesErr != nil || notesSnapshot == nil || notesSnapshot.Content != "- [ ] preserve before archive" {
		t.Fatalf("archived Notes = %+v, %v", notesSnapshot, notesErr)
	}
	backups, err := filepath.Glob(filepath.Join(archived.Dir, "*.context-renewal-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("archive retained recovery backups: %v", backups)
	}
}
