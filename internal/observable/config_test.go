package observable_test

import (
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
