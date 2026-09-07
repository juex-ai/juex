package goal

import (
	"context"
	"reflect"
	"strings"
	"testing"

	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func TestGoalToolDefinitionsBindThreadStateGroup(t *testing.T) {
	reg := toolcore.NewRegistry()
	store := NewGoalStateStore(t.TempDir(), GoalStateOptions{})
	installModuleTools(t, reg, New(store))
	definitions := ToolDefinitions()
	if len(definitions) != 3 {
		t.Fatalf("definition count = %d, want 3", len(definitions))
	}
	for _, definition := range definitions {
		if definition.Group != toolcore.ToolGroupThreadState {
			t.Errorf("%s definition group = %q, want %q", definition.Name, definition.Group, toolcore.ToolGroupThreadState)
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

func TestGoalToolsCreateUpdateGetAndStayThreadScoped(t *testing.T) {
	reg := toolcore.NewRegistry()
	store := NewGoalStateStore(t.TempDir(), GoalStateOptions{})
	installModuleTools(t, reg, New(store))
	createTool, ok := reg.Get(ToolCreate)
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
	updateTool, ok := reg.Get(ToolUpdate)
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
	if !strings.Contains(strings.ToLower(updateTool.Description), "success requires acceptance") ||
		!strings.Contains(updateTool.Description, string(GoalStatusWaitForUser)) {
		t.Fatalf("update_goal description should explain completion and waiting: %q", updateTool.Description)
	}

	out, err := reg.Call(context.Background(), ToolGet, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"present":false`) {
		t.Fatalf("get before create = %s", out)
	}

	if _, err := reg.Call(context.Background(), ToolCreate, map[string]any{
		"description":   "finish the feature",
		"acceptance":    "command succeeds, docs/contract.md is updated, and go test ./... passes",
		"status_reason": "created from taskline spec",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Call(context.Background(), ToolUpdate, map[string]any{
		"status":        string(GoalStatusSuccess),
		"status_reason": "validated by tests",
	}); err != nil {
		t.Fatal(err)
	}
	out, err = reg.Call(context.Background(), ToolGet, nil)
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

	if _, err := reg.Call(context.Background(), ToolUpdate, map[string]any{
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
		t.Fatalf("goal leaked across threads: %+v", snapshot)
	}
}

func installModuleTools(t *testing.T, registry *toolcore.Registry, provider runtimemodule.ToolProvider) {
	t.Helper()
	provided, err := provider.Tools(t.Context(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range provided {
		if err := registry.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
}
