package version

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestString_Default(t *testing.T) {
	if got := String(); got != "juex 0.0.1-dev" {
		t.Fatalf("String() = %q", got)
	}
}

func TestVerbose_BuildOnly(t *testing.T) {
	out := Verbose()
	for _, want := range []string{"juex 0.0.1-dev", "commit:", "built:", "go:", "os/arch:", runtime.Version(), runtime.GOOS, runtime.GOARCH} {
		if !strings.Contains(out, want) {
			t.Errorf("Verbose() missing %q. full:\n%s", want, out)
		}
	}
	// Optional fields should not appear when empty.
	for _, mustNot := range []string{"work_dir:", "config_file:", "provider_type:"} {
		if strings.Contains(out, mustNot) {
			t.Errorf("Verbose() should not contain %q with empty Info; got:\n%s", mustNot, out)
		}
	}
}

func TestVerbose_WithRuntimeContext(t *testing.T) {
	info := Build()
	info.WorkDir = "/tmp/x"
	info.ConfigFile = "/tmp/juex.yaml"
	info.ProviderType = "openai"
	info.Model = "gpt-test"
	info.BaseURL = "https://x"

	out := info.Verbose()
	for _, want := range []string{
		"work_dir:      /tmp/x",
		"config_file:   /tmp/juex.yaml",
		"provider_type: openai",
		"model:         gpt-test",
		"base_url:      https://x",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Verbose() missing %q in:\n%s", want, out)
		}
	}
	// Derived paths must NOT appear — they are reconstructible from work_dir.
	for _, mustNot := range []string{"sessions_dir:", "memory_dir:", "home_agents:"} {
		if strings.Contains(out, mustNot) {
			t.Errorf("Verbose() should not contain derived path %q; got:\n%s", mustNot, out)
		}
	}
}

func TestJSON_OmitsDerivedPaths(t *testing.T) {
	info := Build()
	info.WorkDir = "/tmp/x"
	js := info.JSON()
	for _, mustNot := range []string{"sessions_dir", "memory_dir", "home_agents_dir"} {
		if strings.Contains(js, mustNot) {
			t.Errorf("JSON should not contain derived path %q in:\n%s", mustNot, js)
		}
	}
}

func TestJSON_RoundTrip(t *testing.T) {
	in := Build()
	in.WorkDir = "/tmp/x"
	in.ProviderType = "openai"
	js := in.JSON()
	var out Info
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, js)
	}
	if out.WorkDir != "/tmp/x" || out.ProviderType != "openai" || out.Name != "juex" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestVerbose_OverridesPropagated(t *testing.T) {
	prevV, prevC, prevB := Version, Commit, BuildTime
	t.Cleanup(func() { Version, Commit, BuildTime = prevV, prevC, prevB })
	Version = "1.2.3"
	Commit = "abc1234"
	BuildTime = "2026-05-01T00:00:00Z"
	out := Build().Verbose()
	for _, want := range []string{"juex 1.2.3", "abc1234", "2026-05-01T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("Verbose missing %q in:\n%s", want, out)
		}
	}
}

func TestBuild_FieldsPopulated(t *testing.T) {
	info := Build()
	if info.Name != "juex" || info.GoVersion == "" || info.OS == "" || info.Arch == "" {
		t.Fatalf("missing core fields: %+v", info)
	}
}
