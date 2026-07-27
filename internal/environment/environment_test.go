package environment

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestResolveAppliesLayersInheritedAndOverlaysInOrder(t *testing.T) {
	snapshot, err := Resolve(Options{
		Layers: []Layer{
			{
				Source: SourceUserConfig,
				Path:   "/home/test/.juex/juex.yaml",
				Values: map[string]string{
					"MARKER": "user",
					"EMPTY":  "",
				},
				Strict: true,
			},
			{
				Source: SourceDotenv,
				Path:   "/work/.env",
				Values: map[string]string{"MARKER": "dotenv"},
				Strict: true,
			},
			{
				Source: SourceWorkspaceConfig,
				Path:   "/work/.juex/juex.yaml",
				Values: map[string]string{"MARKER": "workspace"},
				Strict: true,
			},
			{
				Source: SourceExplicitConfig,
				Path:   "/other/override.yaml",
				Values: map[string]string{"MARKER": "explicit"},
				Strict: true,
			},
		},
		Inherited: []string{
			"MARKER=inherited",
			"PATH=/bin",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := snapshot.Lookup("MARKER"); !ok || got != "inherited" {
		t.Fatalf("MARKER = %q, %v", got, ok)
	}
	if got, ok := snapshot.Lookup("EMPTY"); !ok || got != "" {
		t.Fatalf("EMPTY = %q, %v", got, ok)
	}

	env := snapshot.Environ(
		map[string]string{"MARKER": "child", "WORKDIR": "child"},
		map[string]string{"WORKDIR": "/work", "JUEX_WORKDIR": "/work"},
	)
	got := envMapForTest(env, false)
	if got["MARKER"] != "child" {
		t.Fatalf("child overlay lost: %q", got["MARKER"])
	}
	if got["WORKDIR"] != "/work" || got["JUEX_WORKDIR"] != "/work" {
		t.Fatalf("runtime overlay lost: %#v", got)
	}
	if got["EMPTY"] != "" {
		t.Fatalf("empty value lost: %#v", got)
	}
	if !reflect.DeepEqual(env, sortedCopy(env)) {
		t.Fatalf("environment output is not deterministic: %#v", env)
	}
}

func TestResolveRejectsStrictInvalidAndReservedEntries(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{name: "invalid name", values: map[string]string{"BAD-NAME": "x"}, want: "BAD-NAME"},
		{name: "nul value", values: map[string]string{"GOOD": "x\x00y"}, want: "NUL"},
		{name: "reserved", values: map[string]string{"JUEX_HOME": "/tmp/other"}, want: "reserved"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(Options{Layers: []Layer{{
				Source: SourceWorkspaceConfig,
				Path:   "/work/.juex/juex.yaml",
				Values: tc.values,
				Strict: true,
			}}})
			if err == nil || !strings.Contains(err.Error(), "/work/.juex/juex.yaml") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want path and %q", err, tc.want)
			}
		})
	}
}

func TestResolveWindowsCaseRulesAndDeterministicOutput(t *testing.T) {
	_, err := Resolve(Options{
		GOOS: "windows",
		Layers: []Layer{{
			Source: SourceWorkspaceConfig,
			Path:   `C:\work\.juex\juex.yaml`,
			Values: map[string]string{"Path": "a", "PATH": "b"},
			Strict: true,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "case-conflicting") {
		t.Fatalf("err = %v, want case-conflicting error", err)
	}

	snapshot, err := Resolve(Options{
		GOOS: "windows",
		Inherited: []string{
			"Path=first",
			"PATH=second",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := snapshot.Environ(map[string]string{"path": "child"})
	got := envMapForTest(env, true)
	if len(got) != 1 || got["PATH"] != "child" {
		t.Fatalf("windows environment = %#v", got)
	}
	if !reflect.DeepEqual(env, sortedCopy(env)) {
		t.Fatalf("windows environment output is not deterministic: %#v", env)
	}
}

func TestMetadataNeverContainsValues(t *testing.T) {
	const sentinel = "configured-value-must-not-leak"
	snapshot, err := Resolve(Options{
		Layers: []Layer{{
			Source: SourceDotenv,
			Path:   "/work/.env",
			Values: map[string]string{"SECRET_MARKER": sentinel},
			Strict: true,
		}},
		Inherited: []string{"PATH=/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range snapshot.Metadata() {
		encoded := item.Key + item.Path + string(item.Source)
		if strings.Contains(encoded, sentinel) {
			t.Fatalf("metadata leaked value: %+v", item)
		}
	}
	configured := snapshot.ConfiguredMetadata()
	if len(configured) != 1 || configured[0].Key != "SECRET_MARKER" || configured[0].Source != SourceDotenv {
		t.Fatalf("configured metadata = %+v", configured)
	}
}

func TestSnapshotLookPathUsesResolvedPath(t *testing.T) {
	dir := t.TempDir()
	commandName := "snapshot-command"
	executable := filepath.Join(dir, commandName)
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Resolve(Options{
		Layers: []Layer{{
			Source: SourceDotenv,
			Path:   filepath.Join(dir, ".env"),
			Values: map[string]string{"PATH": dir},
			Strict: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := snapshot.LookPath(commandName)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(got, executable) {
		t.Fatalf("LookPath = %q, want case-insensitive %q", got, executable)
	}
	if runtime.GOOS != "windows" && got != executable {
		t.Fatalf("LookPath = %q, want %q", got, executable)
	}
}

func TestZeroSnapshotLookPathUsesInheritedPlatformRules(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Snapshot{}).LookPath(executable)
	if err != nil {
		t.Fatal(err)
	}
	if got != executable {
		t.Fatalf("LookPath = %q, want %q", got, executable)
	}
}

func TestSnapshotLookPathInDirResolvesSlashRelativeExecutable(t *testing.T) {
	dir := t.TempDir()
	commandName := "collector"
	executable := filepath.Join(dir, commandName)
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot := FromEnviron(os.Environ())

	relative := "." + string(filepath.Separator) + commandName
	got, err := snapshot.LookPathInDir(relative, dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(got, executable) {
		t.Fatalf("LookPathInDir = %q, want case-insensitive %q", got, executable)
	}
	if runtime.GOOS != "windows" && got != executable {
		t.Fatalf("LookPathInDir = %q, want %q", got, executable)
	}
	t.Chdir(dir)
	got, err = snapshot.LookPath(relative)
	if err != nil {
		t.Fatal(err)
	}
	wantRelative := relative
	if runtime.GOOS == "windows" {
		wantRelative += ".exe"
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(got, wantRelative) {
		t.Fatalf("LookPath without child dir = %q, want case-insensitive %q", got, wantRelative)
	}
	if runtime.GOOS != "windows" && got != wantRelative {
		t.Fatalf("LookPath without child dir = %q, want %q", got, wantRelative)
	}
	if _, err := snapshot.LookPath("." + string(filepath.Separator) + "missing"); err != exec.ErrNotFound {
		t.Fatalf("missing slash-relative LookPath error = %v, want exec.ErrNotFound", err)
	}
}

func TestSnapshotIsImmutableAndActivationRestoresProcess(t *testing.T) {
	const key = "JUEX_ENVIRONMENT_ACTIVATION_TEST"
	original, originallySet := os.LookupEnv(key)
	if err := os.Setenv(key, "before"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if originallySet {
			_ = os.Setenv(key, original)
		} else {
			_ = os.Unsetenv(key)
		}
	})

	values := map[string]string{key: "configured"}
	snapshot, err := Resolve(Options{Layers: []Layer{{
		Source: SourceWorkspaceConfig,
		Path:   "/work/.juex/juex.yaml",
		Values: values,
		Strict: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	values[key] = "mutated"
	if got, _ := snapshot.Lookup(key); got != "configured" {
		t.Fatalf("snapshot changed after input mutation: %q", got)
	}

	restore, err := snapshot.Activate()
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(key); got != "configured" {
		t.Fatalf("activated value = %q", got)
	}
	if _, err := snapshot.Activate(); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("nested activation err = %v", err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(key); got != "before" {
		t.Fatalf("restored value = %q, want before", got)
	}
	if err := restore(); err != nil {
		t.Fatalf("second restore = %v", err)
	}
}

func TestRedactConfiguredValuesCoversRawAndJSONEscapedForms(t *testing.T) {
	snapshot, err := Resolve(Options{Layers: []Layer{{
		Source: SourceDotenv,
		Path:   "/work/.env",
		Values: map[string]string{
			"PLAIN_SECRET": "plain-secret",
			"JSON_SECRET":  "line\nsecret",
			"EMPTY":        "",
		},
		Strict: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("raw=plain-secret json=line\\nsecret empty marker remains")
	got, changed := snapshot.RedactConfiguredValues(input)
	if !changed {
		t.Fatal("redaction did not report a change")
	}
	text := string(got)
	for _, leaked := range []string{"plain-secret", `line\nsecret`} {
		if strings.Contains(text, leaked) {
			t.Fatalf("redaction leaked %q: %s", leaked, text)
		}
	}
	if strings.Count(text, "[REDACTED_ENV]") != 2 {
		t.Fatalf("redacted payload = %q", text)
	}
}

func TestRedactConfiguredValuesRetainsOverriddenLayerValues(t *testing.T) {
	snapshot, err := Resolve(Options{Layers: []Layer{
		{
			Source: SourceUserConfig,
			Path:   "/home/test/.juex/juex.yaml",
			Values: map[string]string{"TOKEN": "old-secret"},
			Strict: true,
		},
		{
			Source: SourceWorkspaceConfig,
			Path:   "/work/.juex/juex.yaml",
			Values: map[string]string{"TOKEN": "new-secret"},
			Strict: true,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, changed := snapshot.RedactConfiguredValues([]byte("before=old-secret after=new-secret"))
	if !changed {
		t.Fatal("redaction did not report a change")
	}
	text := string(got)
	for _, leaked := range []string{"old-secret", "new-secret"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("redaction leaked overridden value %q: %s", leaked, text)
		}
	}
	if strings.Count(text, "[REDACTED_ENV]") != 2 {
		t.Fatalf("redacted payload = %q", text)
	}
}

func TestRedactConfiguredJSONPreservesKeysAndSyntaxForShortValues(t *testing.T) {
	snapshot, err := Resolve(Options{Layers: []Layer{{
		Source: SourceDotenv,
		Path:   "/work/.env",
		Values: map[string]string{"SHORT": "a"},
		Strict: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got, changed, err := snapshot.RedactConfiguredJSON([]byte(`{"data":"a","array":["cat"],"a":"key-is-not-redacted"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("JSON redaction did not report a change")
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("redacted JSON invalid: %v\n%s", err, got)
	}
	if _, ok := decoded["array"]; !ok {
		t.Fatalf("JSON key was redacted: %s", got)
	}
	if decoded["a"] != "key-is-not-red[REDACTED_ENV]cted" {
		t.Fatalf("object key or value handling = %#v", decoded)
	}
}

func envMapForTest(env []string, caseInsensitive bool) map[string]string {
	out := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if caseInsensitive {
			key = strings.ToUpper(key)
		}
		out[key] = value
	}
	return out
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
