package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/tests/testsupport/modulestate"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	web "github.com/juex-ai/juex/internal/entrypoints/agenthttp"
	notesmodule "github.com/juex-ai/juex/internal/features/notes"
	"github.com/juex-ai/juex/internal/features/scratchpad"
	tasksmodule "github.com/juex-ai/juex/internal/features/tasks"
	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestModuleLifecycle_AllCompiledModulesDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}
	modules := config.ModulePolicy{}
	for _, definition := range modulecatalog.Inventory().Definitions() {
		modules[definition.ID] = config.ModuleSettings{Enabled: false}
	}
	work := t.TempDir()
	application, err := app.New(app.Options{
		Config: config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: work, Modules: modules}, Provider: &bareScriptProvider{}, WorkDir: work,
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
		Config:   config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: work, AgentStateDir: filepath.Join(work, ".juex")},
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
	if err := os.WriteFile(filepath.Join(scratchpad.Dir(before.Thread.Dir), "durable.txt"), []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks, notes := modulestate.Stores(before.Modules)
	if tasks == nil || notes == nil {
		t.Fatal("Tasks and Notes Modules did not expose their stores")
	}
	if _, err := tasks.Create(tasksmodule.Create{Status: tasksmodule.Done, Title: "Tracked work", Description: "finish the current context", Acceptance: "new Generation is empty"}); err != nil {
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
	if data, err := os.ReadFile(filepath.Join(scratchpad.Dir(after.Thread.Dir), "durable.txt")); err != nil || string(data) != "retained" {
		t.Fatalf("scratchpad after /new = %q, %v", data, err)
	}
	if snapshot, err := tasks.StatusSnapshot(); err != nil || snapshot != nil {
		t.Fatalf("Tasks after /new = %+v, %v", snapshot, err)
	}
	if snapshot, err := notes.StatusSnapshot(); err != nil || snapshot != nil {
		t.Fatalf("Notes after /new = %+v, %v", snapshot, err)
	}
	for _, path := range []string{notes.Path} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("module state file survived /new: %s: %v", path, err)
		}
	}

	threadContext := runtimemodule.ThreadContext{ID: after.Thread.ID, Dir: after.Thread.Dir}
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

func TestModuleLifecycle_DisabledTasksAndNotesRetireBeforeReenable(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: work, AgentStateDir: filepath.Join(work, "state")}
	first, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]map[string]any{
		"create_task":  {"title": "Retire work", "description": "retire current work", "acceptance": "re-enable empty"},
		"update_notes": {"content": "retire these notes"},
	} {
		tool, ok := first.Engine.Tools.Get(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if _, err := tool.Handler(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	tasks, notes := modulestate.Stores(first.Engine.ThreadRuntimeSnapshot().Modules)
	if err := first.Thread.Append(llm.TextMessage(llm.RoleUser, "retain history")); err != nil {
		t.Fatal(err)
	}
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	// An ordinary restart keeps both owners' current state.
	restarted, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	g, n := modulestate.Status(restarted.Engine.ThreadRuntimeSnapshot().Modules)
	if g == nil || n == nil {
		t.Fatalf("ordinary restart lost state: %v %v", g, n)
	}
	if err := restarted.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	disabled := cfg
	disabled.Modules = config.ModulePolicy{"tasks": {Enabled: false}, "notes": {Enabled: false}}
	response := httptest.NewRecorder()
	web.NewReadOnlyAPIHandler(disabled).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.MainID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("read-only request: %d %s", response.Code, response.Body.String())
	}
	if g, err := tasks.StatusSnapshot(); err != nil || g == nil {
		t.Fatalf("read-only request retired Tasks: %v %v", g, err)
	}
	if n, err := notes.StatusSnapshot(); err != nil || n == nil {
		t.Fatalf("read-only request retired Notes: %v %v", n, err)
	}
	second, err := app.New(app.Options{Config: disabled, Provider: &bareScriptProvider{}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	if got, err := tasks.StatusSnapshot(); err != nil || got != nil {
		t.Fatalf("retired Tasks = %+v, %v", got, err)
	}
	if got, err := notes.StatusSnapshot(); err != nil || got != nil {
		t.Fatalf("retired Notes = %+v, %v", got, err)
	}
	third, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = third.CloseAndWait() })
	g, n = modulestate.Status(third.Engine.ThreadRuntimeSnapshot().Modules)
	if g != nil || n != nil {
		t.Fatalf("re-enabled state revived from history: %v %v", g, n)
	}
	if len(third.Thread.History) == 0 {
		t.Fatal("retirement deleted event history")
	}
}

func TestModuleLifecycle_InterruptedRenewalRecoversBeforeArchive(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}
	work := t.TempDir()
	application, err := app.New(app.Options{
		Config:   config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: work, AgentStateDir: filepath.Join(work, ".juex")},
		Provider: &bareScriptProvider{}, WorkDir: work, DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.CloseAndWait() })

	worker, err := application.ThreadStore.CreateWorker(thread.MainID, "recover-before-archive", 2)
	if err != nil {
		t.Fatal(err)
	}
	workerID := worker.ID
	tasks := tasksmodule.NewStore(worker.Dir, tasksmodule.Options{})
	notes := notesmodule.NewNotesStore(worker.Dir)
	if _, err := tasks.Create(tasksmodule.Create{Status: tasksmodule.Done, Title: "Tracked work", Description: "preserve interrupted state", Acceptance: "archive after recovery"}); err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Update("- [ ] preserve before archive"); err != nil {
		t.Fatal(err)
	}
	generationID := worker.Projection().CurrentGeneration.ID
	stageModuleRenewalCrash(t, worker.Dir, generationID, tasks.Path, notes.Path)
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
	defer func() { _ = archived.Close() }()
	taskSnapshot, taskErr := tasksmodule.NewStore(archived.Dir, tasksmodule.Options{}).StatusSnapshot()
	notesSnapshot, notesErr := notesmodule.NewNotesStore(archived.Dir).StatusSnapshot()
	if taskErr != nil || taskSnapshot == nil || taskSnapshot.Tasks[0].Description != "preserve interrupted state" {
		t.Fatalf("archived Tasks = %+v, %v", taskSnapshot, taskErr)
	}
	if notesErr != nil || notesSnapshot == nil || notesSnapshot.Content != "- [ ] preserve before archive" {
		t.Fatalf("archived Notes = %+v, %v", notesSnapshot, notesErr)
	}
	backups, err := filepath.Glob(filepath.Join(archived.Dir, "modules", "*", "*.context-renewal-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("archive retained recovery backups: %v", backups)
	}
}

func stageModuleRenewalCrash(t *testing.T, dir, generationID string, paths ...string) {
	t.Helper()
	entries := make([]map[string]string, 0, len(paths))
	for _, path := range paths {
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, map[string]string{"path": filepath.ToSlash(relative), "generation_id": generationID})
	}
	data, err := json.Marshal(map[string]any{"version": 1, "files": entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "context-renewal.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if err := os.Rename(path, path+".context-renewal-"+generationID); err != nil {
			t.Fatal(err)
		}
	}
}
