package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/tools"
)

func TestGoalToolsCreateUpdateGetAndStaySessionScoped(t *testing.T) {
	reg := tools.NewRegistry()
	eng := &Engine{Tools: reg, GoalState: NewGoalStateStore(t.TempDir(), GoalStateOptions{})}
	if err := RegisterGoalTools(reg, eng); err != nil {
		t.Fatal(err)
	}

	out, err := reg.Call(context.Background(), GoalToolGet, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"present":false`) {
		t.Fatalf("get before create = %s", out)
	}

	if _, err := reg.Call(context.Background(), GoalToolCreate, map[string]any{
		"description":         "finish the feature",
		"verification_method": "go test ./...",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Call(context.Background(), GoalToolUpdate, map[string]any{
		"status": string(GoalStatusSuccess),
	}); err != nil {
		t.Fatal(err)
	}
	out, err = reg.Call(context.Background(), GoalToolGet, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"present":true`, `"description":"finish the feature"`, `"status":"success"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("get missing %s:\n%s", want, out)
		}
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
