package observable_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
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
		"schedule_create",
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
		!strings.Contains(create.Description, "schedule_create") {
		t.Fatalf("description = %q", create.Description)
	}
	schedule, ok := reg.Get("schedule_create")
	if !ok {
		t.Fatal("schedule_create missing")
	}
	if !strings.Contains(schedule.Description, "scheduled") ||
		!strings.Contains(schedule.Description, "polling") ||
		!strings.Contains(schedule.Description, "exactly one of once, daily, or interval") ||
		!strings.Contains(schedule.Description, "daily requires timezone") {
		t.Fatalf("schedule description = %q", schedule.Description)
	}
	var providerScheduleDescription string
	for _, spec := range reg.Specs() {
		if spec.Name == "schedule_create" {
			providerScheduleDescription = spec.Description
		}
	}
	if !strings.Contains(providerScheduleDescription, "exactly one of once, daily, or interval") ||
		!strings.Contains(providerScheduleDescription, "daily requires timezone") {
		t.Fatalf("provider-visible schedule_create description = %q", providerScheduleDescription)
	}
}

func TestCreateToolSchemasAreClosedAndSourceSpecific(t *testing.T) {
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
		t.Fatalf("observable_create additionalProperties = %v, want false", got)
	}
	commandProps := schemaMap(t, create.Schema, "properties")
	for _, required := range []string{"id", "command", "args", "cwd", "env", "streams", "parser", "filters", "batch", "on_exit", "observation"} {
		if _, ok := commandProps[required]; !ok {
			t.Fatalf("observable_create missing command field %q", required)
		}
	}
	for _, forbidden := range []string{"source", "type", "timezone", "once", "daily", "interval", "catch_up", "content", "attachments", "command_config", "schedule_config"} {
		if _, ok := commandProps[forbidden]; ok {
			t.Fatalf("observable_create exposes cross-source field %q", forbidden)
		}
	}
	if _, ok := create.Schema["oneOf"]; ok {
		t.Fatalf("observable_create retains old source union: %#v", create.Schema["oneOf"])
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
	commandObservation := schemaMapFromValue(t, commandProps["observation"])
	commandObservationProps := schemaMap(t, commandObservation, "properties")
	for _, forbidden := range []string{"content", "attachments"} {
		if _, ok := commandObservationProps[forbidden]; ok {
			t.Fatalf("command observation exposes %q", forbidden)
		}
	}

	schedule, ok := reg.Get("schedule_create")
	if !ok {
		t.Fatal("schedule_create missing")
	}
	if schedule.Schema["additionalProperties"] != false {
		t.Fatalf("schedule_create additionalProperties = %#v, want false", schedule.Schema["additionalProperties"])
	}
	schemaDescription, _ := schedule.Schema["description"].(string)
	if !strings.Contains(schemaDescription, "exactly one of once, daily, or interval") ||
		!strings.Contains(schemaDescription, "daily requires timezone") {
		t.Fatalf("provider-visible schedule schema description = %q", schemaDescription)
	}
	scheduleProps := schemaMap(t, schedule.Schema, "properties")
	for _, required := range []string{"id", "timezone", "once", "daily", "interval", "catch_up", "observation"} {
		if _, ok := scheduleProps[required]; !ok {
			t.Fatalf("schedule_create missing schedule field %q", required)
		}
	}
	for _, forbidden := range []string{"source", "type", "command", "args", "cwd", "env", "streams", "parser", "filters", "batch", "on_exit", "command_config", "schedule_config"} {
		if _, ok := scheduleProps[forbidden]; ok {
			t.Fatalf("schedule_create exposes command field %q", forbidden)
		}
	}
	if oneOf, ok := schedule.Schema["oneOf"].([]any); !ok || len(oneOf) != 3 {
		t.Fatalf("schedule_create oneOf = %#v, want once/daily/interval alternatives", schedule.Schema["oneOf"])
	} else {
		wantBranches := []struct {
			required  []string
			forbidden []string
		}{
			{required: []string{"once"}, forbidden: []string{"daily", "interval"}},
			{required: []string{"daily", "timezone"}, forbidden: []string{"once", "interval"}},
			{required: []string{"interval"}, forbidden: []string{"once", "daily"}},
		}
		for i, want := range wantBranches {
			branch := schemaMapFromValue(t, oneOf[i])
			if got := schemaRequiredStrings(t, branch); !reflect.DeepEqual(got, want.required) {
				t.Fatalf("schedule branch %d required = %v, want %v", i, got, want.required)
			}
			if got := schemaForbiddenRequiredStrings(t, branch); !reflect.DeepEqual(got, want.forbidden) {
				t.Fatalf("schedule branch %d forbidden recurrence fields = %v, want %v", i, got, want.forbidden)
			}
		}
		cases := []struct {
			name string
			keys []string
			want int
		}{
			{name: "once", keys: []string{"once"}, want: 1},
			{name: "daily", keys: []string{"daily", "timezone"}, want: 1},
			{name: "interval", keys: []string{"interval"}, want: 1},
			{name: "once and daily without timezone", keys: []string{"once", "daily"}, want: 0},
			{name: "once and daily", keys: []string{"once", "daily", "timezone"}, want: 0},
			{name: "once and interval", keys: []string{"once", "interval"}, want: 0},
			{name: "daily and interval without timezone", keys: []string{"daily", "interval"}, want: 0},
			{name: "daily and interval", keys: []string{"daily", "interval", "timezone"}, want: 0},
			{name: "all recurrences without timezone", keys: []string{"once", "daily", "interval"}, want: 0},
			{name: "all recurrences", keys: []string{"once", "daily", "interval", "timezone"}, want: 0},
		}
		for _, tt := range cases {
			t.Run("schedule schema "+tt.name, func(t *testing.T) {
				if got := schemaMatchingBranches(t, oneOf, tt.keys); got != tt.want {
					t.Fatalf("matching recurrence branches = %d, want %d for keys %v", got, tt.want, tt.keys)
				}
			})
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
	if schemaMapFromValue(t, scheduleProps["once"])["additionalProperties"] != false ||
		schemaMapFromValue(t, scheduleProps["daily"])["additionalProperties"] != false ||
		schemaMapFromValue(t, scheduleProps["catch_up"])["additionalProperties"] != false {
		t.Fatal("schedule recurrence sub-schemas must be closed")
	}
}

func TestCreateToolSchemaCostsAreMeasuredWithoutOldUnion(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	var commandTokens, scheduleTokens int
	for _, spec := range reg.Specs() {
		switch spec.Name {
		case "observable_create":
			commandTokens = contextbudget.EstimateToolTokens([]llm.ToolSpec{spec})
		case "schedule_create":
			scheduleTokens = contextbudget.EstimateToolTokens([]llm.ToolSpec{spec})
		}
	}
	if commandTokens <= 0 || scheduleTokens <= 0 {
		t.Fatalf("create schema token estimates = command:%d schedule:%d", commandTokens, scheduleTokens)
	}
	t.Logf("create schema token estimates: observable_create=%d schedule_create=%d delta=%d", commandTokens, scheduleTokens, scheduleTokens-commandTokens)
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

func TestScheduleCreatePersistsTaggedSpecAndStartsSchedule(t *testing.T) {
	mgr, config := newToolTestManagerWithConfigPath(t)
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"id":       "weekday-brief",
		"timezone": "Asia/Shanghai",
		"daily": map[string]any{
			"times":    []any{"09:00"},
			"weekdays": []any{"mon", "tue", "wed", "thu", "fri"},
		},
		"catch_up": map[string]any{
			"mode":                 "latest",
			"max_lateness_minutes": float64(120),
		},
		"observation": map[string]any{
			"kind":     "heartbeat",
			"severity": "info",
			"content":  "Prepare a concise work brief.",
		},
	}
	out, _, err := reg.CallWithInfo(context.Background(), "schedule_create", input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"source_type": "schedule"`) {
		t.Fatalf("create schedule output = %s", out)
	}
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{`"type": "schedule"`, `"schedule_config"`, `"content": "Prepare a concise work brief."`} {
		if !strings.Contains(text, want) {
			t.Fatalf("persisted schedule missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, `"command_config"`) || strings.Contains(text, `"source"`) {
		t.Fatalf("persisted schedule contains cross-source shape: %s", text)
	}
}

func TestObservableCreatePersistsTaggedSpecAndStartsCommand(t *testing.T) {
	mgr, config := newToolTestManagerWithConfigPath(t)
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"id":      "lark-events",
		"command": "echo",
		"args":    []any{"hello"},
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
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"type": "command"`) || !strings.Contains(text, `"command_config"`) {
		t.Fatalf("persisted command is not tagged: %s", text)
	}
	if strings.Contains(text, `"schedule_config"`) || strings.Contains(text, `"source"`) {
		t.Fatalf("persisted command contains cross-source shape: %s", text)
	}
}

func TestObservableCreateRoutesLegacyAndScheduleShapesWithoutMisleading(t *testing.T) {
	tests := []struct {
		name    string
		input   map[string]any
		want    string
		notWant string
	}{
		{
			name: "nested command source",
			input: map[string]any{
				"id": "nested-command",
				"source": map[string]any{
					"type": "command", "command": "echo",
				},
			},
			want:    "flat command input",
			notWant: "schedule_create",
		},
		{
			name: "nested command fields without discriminator",
			input: map[string]any{
				"id":     "nested-command",
				"source": map[string]any{"command": "echo"},
			},
			want:    "flat command input",
			notWant: "schedule_create",
		},
		{
			name: "top level command discriminator",
			input: map[string]any{
				"id": "tagged-command", "type": "command", "command": "echo",
			},
			want:    "flat command input",
			notWant: "schedule_create",
		},
		{
			name: "nested command config",
			input: map[string]any{
				"id": "config-command", "command_config": map[string]any{"command": "echo"},
			},
			want:    "flat command input",
			notWant: "schedule_create",
		},
		{
			name: "nested schedule source",
			input: map[string]any{
				"id": "nested-schedule",
				"source": map[string]any{
					"type":     "schedule",
					"interval": map[string]any{"every_seconds": float64(60)},
				},
				"observation": map[string]any{"content": "tick"},
			},
			want: "schedule_create",
		},
		{
			name: "top level schedule discriminator",
			input: map[string]any{
				"id": "tagged-schedule", "type": "schedule",
			},
			want: "schedule_create",
		},
		{
			name: "flat schedule recurrence",
			input: map[string]any{
				"id":          "flat-schedule",
				"interval":    map[string]any{"every_seconds": float64(60)},
				"observation": map[string]any{"content": "tick"},
			},
			want: "schedule_create",
		},
		{
			name: "schedule observation content",
			input: map[string]any{
				"id":          "schedule-content",
				"command":     "echo",
				"observation": map[string]any{"content": "tick"},
			},
			want: "schedule_create",
		},
		{
			name: "schedule observation attachments",
			input: map[string]any{
				"id": "schedule-attachments",
				"observation": map[string]any{
					"attachments": []any{map[string]any{"path": "brief.md"}},
				},
			},
			want: "schedule_create",
		},
		{
			name: "legacy tagged persisted shape",
			input: map[string]any{
				"id": "legacy-tagged", "type": "schedule",
				"schedule_config": map[string]any{
					"interval":    map[string]any{"every_seconds": float64(60)},
					"observation": map[string]any{"content": "tick"},
				},
			},
			want: "schedule_create",
		},
		{
			name: "unknown discriminator",
			input: map[string]any{
				"id": "unknown-source", "source": map[string]any{"type": "http"},
			},
			want:    "unknown source discriminator",
			notWant: "schedule_create",
		},
		{
			name: "mixed discriminators",
			input: map[string]any{
				"id": "mixed-source", "type": "command",
				"source": map[string]any{"type": "schedule"},
			},
			want:    "mixed command and schedule",
			notWant: "schedule_create",
		},
		{
			name: "command discriminator with schedule config",
			input: map[string]any{
				"id": "mixed-config", "type": "command",
				"schedule_config": map[string]any{"interval": map[string]any{"every_seconds": float64(60)}},
			},
			want:    "mixed command and schedule",
			notWant: "schedule_create",
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
			} else if tt.notWant != "" && strings.Contains(err.Error(), tt.notWant) {
				t.Fatalf("observable_create error = %v, must not contain misleading %q", err, tt.notWant)
			}
		})
	}
}

func TestCreateHandlersRejectUnknownCrossSourceFields(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.CallWithInfo(context.Background(), "schedule_create", map[string]any{
		"id": "bad-schedule", "command": "echo",
		"interval":    map[string]any{"every_seconds": float64(60)},
		"observation": map[string]any{"content": "tick"},
	}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("schedule_create command-field error = %v, want strict unknown field", err)
	}
	if _, _, err := reg.CallWithInfo(context.Background(), "observable_create", map[string]any{
		"id": "bad-command", "command": "echo", "mystery": true,
	}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("observable_create unknown-field error = %v, want strict unknown field", err)
	}
}

func TestCreateHandlersRequireOneFilterAndRecurrenceBranch(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	if err := observable.RegisterTools(reg, mgr); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.CallWithInfo(context.Background(), "observable_create", map[string]any{
		"id": "bad-filter", "command": "echo",
		"filters": []any{map[string]any{"contains": "ok", "regex": "ok"}},
	}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("observable_create filter error = %v, want exactly one predicate", err)
	}
	if _, _, err := reg.CallWithInfo(context.Background(), "schedule_create", map[string]any{
		"id":       "bad-recurrence",
		"once":     map[string]any{"at": "2030-01-01T00:00:00Z"},
		"interval": map[string]any{"every_seconds": float64(60)},
		"observation": map[string]any{
			"content": "tick",
		},
	}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("schedule_create recurrence error = %v, want exactly one recurrence", err)
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

func schemaRequiredStrings(t *testing.T, schema map[string]any) []string {
	t.Helper()
	values, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema required = %#v, want []any", schema["required"])
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("schema required value = %#v, want string", value)
		}
		result = append(result, text)
	}
	return result
}

func schemaForbiddenRequiredStrings(t *testing.T, schema map[string]any) []string {
	t.Helper()
	notSchema := schemaMapFromValue(t, schema["not"])
	branches, ok := notSchema["anyOf"].([]any)
	if !ok {
		t.Fatalf("schema not.anyOf = %#v, want []any", notSchema["anyOf"])
	}
	result := make([]string, 0, len(branches))
	for _, value := range branches {
		required := schemaRequiredStrings(t, schemaMapFromValue(t, value))
		if len(required) != 1 {
			t.Fatalf("forbidden branch required = %v, want one field", required)
		}
		result = append(result, required[0])
	}
	return result
}

func schemaMatchingBranches(t *testing.T, branches []any, keys []string) int {
	t.Helper()
	present := make(map[string]bool, len(keys))
	for _, key := range keys {
		present[key] = true
	}
	matches := 0
	for _, value := range branches {
		branch := schemaMapFromValue(t, value)
		matched := true
		for _, required := range schemaRequiredStrings(t, branch) {
			matched = matched && present[required]
		}
		if forbidden, ok := branch["not"]; ok {
			for _, field := range schemaForbiddenRequiredStrings(t, map[string]any{"not": forbidden}) {
				matched = matched && !present[field]
			}
		}
		if matched {
			matches++
		}
	}
	return matches
}
