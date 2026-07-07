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

func TestLoadConfig_MissingFileReturnsEmpty(t *testing.T) {
	cfg, err := observable.LoadConfig(filepath.Join(t.TempDir(), "observables.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Observables) != 0 {
		t.Fatalf("observables = %+v, want empty", cfg.Observables)
	}
}

func TestLoadConfig_DefaultsStreamsAndValidatesBatch(t *testing.T) {
	dir := t.TempDir()
	body := `{"observables":[{"id":"lark-events","command":"lark-cli","args":["watch","--json"],"batch":{"interval_seconds":10,"max_chars":1000}}]}`
	path := filepath.Join(dir, "observables.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := observable.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Observables[0]
	if got.ID != "lark-events" || !reflect.DeepEqual(got.Streams, []string{"stdout", "stderr"}) {
		t.Fatalf("spec defaults = %+v", got)
	}
}

func TestLoadConfig_AcceptsExplicitCommandSource(t *testing.T) {
	dir := t.TempDir()
	body := `{"observables":[{"id":"lark-events","source":{"type":"command","command":"lark-cli","args":["watch","--json"],"streams":["stdout"],"parser":{"type":"jsonl","content_field":"content"},"batch":{"interval_seconds":10,"max_chars":1000}},"observation":{"kind":"lark_notification","severity":"info"}}]}`
	path := filepath.Join(dir, "observables.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := observable.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Observables[0]
	if got.Source.Type != observable.SourceTypeCommand || got.Command != "lark-cli" || got.Parser == nil || got.Defaults.Kind != "lark_notification" {
		t.Fatalf("explicit command source = %+v", got)
	}
}

func TestValidateConfig_AcceptsScheduleSource(t *testing.T) {
	cfg, err := observable.ValidateConfig(observable.FileConfig{Observables: []observable.Spec{{
		ID: "weekday-brief",
		Source: observable.SourceSpec{
			Type:     observable.SourceTypeSchedule,
			Timezone: "Asia/Shanghai",
			Daily: &observable.DailySchedule{
				Times:    []string{"09:00"},
				Weekdays: []string{"mon", "tue", "wed", "thu", "fri"},
			},
			CatchUp: observable.CatchUpSpec{Mode: observable.ScheduleCatchUpLatest, MaxLatenessMinutes: 120},
		},
		Observation: observable.ObservationSpec{Content: "Prepare a work brief."},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Observables[0]
	if got.Observation.Kind != observable.DefaultScheduleKind || got.Observation.Severity != observable.DefaultSeverity {
		t.Fatalf("schedule defaults = %+v", got.Observation)
	}
}

func TestValidateConfig_RejectsInvalidSpecs(t *testing.T) {
	tests := []struct {
		name string
		cfg  observable.FileConfig
		want string
	}{
		{
			name: "duplicate ids",
			cfg: observable.FileConfig{Observables: []observable.Spec{
				validSpec("dup"),
				validSpec("dup"),
			}},
			want: "duplicate",
		},
		{
			name: "missing command",
			cfg: observable.FileConfig{Observables: []observable.Spec{
				withSpec(validSpec("missing-command"), func(s *observable.Spec) { s.Command = "" }),
			}},
			want: "command",
		},
		{
			name: "invalid id",
			cfg: observable.FileConfig{Observables: []observable.Spec{
				withSpec(validSpec("Bad ID"), func(s *observable.Spec) {}),
			}},
			want: "id",
		},
		{
			name: "invalid stream",
			cfg: observable.FileConfig{Observables: []observable.Spec{
				withSpec(validSpec("bad-stream"), func(s *observable.Spec) { s.Streams = []string{"stdout", "stdin"} }),
			}},
			want: "stream",
		},
		{
			name: "invalid severity",
			cfg: observable.FileConfig{Observables: []observable.Spec{
				withSpec(validSpec("bad-severity"), func(s *observable.Spec) { s.Defaults.Severity = "urgent" }),
			}},
			want: "severity",
		},
		{
			name: "batch interval too small",
			cfg: observable.FileConfig{Observables: []observable.Spec{
				withSpec(validSpec("small-interval"), func(s *observable.Spec) { s.Batch.IntervalSeconds = 4 }),
			}},
			want: "interval",
		},
		{
			name: "batch max too large",
			cfg: observable.FileConfig{Observables: []observable.Spec{
				withSpec(validSpec("large-max"), func(s *observable.Spec) { s.Batch.MaxChars = 1001 }),
			}},
			want: "max_chars",
		},
		{
			name: "filter no predicate",
			cfg: observable.FileConfig{Observables: []observable.Spec{
				withSpec(validSpec("empty-filter"), func(s *observable.Spec) { s.Filters = []observable.FilterSpec{{Kind: "matched_output"}} }),
			}},
			want: "filter",
		},
		{
			name: "filter both predicates",
			cfg: observable.FileConfig{Observables: []observable.Spec{
				withSpec(validSpec("double-filter"), func(s *observable.Spec) { s.Filters = []observable.FilterSpec{{Contains: "FAIL", Regex: "panic:"}} }),
			}},
			want: "exactly one",
		},
		{
			name: "bad regex",
			cfg: observable.FileConfig{Observables: []observable.Spec{
				withSpec(validSpec("bad-regex"), func(s *observable.Spec) { s.Filters = []observable.FilterSpec{{Regex: "["}} }),
			}},
			want: "regex",
		},
		{
			name: "schedule without content",
			cfg: observable.FileConfig{Observables: []observable.Spec{{
				ID: "empty-schedule",
				Source: observable.SourceSpec{
					Type:     observable.SourceTypeSchedule,
					Interval: &observable.IntervalSchedule{EverySeconds: 60},
				},
			}}},
			want: "observation.content",
		},
		{
			name: "daily schedule without timezone",
			cfg: observable.FileConfig{Observables: []observable.Spec{{
				ID: "daily-no-timezone",
				Source: observable.SourceSpec{
					Type:  observable.SourceTypeSchedule,
					Daily: &observable.DailySchedule{Times: []string{"09:00"}},
				},
				Observation: observable.ObservationSpec{Content: "hello"},
			}}},
			want: "source.timezone",
		},
		{
			name: "schedule with top-level defaults",
			cfg: observable.FileConfig{Observables: []observable.Spec{{
				ID: "schedule-defaults",
				Source: observable.SourceSpec{
					Type:     observable.SourceTypeSchedule,
					Interval: &observable.IntervalSchedule{EverySeconds: 60},
				},
				Defaults:    observable.Defaults{Severity: "critical"},
				Observation: observable.ObservationSpec{Content: "hello"},
			}}},
			want: "cannot set defaults",
		},
		{
			name: "schedule mixed with legacy command",
			cfg: observable.FileConfig{Observables: []observable.Spec{{
				ID:      "mixed-schedule",
				Command: "echo",
				Source: observable.SourceSpec{
					Type:     observable.SourceTypeSchedule,
					Interval: &observable.IntervalSchedule{EverySeconds: 60},
				},
				Observation: observable.ObservationSpec{Content: "hello"},
			}}},
			want: "legacy command",
		},
		{
			name: "schedule mixed with command source field",
			cfg: observable.FileConfig{Observables: []observable.Spec{{
				ID: "mixed-schedule-source",
				Source: observable.SourceSpec{
					Type:     observable.SourceTypeSchedule,
					Command:  "echo",
					Interval: &observable.IntervalSchedule{EverySeconds: 60},
				},
				Observation: observable.ObservationSpec{Content: "hello"},
			}}},
			want: "command fields",
		},
		{
			name: "command mixed with schedule source field",
			cfg: observable.FileConfig{Observables: []observable.Spec{{
				ID: "mixed-command-source",
				Source: observable.SourceSpec{
					Type:     observable.SourceTypeCommand,
					Command:  "echo",
					Interval: &observable.IntervalSchedule{EverySeconds: 60},
					Batch:    observable.BatchSpec{IntervalSeconds: 10, MaxChars: 1000},
				},
			}}},
			want: "schedule fields",
		},
		{
			name: "interval too small",
			cfg: observable.FileConfig{Observables: []observable.Spec{{
				ID: "small-schedule",
				Source: observable.SourceSpec{
					Type:     observable.SourceTypeSchedule,
					Interval: &observable.IntervalSchedule{EverySeconds: 59},
				},
				Observation: observable.ObservationSpec{Content: "hello"},
			}}},
			want: "every_seconds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := observable.ValidateConfig(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateConfig() err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSaveConfig_FormatsStableJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "observables.json")
	cfg := observable.FileConfig{Observables: []observable.Spec{validSpec("lark-events")}}
	if err := observable.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.HasSuffix(text, "\n") || !strings.Contains(text, "\n  \"observables\": [") {
		t.Fatalf("saved JSON = %q, want indented JSON ending with newline", text)
	}
}

func TestSaveConfig_PersistsPreferredSourceShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "observables.json")
	cfg := observable.FileConfig{Observables: []observable.Spec{
		validSpec("lark-events"),
		{
			ID: "weekday-brief",
			Source: observable.SourceSpec{
				Type:     observable.SourceTypeSchedule,
				Timezone: "Asia/Shanghai",
				Daily:    &observable.DailySchedule{Times: []string{"09:00"}},
				CatchUp:  observable.CatchUpSpec{Mode: observable.ScheduleCatchUpNone},
			},
			Observation: observable.ObservationSpec{Content: "Prepare a work brief."},
		},
	}}
	if err := observable.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, `"interval_seconds": 0`) || strings.Contains(text, `"on_exit": {}`) {
		t.Fatalf("saved JSON kept empty structs:\n%s", text)
	}
	if !strings.Contains(text, `"type": "schedule"`) || !strings.Contains(text, `"type": "command"`) {
		t.Fatalf("saved JSON missing source types:\n%s", text)
	}
	var parsed struct {
		Observables []map[string]any `json:"observables"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed.Observables[0]["command"]; ok {
		t.Fatalf("saved JSON kept legacy top-level command:\n%s", text)
	}
}

func TestSaveConfig_CommandSourceWithDefaultsRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "observables.json")
	spec := validSpec("with-defaults")
	spec.Defaults = observable.Defaults{Kind: "lark_notification", Severity: "info"}
	if err := observable.SaveConfig(path, observable.FileConfig{Observables: []observable.Spec{spec}}); err != nil {
		t.Fatal(err)
	}
	cfg, issues, err := observable.LoadConfigLenient(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("LoadConfigLenient issues = %+v, want none", issues)
	}
	if len(cfg.Observables) != 1 {
		t.Fatalf("observables = %+v, want one command observable", cfg.Observables)
	}
	got := cfg.Observables[0]
	if got.Source.Type != observable.SourceTypeCommand || got.Defaults.Kind != "lark_notification" || got.Defaults.Severity != "info" {
		t.Fatalf("round-tripped command observable = %+v", got)
	}
}

func TestLoadConfig_CommandObservationOverridesStaleDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observables.json")
	body := `{"observables":[{"id":"with-defaults","source":{"type":"command","command":"juex-observable-test","batch":{"interval_seconds":10,"max_chars":1000}},"observation":{"kind":"new_kind","severity":"critical"},"defaults":{"kind":"old_kind","severity":"warning"}}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := observable.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Observables[0]
	if got.Defaults.Kind != "new_kind" || got.Defaults.Severity != "critical" {
		t.Fatalf("command defaults = %+v, want preferred observation values", got.Defaults)
	}
}

func TestExpandVariables(t *testing.T) {
	got := observable.ExpandVariables("$WORKDIR/${JUEX_WORKDIR}/$JUEX_WORKDIR/${WORKDIR}", "/tmp/work")
	if got != "/tmp/work//tmp/work//tmp/work//tmp/work" {
		t.Fatalf("ExpandVariables() = %q", got)
	}
}

func validSpec(id string) observable.Spec {
	return observable.Spec{
		ID:      id,
		Command: "juex-observable-test",
		Batch: observable.BatchSpec{
			IntervalSeconds: 10,
			MaxChars:        1000,
		},
	}
}

func withSpec(s observable.Spec, fn func(*observable.Spec)) observable.Spec {
	fn(&s)
	return s
}
