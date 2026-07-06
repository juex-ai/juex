package observable_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/tools"
)

func TestRegisterToolsAndDescriptions(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"observable_create",
		"observable_delete",
		"observable_list",
		"observable_observations",
		"observable_start",
		"observable_stop",
	}
	var got []string
	for _, tool := range reg.List() {
		got = append(got, tool.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	create, ok := reg.Get("observable_create")
	if !ok {
		t.Fatal("observable_create missing")
	}
	if !strings.Contains(create.Description, "Call observable_list before creating") || !strings.Contains(create.Description, "Deleting is permanent") {
		t.Fatalf("description = %q", create.Description)
	}
}

func TestObservableToolsCreateListDelete(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"id":      "lark-events",
		"command": "echo",
		"args":    []any{"hello"},
		"batch": map[string]any{
			"interval_seconds": float64(10),
			"max_chars":        float64(1000),
		},
	}
	if _, _, err := reg.CallWithInfo(context.Background(), "observable_create", input); err != nil {
		t.Fatal(err)
	}
	out, _, err := reg.CallWithInfo(context.Background(), "observable_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Observables []observable.ObservableStatus `json:"observables"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Observables) != 1 || listed.Observables[0].ID != "lark-events" {
		t.Fatalf("listed = %+v", listed)
	}
	if _, _, err := reg.CallWithInfo(context.Background(), "observable_delete", map[string]any{"id": "lark-events"}); err != nil {
		t.Fatal(err)
	}
	out, _, err = reg.CallWithInfo(context.Background(), "observable_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "lark-events") {
		t.Fatalf("list after delete = %s", out)
	}
}

func TestObservableToolsObservations(t *testing.T) {
	mgr := newToolTestManager(t)
	rec, err := mgr.RecordObservation(observation("lark-events", "hello", fixedTime))
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	out, _, err := reg.CallWithInfo(context.Background(), "observable_observations", map[string]any{
		"id":    "lark-events",
		"limit": float64(5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, rec.ID) || !strings.Contains(out, "hello") {
		t.Fatalf("observations output = %s", out)
	}
}

func TestObservableToolsObservationsBoundsLimit(t *testing.T) {
	mgr := newToolTestManager(t)
	for i := 0; i < 105; i++ {
		_, err := mgr.RecordObservation(observation("lark-events", fmt.Sprintf("event-%03d", i), fixedTime.Add(time.Duration(i)*time.Second)))
		if err != nil {
			t.Fatal(err)
		}
	}
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	out, _, err := reg.CallWithInfo(context.Background(), "observable_observations", map[string]any{
		"id": "lark-events",
	})
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Observations []observable.ObservationRecord `json:"observations"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Observations) != 20 {
		t.Fatalf("default observations len = %d, want 20", len(listed.Observations))
	}
	out, _, err = reg.CallWithInfo(context.Background(), "observable_observations", map[string]any{
		"id":    "lark-events",
		"limit": float64(1000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Observations) != 100 {
		t.Fatalf("capped observations len = %d, want 100", len(listed.Observations))
	}
}

func newToolTestManager(t *testing.T) *observable.Manager {
	t.Helper()
	dir := t.TempDir()
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}
