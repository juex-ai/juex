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
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

func installObservableModuleTools(t *testing.T, registry *tools.Registry, manager *observable.Manager) {
	t.Helper()
	provided, err := observable.NewModule(manager).Tools(context.Background(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range provided {
		if err := registry.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRegisterToolsAndDescriptions(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
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
	if !strings.Contains(create.Description, "schedule_create") ||
		!strings.Contains(create.Description, `Guide available via skill_load("juex-observables").`) {
		t.Fatalf("description = %q", create.Description)
	}
	schedule, ok := reg.Get("schedule_create")
	if !ok {
		t.Fatal("schedule_create missing")
	}
	if !strings.Contains(schedule.Description, "List first") ||
		!strings.Contains(schedule.Description, "reuse equivalents") ||
		!strings.Contains(schedule.Description, "never probe") ||
		!strings.Contains(schedule.Description, "command-poll") ||
		!strings.Contains(schedule.Description, `Guide available via skill_load("juex-observables").`) {
		t.Fatalf("schedule description = %q", schedule.Description)
	}
	var providerScheduleDescription string
	for _, spec := range reg.Specs() {
		if spec.Name == "schedule_create" {
			providerScheduleDescription = spec.Description
		}
	}
	if !strings.Contains(providerScheduleDescription, `Guide available via skill_load("juex-observables").`) {
		t.Fatalf("provider-visible schedule_create description = %q", providerScheduleDescription)
	}
}

func TestCreateToolSchemasAreClosedAndSourceSpecific(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
	create, ok := reg.Get("observable_create")
	if !ok {
		t.Fatal("observable_create missing")
	}
	if got := create.Schema["additionalProperties"]; got != false {
		t.Fatalf("observable_create additionalProperties = %v, want false", got)
	}
	if got := schemaRequiredStrings(t, create.Schema); !reflect.DeepEqual(got, []string{"command"}) {
		t.Fatalf("observable_create required = %v, want command only so name can derive id", got)
	}
	commandProps := schemaMap(t, create.Schema, "properties")
	for _, required := range []string{"id", "command", "args", "cwd", "env", "streams", "parser", "filters", "batch", "on_exit", "observation"} {
		if _, ok := commandProps[required]; !ok {
			t.Fatalf("observable_create missing command field %q", required)
		}
	}
	for _, forbidden := range []string{"source", "type", "timezone", "once", "daily", "monthly", "interval", "catch_up", "content", "attachments", "command_config", "schedule_config"} {
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
	if got := schemaRequiredStrings(t, schedule.Schema); !reflect.DeepEqual(got, []string{"observation"}) {
		t.Fatalf("schedule_create required = %v, want observation only so name can derive id", got)
	}
	if _, ok := schedule.Schema["description"]; ok {
		t.Fatalf("schedule schema should move prose to builtin guide: %#v", schedule.Schema)
	}
	scheduleProps := schemaMap(t, schedule.Schema, "properties")
	for _, required := range []string{"id", "timezone", "once", "daily", "monthly", "interval", "catch_up", "observation"} {
		if _, ok := scheduleProps[required]; !ok {
			t.Fatalf("schedule_create missing schedule field %q", required)
		}
	}
	for _, forbidden := range []string{"source", "type", "command", "args", "cwd", "env", "streams", "parser", "filters", "batch", "on_exit", "command_config", "schedule_config"} {
		if _, ok := scheduleProps[forbidden]; ok {
			t.Fatalf("schedule_create exposes command field %q", forbidden)
		}
	}
	if oneOf, ok := schedule.Schema["oneOf"].([]any); !ok || len(oneOf) != 4 {
		t.Fatalf("schedule_create oneOf = %#v, want once/daily/monthly/interval alternatives", schedule.Schema["oneOf"])
	} else {
		wantBranches := []struct {
			required []string
		}{
			{required: []string{"once"}},
			{required: []string{"daily"}},
			{required: []string{"monthly"}},
			{required: []string{"interval"}},
		}
		for i, want := range wantBranches {
			branch := schemaMapFromValue(t, oneOf[i])
			if got := schemaRequiredStrings(t, branch); !reflect.DeepEqual(got, want.required) {
				t.Fatalf("schedule branch %d required = %v, want %v", i, got, want.required)
			}
		}
		cases := []struct {
			name      string
			keys      []string
			wantValid bool
		}{
			{name: "once", keys: []string{"once"}, wantValid: true},
			{name: "daily", keys: []string{"daily", "timezone"}, wantValid: true},
			{name: "daily without timezone", keys: []string{"daily"}, wantValid: true},
			{name: "monthly", keys: []string{"monthly", "timezone"}, wantValid: true},
			{name: "monthly without timezone", keys: []string{"monthly"}, wantValid: true},
			{name: "interval", keys: []string{"interval"}, wantValid: true},
			{name: "once and daily without timezone", keys: []string{"once", "daily"}},
			{name: "once and daily", keys: []string{"once", "daily", "timezone"}},
			{name: "once and interval", keys: []string{"once", "interval"}},
			{name: "daily and interval without timezone", keys: []string{"daily", "interval"}},
			{name: "daily and interval", keys: []string{"daily", "interval", "timezone"}},
			{name: "monthly and interval", keys: []string{"monthly", "interval", "timezone"}},
			{name: "daily and monthly", keys: []string{"daily", "monthly", "timezone"}},
			{name: "all recurrences without timezone", keys: []string{"once", "daily", "monthly", "interval"}},
			{name: "all recurrences", keys: []string{"once", "daily", "monthly", "interval", "timezone"}},
		}
		for _, tt := range cases {
			t.Run("schedule schema "+tt.name, func(t *testing.T) {
				matches := schemaMatchingBranches(t, oneOf, tt.keys)
				if gotValid := matches == 1; gotValid != tt.wantValid {
					t.Fatalf("matching recurrence branches = %d, valid=%v, want valid=%v for keys %v", matches, gotValid, tt.wantValid, tt.keys)
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
	monthlyRequired, ok := schemaMapFromValue(t, scheduleProps["monthly"])["required"].([]any)
	if !ok || !reflect.DeepEqual(monthlyRequired, []any{"days", "times"}) {
		t.Fatalf("monthly required = %#v, want days and times", monthlyRequired)
	}
	if schemaMapFromValue(t, scheduleProps["interval"])["additionalProperties"] != false {
		t.Fatalf("interval schema is open: %#v", scheduleProps["interval"])
	}
	if schemaMapFromValue(t, scheduleProps["once"])["additionalProperties"] != false ||
		schemaMapFromValue(t, scheduleProps["daily"])["additionalProperties"] != false ||
		schemaMapFromValue(t, scheduleProps["monthly"])["additionalProperties"] != false ||
		schemaMapFromValue(t, scheduleProps["catch_up"])["additionalProperties"] != false {
		t.Fatal("schedule recurrence sub-schemas must be closed")
	}
}

func TestCreateToolSchemaCostsAreMeasuredWithoutOldUnion(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
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
	installObservableModuleTools(t, reg, mgr)
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
	installObservableModuleTools(t, reg, mgr)
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
	listed, _, err := reg.CallWithInfo(context.Background(), "observable_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"schedule_config"`,
		`"times": [`,
		`"09:00"`,
		`"content": "Prepare a concise work brief."`,
	} {
		if !strings.Contains(listed, want) {
			t.Fatalf("observable_list schedule missing %s: %s", want, listed)
		}
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

func TestScheduleCreatePersistsMonthlySpecAndListsStatus(t *testing.T) {
	mgr, config := newToolTestManagerWithConfigPath(t)
	reg := tools.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
	input := map[string]any{
		"id":       "monthly-brief",
		"timezone": "Asia/Shanghai",
		"monthly": map[string]any{
			"days":  []any{float64(1), float64(15), float64(31)},
			"times": []any{"09:00", "17:30"},
		},
		"observation": map[string]any{"content": "Prepare a monthly brief."},
	}
	out, _, err := reg.CallWithInfo(context.Background(), "schedule_create", input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"summary": "monthly days 1,15,31 at 09:00,17:30 Asia/Shanghai"`) {
		t.Fatalf("monthly create output = %s", out)
	}
	listed, _, err := reg.CallWithInfo(context.Background(), "observable_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"monthly"`, `"days": [`, `"timezone": "Asia/Shanghai"`, `"Prepare a monthly brief."`} {
		if !strings.Contains(listed, want) {
			t.Fatalf("observable_list monthly schedule missing %s: %s", want, listed)
		}
	}
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type": "schedule"`, `"monthly": {`, `"days": [`, `"times": [`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("persisted monthly schedule missing %s: %s", want, body)
		}
	}
}

func TestScheduleCreateDerivesIDFromName(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
	out, _, err := reg.CallWithInfo(context.Background(), "schedule_create", map[string]any{
		"name":     "Morning Brief!",
		"interval": map[string]any{"every_seconds": float64(3600)},
		"observation": map[string]any{
			"content": "Prepare the brief.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id": "morning-brief"`) {
		t.Fatalf("schedule_create output = %s, want slugged id", out)
	}
}

func TestObservableCreatePersistsTaggedSpecAndStartsCommand(t *testing.T) {
	mgr, config := newToolTestManagerWithConfigPath(t)
	reg := tools.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
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

func TestCreateHandlersRejectUnknownCrossSourceFields(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
	if _, _, err := reg.CallWithInfo(context.Background(), "schedule_create", map[string]any{
		"id": "bad-schedule", "command": "echo",
		"interval":    map[string]any{"every_seconds": float64(60)},
		"observation": map[string]any{"content": "tick"},
	}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("schedule_create command-field error = %v, want strict unknown field", err)
	}
	for _, test := range []struct {
		name  string
		input map[string]any
		field string
	}{
		{
			name: "nested command config",
			input: map[string]any{
				"id": "bad-command-config", "command": "echo",
				"command_config": map[string]any{"command": "echo"},
			},
			field: "command_config",
		},
		{
			name: "mixed schedule field",
			input: map[string]any{
				"id": "mixed-command", "command": "echo",
				"interval": map[string]any{"every_seconds": float64(60)},
			},
			field: "interval",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := reg.CallWithInfo(context.Background(), "observable_create", test.input)
			want := `json: unknown field "` + test.field + `"`
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("observable_create error = %v, want containing %q", err, want)
			}
		})
	}
}

func TestCreateHandlersRequireOneFilterAndRecurrenceBranch(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := tools.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
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
	installObservableModuleTools(t, reg, mgr)
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
	installObservableModuleTools(t, reg, mgr)
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
		if matched {
			matches++
		}
	}
	return matches
}
