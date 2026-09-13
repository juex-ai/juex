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

	observable "github.com/juex-ai/juex/internal/features/observables"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func installObservableModuleTools(t *testing.T, registry *toolcore.Registry, manager *observable.Manager) {
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
	reg := toolcore.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
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
		if definition.Group != toolcore.ToolGroupObservable {
			t.Errorf("%s definition group = %q, want %q", definition.Name, definition.Group, toolcore.ToolGroupObservable)
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
	if !strings.Contains(create.Description, "command Observable") {
		t.Fatal(create.Description)
	}
}

func TestCreateToolSchemasAreClosedAndSourceSpecific(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := toolcore.NewRegistry()
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
	for _, forbidden := range []string{"source", "type", "content", "attachments", "command_config"} {
		if _, ok := commandProps[forbidden]; ok {
			t.Fatalf("observable_create exposes cross-source field %q", forbidden)
		}
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

}

func TestObservableToolsCreateListDelete(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := toolcore.NewRegistry()
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

func TestObservableCreatePersistsTaggedSpecAndStartsCommand(t *testing.T) {
	mgr, config := newToolTestManagerWithConfigPath(t)
	reg := toolcore.NewRegistry()
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
}

func TestCreateHandlersRejectUnknownFields(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := toolcore.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
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
			name: "unknown field",
			input: map[string]any{
				"id": "mixed-command", "command": "echo",
				"unexpected": true,
			},
			field: "unexpected",
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

func TestCreateHandlersRequireOneFilterPredicate(t *testing.T) {
	mgr := newToolTestManager(t)
	reg := toolcore.NewRegistry()
	installObservableModuleTools(t, reg, mgr)
	if _, _, err := reg.CallWithInfo(context.Background(), "observable_create", map[string]any{
		"id": "bad-filter", "command": "echo",
		"filters": []any{map[string]any{"contains": "ok", "regex": "ok"}},
	}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("observable_create filter error = %v, want exactly one predicate", err)
	}
}

func TestObservableToolsObservations(t *testing.T) {
	mgr := newToolTestManager(t)
	rec, err := mgr.RecordObservation(observation("lark-events", "hello", fixedTime))
	if err != nil {
		t.Fatal(err)
	}
	reg := toolcore.NewRegistry()
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
	reg := toolcore.NewRegistry()
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
