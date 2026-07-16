package runtime

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/tools"
)

func TestGoalToolDefinitionsBindSessionStateGroup(t *testing.T) {
	reg := tools.NewRegistry()
	eng := &Engine{Tools: reg, GoalState: NewGoalStateStore(t.TempDir(), GoalStateOptions{})}
	if err := RegisterGoalTools(reg, eng); err != nil {
		t.Fatal(err)
	}
	definitions := GoalToolDefinitions()
	if len(definitions) != 3 {
		t.Fatalf("definition count = %d, want 3", len(definitions))
	}
	for _, definition := range definitions {
		if definition.Group != tools.ToolGroupSessionState {
			t.Errorf("%s definition group = %q, want %q", definition.Name, definition.Group, tools.ToolGroupSessionState)
		}
		registered, ok := reg.Get(definition.Name)
		if !ok {
			t.Errorf("%s is not registered", definition.Name)
			continue
		}
		if got := registered.Definition(); !reflect.DeepEqual(got, definition) {
			t.Errorf("%s registered definition = %#v, want %#v", definition.Name, got, definition)
		}
	}
}

func TestGoalToolsCreateUpdateGetAndStaySessionScoped(t *testing.T) {
	reg := tools.NewRegistry()
	eng := &Engine{Tools: reg, GoalState: NewGoalStateStore(t.TempDir(), GoalStateOptions{})}
	if err := RegisterGoalTools(reg, eng); err != nil {
		t.Fatal(err)
	}
	createTool, ok := reg.Get(GoalToolCreate)
	if !ok {
		t.Fatal("create_goal is not registered")
	}
	createProperties := createTool.Schema["properties"].(map[string]any)
	for _, key := range []string{"description", "acceptance", "status_reason"} {
		if _, ok := createProperties[key]; !ok {
			t.Fatalf("create_goal schema missing %q: %#v", key, createProperties)
		}
	}
	if len(createProperties) != 3 {
		t.Fatalf("create_goal properties = %#v", createProperties)
	}
	updateTool, ok := reg.Get(GoalToolUpdate)
	if !ok {
		t.Fatal("update_goal is not registered")
	}
	updateProperties := updateTool.Schema["properties"].(map[string]any)
	for _, key := range []string{"description", "acceptance", "status", "status_reason"} {
		if _, ok := updateProperties[key]; !ok {
			t.Fatalf("update_goal schema missing %q: %#v", key, updateProperties)
		}
	}
	if len(updateProperties) != 4 {
		t.Fatalf("update_goal properties = %#v", updateProperties)
	}
	if !strings.Contains(strings.ToLower(updateTool.Description), "failure") || !strings.Contains(strings.ToLower(updateTool.Description), "status_reason") {
		t.Fatalf("update_goal description should recommend status_reason for failure: %q", updateTool.Description)
	}

	out, err := reg.Call(context.Background(), GoalToolGet, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"present":false`) {
		t.Fatalf("get before create = %s", out)
	}

	if _, err := reg.Call(context.Background(), GoalToolCreate, map[string]any{
		"description":   "finish the feature",
		"acceptance":    "command succeeds, docs/contract.md is updated, and go test ./... passes",
		"status_reason": "created from taskline spec",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Call(context.Background(), GoalToolUpdate, map[string]any{
		"status":        string(GoalStatusSuccess),
		"status_reason": "validated by tests",
	}); err != nil {
		t.Fatal(err)
	}
	out, err = reg.Call(context.Background(), GoalToolGet, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"present":true`,
		`"description":"finish the feature"`,
		`"acceptance":"command succeeds, docs/contract.md is updated, and go test ./... passes"`,
		`"status":"success"`,
		`"status_reason":"validated by tests"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("get missing %s:\n%s", want, out)
		}
	}

	if _, err := reg.Call(context.Background(), GoalToolUpdate, map[string]any{
		"status": string(GoalStatusFailure),
	}); err != nil {
		t.Fatalf("failure without status_reason should remain valid: %v", err)
	}

	other := NewGoalStateStore(t.TempDir(), GoalStateOptions{})
	snapshot, err := other.StatusSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != nil {
		t.Fatalf("goal leaked across sessions: %+v", snapshot)
	}
}
