package observable_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/observable"
)

func TestLoadConfigMissingFileReturnsEmpty(t *testing.T) {
	cfg, err := observable.LoadConfig(filepath.Join(t.TempDir(), "observables.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Observables) != 0 {
		t.Fatalf("observables = %+v, want empty", cfg.Observables)
	}
}

func TestTaggedSpecsRoundTripAndDefensivelyCopy(t *testing.T) {
	commandInput := observable.CommandSourceSpec{
		Command:     "juex-observable-test",
		Args:        []string{"watch"},
		Env:         map[string]string{"TOKEN": "secret"},
		Parser:      &observable.ParserSpec{Type: observable.ParserJSONL, ContentField: "content"},
		Batch:       observable.BatchSpec{IntervalSeconds: 10, MaxChars: 1000},
		Observation: observable.CommandObservationSpec{Kind: "event", Severity: "warning"},
	}
	command, err := observable.NewCommandSpec("command", "Command", commandInput)
	if err != nil {
		t.Fatal(err)
	}
	commandInput.Args[0] = "mutated"
	commandInput.Env["TOKEN"] = "mutated"
	commandInput.Parser.ContentField = "mutated"
	gotCommand, ok := command.CommandConfig()
	if !ok || gotCommand.Args[0] != "watch" || gotCommand.Env["TOKEN"] != "secret" || gotCommand.Parser.ContentField != "content" {
		t.Fatalf("command config = %+v, ok = %v", gotCommand, ok)
	}
	gotCommand.Args[0] = "again"
	if again, _ := command.CommandConfig(); again.Args[0] != "watch" {
		t.Fatalf("CommandConfig leaked mutable state: %+v", again)
	}

	scheduleInput := observable.ScheduleSourceSpec{
		Timezone:    "Asia/Shanghai",
		Daily:       &observable.DailySchedule{Times: []string{"09:00"}, Weekdays: []string{"mon"}},
		CatchUp:     observable.CatchUpSpec{Mode: observable.ScheduleCatchUpLatest, MaxLatenessMinutes: 60},
		Observation: observable.ScheduleObservationSpec{Content: "Prepare brief", Attachments: []observable.AttachmentSpec{{Path: "brief.png"}}},
	}
	schedule, err := observable.NewScheduleSpec("schedule", "Schedule", scheduleInput)
	if err != nil {
		t.Fatal(err)
	}
	scheduleInput.Daily.Times[0] = "10:00"
	scheduleInput.Observation.Attachments[0].Path = "mutated.png"
	gotSchedule, ok := schedule.ScheduleConfig()
	if !ok || gotSchedule.Daily.Times[0] != "09:00" || gotSchedule.Observation.Attachments[0].Path != "brief.png" {
		t.Fatalf("schedule config = %+v, ok = %v", gotSchedule, ok)
	}
	gotSchedule.Daily.Times[0] = "11:00"
	if again, _ := schedule.ScheduleConfig(); again.Daily.Times[0] != "09:00" {
		t.Fatalf("ScheduleConfig leaked mutable state: %+v", again)
	}
	if command.SourceType() != observable.SourceTypeCommand || schedule.SourceType() != observable.SourceTypeSchedule {
		t.Fatalf("source types = %q, %q", command.SourceType(), schedule.SourceType())
	}

	path := filepath.Join(t.TempDir(), "observables.json")
	if err := observable.SaveConfig(path, observable.FileConfig{Observables: []observable.Spec{command, schedule}}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{`"type": "command"`, `"command_config": {`, `"type": "schedule"`, `"schedule_config": {`} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved JSON missing %s:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{`"source":`, `"command_config": null`, `"schedule_config": null`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("saved JSON contains %s:\n%s", unwanted, text)
		}
	}
	loaded, err := observable.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Observables, []observable.Spec{command, schedule}) {
		t.Fatalf("round trip = %#v, want %#v", loaded.Observables, []observable.Spec{command, schedule})
	}
}

func TestLoadConfigLenientReportsLegacyEntriesAndKeepsValidSibling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observables.json")
	body := `{"observables":[` +
		`{"id":"old-flat","command":"echo"},` +
		`{"id":"old-source","source":{"type":"schedule","interval":{"every_seconds":60}},"observation":{"content":"tick"}},` +
		`{"id":"valid","type":"command","command_config":{"command":"echo"}}` +
		`]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, issues, err := observable.LoadConfigLenient(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Observables) != 1 || cfg.Observables[0].ID != "valid" {
		t.Fatalf("valid siblings = %+v", cfg.Observables)
	}
	if len(issues) != 2 {
		t.Fatalf("issues = %+v, want two", issues)
	}
	for _, issue := range issues {
		if !strings.Contains(issue.Error.Error(), issue.ID) ||
			(!strings.Contains(issue.Error.Error(), "type plus command_config") && !strings.Contains(issue.Error.Error(), "type plus schedule_config")) {
			t.Fatalf("issue %q lacks id/rewrite hint: %v", issue.ID, issue.Error)
		}
	}
}

func TestLoadConfigRejectsTaggedWireShapeMismatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "command with schedule config", body: `{"observables":[{"id":"bad","type":"command","schedule_config":{"interval":{"every_seconds":60},"observation":{"content":"tick"}}}]}`},
		{name: "both configs", body: `{"observables":[{"id":"bad","type":"command","command_config":{"command":"echo"},"schedule_config":{"interval":{"every_seconds":60},"observation":{"content":"tick"}}}]}`},
		{name: "unknown top level field", body: `{"observables":[{"id":"bad","type":"command","command_config":{"command":"echo"},"extra":true}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "observables.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := observable.LoadConfig(path); err == nil {
				t.Fatal("LoadConfig() error = nil")
			}
		})
	}
}

func TestValidateSpecSourceLocalDefaults(t *testing.T) {
	command, err := observable.NewCommandSpec("command", "", observable.CommandSourceSpec{Command: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	commandConfig, _ := command.CommandConfig()
	if !reflect.DeepEqual(commandConfig.Streams, []string{observable.StreamStdout, observable.StreamStderr}) ||
		commandConfig.Batch.IntervalSeconds != observable.DefaultBatchIntervalSeconds ||
		commandConfig.Batch.MaxChars != observable.DefaultBatchMaxChars {
		t.Fatalf("command defaults = %+v", commandConfig)
	}
	schedule, err := observable.NewScheduleSpec("schedule", "", observable.ScheduleSourceSpec{
		Interval:    &observable.IntervalSchedule{EverySeconds: 60},
		Observation: observable.ScheduleObservationSpec{Content: "check"},
	})
	if err != nil {
		t.Fatal(err)
	}
	scheduleConfig, _ := schedule.ScheduleConfig()
	if scheduleConfig.Observation.Kind != observable.DefaultScheduleKind ||
		scheduleConfig.Observation.Severity != observable.DefaultSeverity ||
		scheduleConfig.CatchUp.Mode != observable.ScheduleCatchUpNone {
		t.Fatalf("schedule defaults = %+v", scheduleConfig)
	}
}

func TestValidateSpecSourceLocalFailures(t *testing.T) {
	commandCases := []struct {
		name   string
		config observable.CommandSourceSpec
		want   string
	}{
		{name: "missing command", config: observable.CommandSourceSpec{}, want: "command"},
		{name: "invalid stream", config: observable.CommandSourceSpec{Command: "echo", Streams: []string{"stdin"}}, want: "stream"},
		{name: "invalid severity", config: observable.CommandSourceSpec{Command: "echo", Observation: observable.CommandObservationSpec{Severity: "urgent"}}, want: "severity"},
		{name: "small batch", config: observable.CommandSourceSpec{Command: "echo", Batch: observable.BatchSpec{IntervalSeconds: 4, MaxChars: 1}}, want: "interval"},
		{name: "large batch", config: observable.CommandSourceSpec{Command: "echo", Batch: observable.BatchSpec{IntervalSeconds: 5, MaxChars: 1001}}, want: "max_chars"},
		{name: "empty filter", config: observable.CommandSourceSpec{Command: "echo", Filters: []observable.FilterSpec{{Kind: "event"}}}, want: "filter"},
		{name: "double filter", config: observable.CommandSourceSpec{Command: "echo", Filters: []observable.FilterSpec{{Contains: "x", Regex: "x"}}}, want: "exactly one"},
		{name: "bad regex", config: observable.CommandSourceSpec{Command: "echo", Filters: []observable.FilterSpec{{Regex: "["}}}, want: "regex"},
		{name: "invalid parser", config: observable.CommandSourceSpec{Command: "echo", Parser: &observable.ParserSpec{Type: "xml"}}, want: "parser.type"},
		{name: "text attachments field", config: observable.CommandSourceSpec{Command: "echo", Parser: &observable.ParserSpec{Type: observable.ParserText, AttachmentsField: "attachments"}}, want: "requires parser.type jsonl"},
		{name: "invalid on exit", config: observable.CommandSourceSpec{Command: "echo", OnExit: observable.OnExitSpec{Notify: "sometimes"}}, want: "on_exit.notify"},
	}
	for _, tc := range commandCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := observable.NewCommandSpec("command", "", tc.config)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	scheduleCases := []struct {
		name   string
		config observable.ScheduleSourceSpec
		want   string
	}{
		{name: "missing content", config: observable.ScheduleSourceSpec{Interval: &observable.IntervalSchedule{EverySeconds: 60}}, want: "content"},
		{name: "missing attachment path", config: observable.ScheduleSourceSpec{Interval: &observable.IntervalSchedule{EverySeconds: 60}, Observation: observable.ScheduleObservationSpec{Content: "x", Attachments: []observable.AttachmentSpec{{MediaType: "image/png"}}}}, want: "path"},
		{name: "daily missing timezone", config: observable.ScheduleSourceSpec{Daily: &observable.DailySchedule{Times: []string{"09:00"}}, Observation: observable.ScheduleObservationSpec{Content: "x"}}, want: "timezone"},
		{name: "invalid timezone", config: observable.ScheduleSourceSpec{Timezone: "Mars/Olympus", Daily: &observable.DailySchedule{Times: []string{"09:00"}}, Observation: observable.ScheduleObservationSpec{Content: "x"}}, want: "IANA"},
		{name: "daily missing times", config: observable.ScheduleSourceSpec{Timezone: "UTC", Daily: &observable.DailySchedule{}, Observation: observable.ScheduleObservationSpec{Content: "x"}}, want: "daily.times"},
		{name: "invalid daily clock", config: observable.ScheduleSourceSpec{Timezone: "UTC", Daily: &observable.DailySchedule{Times: []string{"25:00"}}, Observation: observable.ScheduleObservationSpec{Content: "x"}}, want: "HH:MM"},
		{name: "invalid weekday", config: observable.ScheduleSourceSpec{Timezone: "UTC", Daily: &observable.DailySchedule{Times: []string{"09:00"}, Weekdays: []string{"funday"}}, Observation: observable.ScheduleObservationSpec{Content: "x"}}, want: "weekday"},
		{name: "interval too small", config: observable.ScheduleSourceSpec{Interval: &observable.IntervalSchedule{EverySeconds: 59}, Observation: observable.ScheduleObservationSpec{Content: "x"}}, want: "every_seconds"},
		{name: "multiple schedules", config: observable.ScheduleSourceSpec{Once: &observable.OnceSchedule{At: "2030-01-01T00:00:00Z"}, Interval: &observable.IntervalSchedule{EverySeconds: 60}, Observation: observable.ScheduleObservationSpec{Content: "x"}}, want: "exactly one"},
		{name: "invalid once", config: observable.ScheduleSourceSpec{Once: &observable.OnceSchedule{At: "tomorrow"}, Observation: observable.ScheduleObservationSpec{Content: "x"}}, want: "once.at"},
		{name: "invalid catch up mode", config: observable.ScheduleSourceSpec{Interval: &observable.IntervalSchedule{EverySeconds: 60}, CatchUp: observable.CatchUpSpec{Mode: "all"}, Observation: observable.ScheduleObservationSpec{Content: "x"}}, want: "catch_up.mode"},
		{name: "invalid catch up window", config: observable.ScheduleSourceSpec{Interval: &observable.IntervalSchedule{EverySeconds: 60}, CatchUp: observable.CatchUpSpec{Mode: observable.ScheduleCatchUpLatest}, Observation: observable.ScheduleObservationSpec{Content: "x"}}, want: "max_lateness_minutes"},
		{name: "invalid severity", config: observable.ScheduleSourceSpec{Interval: &observable.IntervalSchedule{EverySeconds: 60}, Observation: observable.ScheduleObservationSpec{Content: "x", Severity: "urgent"}}, want: "severity"},
		{name: "content too long", config: observable.ScheduleSourceSpec{Interval: &observable.IntervalSchedule{EverySeconds: 60}, Observation: observable.ScheduleObservationSpec{Content: strings.Repeat("x", observable.MaxScheduleContentChars+1)}}, want: "at most"},
	}
	for _, tc := range scheduleCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := observable.NewScheduleSpec("schedule", "", tc.config)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateConfigRejectsDuplicateIDs(t *testing.T) {
	_, err := observable.ValidateConfig(observable.FileConfig{Observables: []observable.Spec{validSpec("dup"), validSpec("dup")}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateSpecDerivesIDFromName(t *testing.T) {
	spec, err := observable.NewCommandSpec("", "Web Events!", observable.CommandSourceSpec{Command: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != "web-events" {
		t.Fatalf("id = %q", spec.ID)
	}
}

func TestSavedJSONContainsOnlyMatchingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observables.json")
	if err := observable.SaveConfig(path, observable.FileConfig{Observables: []observable.Spec{validSpec("command")}}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Observables []map[string]any `json:"observables"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	entry := parsed.Observables[0]
	if entry["type"] != observable.SourceTypeCommand || entry["command_config"] == nil {
		t.Fatalf("entry = %#v", entry)
	}
	if _, ok := entry["schedule_config"]; ok {
		t.Fatalf("entry contains schedule_config: %#v", entry)
	}
}

func TestExpandVariables(t *testing.T) {
	got := observable.ExpandVariables("$WORKDIR/${JUEX_WORKDIR}/$JUEX_WORKDIR/${WORKDIR}", "/tmp/work")
	if got != "/tmp/work//tmp/work//tmp/work//tmp/work" {
		t.Fatalf("ExpandVariables() = %q", got)
	}
}

func validSpec(id string) observable.Spec {
	spec, err := observable.NewCommandSpec(id, "", observable.CommandSourceSpec{
		Command: "juex-observable-test",
		Batch:   observable.BatchSpec{IntervalSeconds: 10, MaxChars: 1000},
	})
	if err != nil {
		panic(err)
	}
	return spec
}

func mutateCommandSpec(spec observable.Spec, mutate func(*observable.CommandSourceSpec)) observable.Spec {
	config, ok := spec.CommandConfig()
	if !ok {
		panic("not a command spec")
	}
	mutate(&config)
	next, err := observable.NewCommandSpec(spec.ID, spec.Name, config)
	if err != nil {
		panic(err)
	}
	return next
}
