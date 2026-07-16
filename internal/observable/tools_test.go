package observable_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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
	definitions := observable.ToolDefinitions()
	if len(definitions) != len(want) {
		t.Fatalf("definition count = %d, want %d", len(definitions), len(want))
	}
	for _, definition := range definitions {
		if definition.Group != tools.ToolGroupObservable {
			t.Errorf("%s definition group = %q, want %q", definition.Name, definition.Group, tools.ToolGroupObservable)
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
	create, ok := reg.Get("observable_create")
	if !ok {
		t.Fatal("observable_create missing")
	}
	if !strings.Contains(create.Description, "Call observable_list first") ||
		!strings.Contains(create.Description, "batch defaults are safe") ||
		!strings.Contains(create.Description, "schedule sources do not run commands") {
		t.Fatalf("description = %q", create.Description)
	}
}

func TestObservableCreateSchemaGuidesSingleSourceShape(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	create, ok := reg.Get("observable_create")
	if !ok {
		t.Fatal("observable_create missing")
	}
	if got := create.Schema["additionalProperties"]; got != false {
		t.Fatalf("create schema additionalProperties = %v, want false", got)
	}
	props := schemaMap(t, create.Schema, "properties")
	for _, legacy := range []string{"command", "args", "cwd", "env", "streams", "defaults", "parser", "filters", "batch", "on_exit"} {
		if _, ok := props[legacy]; ok {
			t.Fatalf("create schema exposes legacy top-level %q: %#v", legacy, props[legacy])
		}
	}
	source := schemaMapFromValue(t, props["source"])
	oneOf, ok := source["oneOf"].([]any)
	if !ok || len(oneOf) != 2 {
		t.Fatalf("source oneOf = %#v, want two source shapes", source["oneOf"])
	}
	command := schemaMapFromValue(t, oneOf[0])
	schedule := schemaMapFromValue(t, oneOf[1])
	if command["additionalProperties"] != false || schedule["additionalProperties"] != false {
		t.Fatalf("source shapes must be closed: command=%v schedule=%v", command["additionalProperties"], schedule["additionalProperties"])
	}
	commandProps := schemaMap(t, command, "properties")
	if _, ok := commandProps["batch"]; !ok {
		t.Fatalf("command source missing batch property: %#v", commandProps)
	}
	if _, ok := commandProps["interval"]; ok {
		t.Fatalf("command source exposes schedule interval: %#v", commandProps["interval"])
	}
	for _, name := range []string{"parser", "batch", "on_exit"} {
		if schemaMapFromValue(t, commandProps[name])["additionalProperties"] != false {
			t.Fatalf("%s schema is open: %#v", name, commandProps[name])
		}
	}
	filters := schemaMapFromValue(t, commandProps["filters"])
	filter := schemaMapFromValue(t, filters["items"])
	if filter["additionalProperties"] != false {
		t.Fatalf("filter item schema is open: %#v", filters["items"])
	}
	if oneOf, ok := filter["oneOf"].([]any); !ok || len(oneOf) != 2 {
		t.Fatalf("filter item oneOf = %#v, want contains/regex alternatives", filter["oneOf"])
	}
	scheduleProps := schemaMap(t, schedule, "properties")
	if _, ok := scheduleProps["command"]; ok {
		t.Fatalf("schedule source exposes command: %#v", scheduleProps["command"])
	}
	if oneOf, ok := schedule["oneOf"].([]any); !ok || len(oneOf) != 3 {
		t.Fatalf("schedule source oneOf = %#v, want once/daily/interval alternatives", schedule["oneOf"])
	} else {
		dailyBranch := schemaMapFromValue(t, oneOf[1])
		req, ok := dailyBranch["required"].([]any)
		if !ok || len(req) != 2 || req[0] != "daily" || req[1] != "timezone" {
			t.Fatalf("schedule daily branch required = %#v, want daily and timezone", req)
		}
	}
	for name, required := range map[string]string{
		"once":     "at",
		"daily":    "times",
		"interval": "every_seconds",
	} {
		req, ok := schemaMapFromValue(t, scheduleProps[name])["required"].([]any)
		if !ok || len(req) != 1 || req[0] != required {
			t.Fatalf("%s required = %#v, want %q", name, req, required)
		}
	}
	if schemaMapFromValue(t, scheduleProps["interval"])["additionalProperties"] != false {
		t.Fatalf("interval schema is open: %#v", scheduleProps["interval"])
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

func TestObservableToolsCreateScheduleSource(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"id": "weekday-brief",
		"source": map[string]any{
			"type":     "schedule",
			"timezone": "Asia/Shanghai",
			"daily": map[string]any{
				"times":    []any{"09:00"},
				"weekdays": []any{"mon", "tue", "wed", "thu", "fri"},
			},
			"catch_up": map[string]any{
				"mode":                 "latest",
				"max_lateness_minutes": float64(120),
			},
		},
		"observation": map[string]any{
			"kind":     "heartbeat",
			"severity": "info",
			"content":  "Prepare a concise work brief.",
		},
	}
	out, _, err := reg.CallWithInfo(context.Background(), "observable_create", input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"source_type": "schedule"`) {
		t.Fatalf("create schedule output = %s", out)
	}
}

func TestObservableToolsCreateCommandSourceDefaultsBatch(t *testing.T) {
	mgr, config := newToolTestManagerWithConfigPath(t)
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"id": "lark-events",
		"source": map[string]any{
			"type":    "command",
			"command": "echo",
			"args":    []any{"hello"},
		},
	}
	out, _, err := reg.CallWithInfo(context.Background(), "observable_create", input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"source_type": "command"`) {
		t.Fatalf("create command output = %s", out)
	}
	cfg, err := observable.LoadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Observables) != 1 {
		t.Fatalf("observables = %+v, want one", cfg.Observables)
	}
	got := cfg.Observables[0]
	commandConfig, ok := got.CommandConfig()
	if !ok || commandConfig.Batch.IntervalSeconds != observable.DefaultBatchIntervalSeconds ||
		commandConfig.Batch.MaxChars != observable.DefaultBatchMaxChars {
		body, _ := json.MarshalIndent(cfg, "", "  ")
		t.Fatalf("persisted command config missing batch defaults: %s", body)
	}
}

func TestObservableToolsCreateRejectsAmbiguousSourceShapes(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name: "unknown source type",
			input: map[string]any{
				"id":     "unknown-source",
				"source": map[string]any{"type": "http", "command": "echo"},
			},
			want: "source.type must be command or schedule",
		},
		{
			name: "command source with schedule field",
			input: map[string]any{
				"id": "mixed-command",
				"source": map[string]any{
					"type": "command", "command": "echo",
					"interval": map[string]any{"every_seconds": float64(60)},
				},
			},
			want: "command source cannot set schedule fields",
		},
		{
			name: "schedule source with command field",
			input: map[string]any{
				"id": "mixed-schedule",
				"source": map[string]any{
					"type": "schedule", "command": "echo",
					"interval": map[string]any{"every_seconds": float64(60)},
				},
				"observation": map[string]any{"content": "tick"},
			},
			want: "schedule source cannot set command fields",
		},
		{
			name: "explicit source with top level command fields",
			input: map[string]any{
				"id": "mixed-levels", "command": "printf",
				"source": map[string]any{"type": "command", "command": "echo"},
			},
			want: "top-level command fields cannot be mixed with source",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newToolTestManager(t)
			reg := tools.NewRegistry()
			if err := observable.RegisterTools(reg, mgr); err != nil {
				t.Fatal(err)
			}
			if _, _, err := reg.CallWithInfo(context.Background(), "observable_create", tt.input); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("observable_create error = %v, want containing %q", err, tt.want)
			}
		})
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
	mgr, _ := newToolTestManagerWithConfigPath(t)
	return mgr
}

func newToolTestManagerWithConfigPath(t *testing.T) (*observable.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	config := configPath(dir)
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: config,
		StateDir:   stateDir(dir),
		WorkDir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr, config
}

func schemaMap(t *testing.T, schema map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := schema[key]
	if !ok {
		t.Fatalf("schema missing key %q: %#v", key, schema)
	}
	return schemaMapFromValue(t, value)
}

func schemaMapFromValue(t *testing.T, value any) map[string]any {
	t.Helper()
	schema, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema value = %#v, want map[string]any", value)
	}
	return schema
}
