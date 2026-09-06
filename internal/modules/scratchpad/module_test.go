package scratchpad

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

func TestModulePreparesAndRetainsThreadWorkingFiles(t *testing.T) {
	work := t.TempDir()
	thread := runtimemodule.ThreadContext{ID: "worker", Dir: filepath.Join(work, "state", "threads", "worker")}
	request := runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration, Thread: &thread}
	mod := &Module{WorkDir: work}
	if sections, err := mod.Context(t.Context(), request); err != nil || len(sections) != 0 {
		t.Fatalf("unstarted context=%+v, %v", sections, err)
	}
	if err := mod.StartThread(t.Context(), thread); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(thread.Dir), "draft.md")
	if err := os.WriteFile(path, []byte("private-body-529"), 0600); err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 2; cycle++ {
		sections, err := mod.Context(t.Context(), request)
		if err != nil || len(sections) != 1 {
			t.Fatalf("context=%+v, %v", sections, err)
		}
		section := sections[0]
		if section.Path != Dir(thread.Dir) || !strings.Contains(section.Text, "workspace-relative path: state/threads/worker/scratchpad") || strings.Contains(section.Text, "private-body-529") {
			t.Fatalf("context=%+v", section)
		}
		for _, want := range []string{"not automatically added to context", "available file tools", "before compaction"} {
			if !strings.Contains(section.Text, want) {
				t.Errorf("guidance missing %q", want)
			}
		}
		if section.Projection != runtimemodule.ContextProjectionSystemPrompt || section.Budget != runtimemodule.UnboundedContextBudget() {
			t.Fatalf("context contract=%+v", section)
		}
		if err := mod.CloseThread(t.Context()); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "private-body-529" {
			t.Fatalf("retained draft=%q, %v", data, err)
		}
		mod = &Module{WorkDir: work}
		if err := mod.StartThread(t.Context(), thread); err != nil {
			t.Fatal(err)
		}
	}
}

func TestModuleDoesNotPublishFailedPreparation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Dir(dir), []byte("old broken resource"), 0600); err != nil {
		t.Fatal(err)
	}
	mod := &Module{}
	if err := mod.StartThread(t.Context(), runtimemodule.ThreadContext{Dir: dir}); err == nil {
		t.Fatal("expected preparation failure")
	}
	if sections, err := mod.Context(t.Context(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration}); err != nil || len(sections) != 0 {
		t.Fatalf("failed resource published=%+v, %v", sections, err)
	}
}

func TestDisabledFactoryDoesNotPrepareResource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Dir(dir), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	thread := runtimemodule.ThreadContext{Dir: dir}
	set, err := runtimemodule.BuildAndStartThreadSet(t.Context(), []runtimemodule.ThreadFactorySpec{{ID: ModuleID, Enabled: false, New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
		t.Fatal("disabled factory called")
		return nil, nil
	}}}, thread, runtimemodule.ToolContext{Thread: &thread})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.CloseThread(t.Context()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(Dir(dir)); err != nil || string(data) != "keep" {
		t.Fatalf("disabled resource=%q, %v", data, err)
	}
}
