package inputtracking

import (
	"context"
	"fmt"
	"strings"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

type testTracker struct{ inputs []runtimemodule.InputReminder }

func (s *testTracker) UncheckedInputs(context.Context) ([]runtimemodule.InputReminder, error) {
	return s.inputs, nil
}
func (*testTracker) CheckInputs(_ context.Context, ids []string) ([]string, error) { return ids, nil }

func TestContextKeepsEveryInputAndFreezesCompaction(t *testing.T) {
	tracker := &testTracker{}
	for i := 0; i < 150; i++ {
		tracker.inputs = append(tracker.inputs, runtimemodule.InputReminder{ID: fmt.Sprintf("i%d", i), Content: strings.Repeat("requirement ", 600)})
	}
	m := New(tracker)
	sections, err := m.Context(t.Context(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration})
	if err != nil || len(sections) != len(tracker.inputs)+1 {
		t.Fatalf("checklist omitted inputs: %d sections, %v", len(sections), err)
	}
	for index, input := range tracker.inputs {
		if !strings.Contains(sections[index+1].Text, input.Content) || !strings.Contains(sections[index+1].Text, input.ID) {
			t.Fatalf("input %d was shortened", index)
		}
	}
	contribution, err := m.CompactionContribution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tracker.inputs = nil
	canonical, err := contribution.Reconcile(t.Context(), "summary forgot these inputs")
	if err != nil || !strings.Contains(canonical, "i149") {
		t.Fatalf("compaction did not freeze IDs: %s, %v", canonical, err)
	}
}

func TestOnlyCheckToolValidatesIDs(t *testing.T) {
	m := New(&testTracker{})
	tools, err := m.Tools(t.Context(), runtimemodule.ToolContext{})
	if err != nil || len(tools) != 1 || tools[0].Name != ToolCheck {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
	for _, input := range []map[string]any{{}, {"input_ids": "i1"}, {"input_ids": []any{42}}, {"input_ids": []string{}}} {
		if _, err := tools[0].Handler(t.Context(), input); err == nil {
			t.Fatalf("accepted invalid input %v", input)
		}
	}
	if result, err := tools[0].Handler(t.Context(), map[string]any{"input_ids": []any{"i1", "i2"}}); err != nil || !strings.Contains(result, "i2") {
		t.Fatalf("check result = %s, %v", result, err)
	}
}
