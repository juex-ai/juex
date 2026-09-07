package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/tools"
)

func TestModuleCatalogAndGuidanceArePure(t *testing.T) {
	agent := t.TempDir()
	m := New(agent)
	catalog, err := m.Tools(t.Context(), runtimemodule.ToolContext{})
	if err != nil || len(catalog) != 3 {
		t.Fatalf("tools=%+v, %v", catalog, err)
	}
	for _, tool := range catalog {
		if tool.ExecutionPolicy != tools.ToolExecutionSerial {
			t.Errorf("%s does not preserve batch order", tool.Name)
		}
	}
	sections, err := m.Context(t.Context(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration})
	if err != nil || len(sections) != 1 || !strings.Contains(sections[0].Text, "memory_search") || strings.Contains(sections[0].Text, "skill_load") {
		t.Fatalf("guidance=%+v, %v", sections, err)
	}
	if _, err := os.Stat(filepath.Join(agent, "modules")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("catalog/context performed file work: %v", err)
	}
	for _, tool := range catalog {
		if _, err := tool.Handler(t.Context(), map[string]any{}); err == nil {
			t.Errorf("%s accepted missing arguments", tool.Name)
		}
	}
}

type maintenanceObserver struct {
	checkpoint                            error
	requested, started, completed, failed int
	last                                  runtimemodule.PolicyExecution
}

func (o *maintenanceObserver) Requested(e runtimemodule.PolicyExecution) error {
	o.requested++
	o.last = e
	return o.checkpoint
}
func (o *maintenanceObserver) Started(runtimemodule.PolicyExecution) { o.started++ }
func (o *maintenanceObserver) Completed(runtimemodule.PolicyExecution, runtimemodule.PolicyResult) {
	o.completed++
}
func (o *maintenanceObserver) Errored(runtimemodule.PolicyExecution, runtimemodule.PolicyResult, error) {
	o.failed++
}

func TestMaintenanceLifecycleReportsNonfatalFailuresAndHonorsCheckpoint(t *testing.T) {
	agent := t.TempDir()
	m := New(agent)
	observer := &maintenanceObserver{}
	if _, err := m.ApplyCompaction(t.Context(), runtimemodule.CompactionPolicyRequest{Stage: runtimemodule.CompactionPolicyBefore, Observer: observer}); err != nil {
		t.Fatal(err)
	}
	if observer.requested != 0 {
		t.Fatal("maintenance ran before compaction")
	}
	observer.checkpoint = errors.New("journal unavailable")
	if _, err := m.ApplyThreadStart(t.Context(), runtimemodule.ThreadStartRequest{Observer: observer}); !runtimemodule.IsPolicyCheckpointError(err) {
		t.Fatalf("lost checkpoint failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agent, "modules")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ran before checkpoint")
	}
	observer.checkpoint = nil
	decision, err := m.ApplyThreadStart(t.Context(), runtimemodule.ThreadStartRequest{Observer: observer})
	if err != nil || decision.Reject || len(decision.Context) != 0 || observer.completed != 1 {
		t.Fatalf("start=%+v, %v, %+v", decision, err, observer)
	}
	index := filepath.Join(agent, "modules", "memory", indexFile)
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(index, 0700); err != nil {
		t.Fatal(err)
	}
	compact, err := m.ApplyCompaction(t.Context(), runtimemodule.CompactionPolicyRequest{Stage: runtimemodule.CompactionPolicyAfter, Observer: observer})
	if err != nil || len(compact.Context) != 0 || len(compact.Instructions) != 0 || observer.failed != 1 || observer.last.Point != runtimemodule.PolicyPointCompactionAfter {
		t.Fatalf("maintenance failure should be observable and nonfatal: %+v, %v, %+v", compact, err, observer)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.ApplyThreadStart(ctx, runtimemodule.ThreadStartRequest{Observer: observer}); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}
