package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadHomeFleetConfigDefaultsAndLoadsAddress(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	home := t.TempDir()
	t.Setenv("JUEX_HOME", home)

	got, err := LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != DefaultFleetAddr {
		t.Fatalf("default addr = %q, want %q", got.Addr, DefaultFleetAddr)
	}
	if got.AddrConfigured {
		t.Fatal("default fleet address reported as explicitly configured")
	}
	if got.UnsafeBindAny {
		t.Fatalf("default unsafe bind settings = %+v", got)
	}

	if err := os.WriteFile(
		filepath.Join(home, "juex.yaml"),
		[]byte("providers: definitely-not-parsed\nfleet:\n  addr: 0.0.0.0:6840\n  unsafe_bind_any: true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	got, err = LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "0.0.0.0:6840" {
		t.Fatalf("configured addr = %q", got.Addr)
	}
	if !got.AddrConfigured {
		t.Fatal("configured fleet address was not marked explicit")
	}
	if !got.UnsafeBindAny {
		t.Fatalf("configured unsafe bind settings = %+v", got)
	}
}

func TestLoadHomeFleetConfigMergesDefaultAndInstanceHomes(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	defaultHome := filepath.Join(userHome, ".juex")
	if err := os.MkdirAll(defaultHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(defaultHome, "juex.yaml"),
		[]byte("fleet:\n  addr: 127.0.0.1:5840\n  unsafe_bind_any: true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	instanceHome := t.TempDir()
	t.Setenv("JUEX_HOME", instanceHome)
	if err := os.WriteFile(
		filepath.Join(instanceHome, "juex.yaml"),
		[]byte("fleet:\n  addr: 127.0.0.1:5999\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	got, err := LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "127.0.0.1:5999" || !got.AddrConfigured {
		t.Fatalf("instance fleet address = %+v", got)
	}
	if !got.UnsafeBindAny {
		t.Fatalf("unsafe bind setting was not inherited from default home: %+v", got)
	}
}

func TestLoadHomeFleetConfigInstanceFalseOverridesDefaultTrue(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	defaultHome := filepath.Join(userHome, ".juex")
	if err := os.MkdirAll(defaultHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(defaultHome, "juex.yaml"),
		[]byte("fleet:\n  addr: 127.0.0.1:5840\n  unsafe_bind_any: true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	instanceHome := t.TempDir()
	t.Setenv("JUEX_HOME", instanceHome)
	if err := os.WriteFile(
		filepath.Join(instanceHome, "juex.yaml"),
		[]byte("fleet:\n  unsafe_bind_any: false\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	got, err := LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "127.0.0.1:5840" || !got.AddrConfigured || got.UnsafeBindAny {
		t.Fatalf("fleet config = %+v, want inherited addr and explicit false", got)
	}
}

func TestLoadHomeFleetConfigEmptyInstanceAddressKeepsDefaultHomeAddress(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	defaultHome := filepath.Join(userHome, ".juex")
	if err := os.MkdirAll(defaultHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(defaultHome, "juex.yaml"),
		[]byte("fleet:\n  addr: 127.0.0.1:5840\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	instanceHome := t.TempDir()
	t.Setenv("JUEX_HOME", instanceHome)
	if err := os.WriteFile(
		filepath.Join(instanceHome, "juex.yaml"),
		[]byte("fleet:\n  addr: \"\"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	got, err := LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "127.0.0.1:5840" || !got.AddrConfigured {
		t.Fatalf("fleet config = %+v, want inherited default-home address", got)
	}
}

func TestLoadHomeFleetConfigRejectsInvalidDefaultBeforeInstance(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	defaultHome := filepath.Join(userHome, ".juex")
	if err := os.MkdirAll(defaultHome, 0o755); err != nil {
		t.Fatal(err)
	}
	defaultPath := filepath.Join(defaultHome, "juex.yaml")
	if err := os.WriteFile(defaultPath, []byte("fleet:\n  addr: invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instanceHome := t.TempDir()
	t.Setenv("JUEX_HOME", instanceHome)
	if err := os.WriteFile(
		filepath.Join(instanceHome, "juex.yaml"),
		[]byte("providers: deliberately-ignored\nfleet:\n  addr: 127.0.0.1:5999\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	_, err := LoadHomeFleetConfig()
	if err == nil ||
		!strings.Contains(err.Error(), "fleet.addr") ||
		!strings.Contains(err.Error(), `got "invalid"`) {
		t.Fatalf("error = %v, want invalid default-home fleet config", err)
	}
}

func TestSetHomeFleetSettingsMergesYAMLAtomically(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	home := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	path := filepath.Join(home, "juex.yaml")
	const original = "# keep this comment\nmodels: [openai:test]\nproviders:\n  - id: openai\n    protocol: openai/chat\nfleet:\n  addr: 127.0.0.1:6840 # keep addr comment\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	gotPath, err := SetHomeFleetSettings("0.0.0.0:6841", true)
	if err != nil {
		t.Fatal(err)
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(resolvedHome, "juex.yaml")
	if gotPath != wantPath {
		t.Fatalf("path = %q, want %q", gotPath, wantPath)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# keep this comment",
		"models:",
		"openai:test",
		"id: openai",
		"fleet:",
		"addr: 0.0.0.0:6841",
		"unsafe_bind_any: true",
		"# keep addr comment",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("merged config missing %q:\n%s", want, body)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSetHomeFleetSettingsWritesOnlyInstanceHome(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	defaultHome := filepath.Join(userHome, ".juex")
	if err := os.MkdirAll(defaultHome, 0o755); err != nil {
		t.Fatal(err)
	}
	defaultPath := filepath.Join(defaultHome, "juex.yaml")
	defaultBody := []byte("models: [shared:base]\nfleet:\n  addr: 127.0.0.1:5840\n")
	if err := os.WriteFile(defaultPath, defaultBody, 0o640); err != nil {
		t.Fatal(err)
	}
	instanceHome := t.TempDir()
	t.Setenv("JUEX_HOME", instanceHome)
	canonicalInstanceHome, err := filepath.EvalSymlinks(instanceHome)
	if err != nil {
		t.Fatal(err)
	}

	gotPath, err := SetHomeFleetSettings("127.0.0.1:5999", false)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != filepath.Join(canonicalInstanceHome, "juex.yaml") {
		t.Fatalf("write path = %q, want instance home", gotPath)
	}
	gotDefault, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotDefault) != string(defaultBody) {
		t.Fatalf("default config changed:\n%s", gotDefault)
	}
	info, err := os.Stat(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("default config mode = %o, want unchanged 640", info.Mode().Perm())
	}
}

func TestRuntimeConfigLoadsFleetUnsafeBindSetting(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	home := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	if err := os.WriteFile(
		filepath.Join(home, "juex.yaml"),
		[]byte("fleet:\n  addr: 0.0.0.0:6842\n  unsafe_bind_any: true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForWorkDirForValidation(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Fleet.Addr != "0.0.0.0:6842" || !cfg.Fleet.UnsafeBindAny {
		t.Fatalf("fleet config = %+v", cfg.Fleet)
	}
}

func TestWorkspaceFleetConfigIsRejectedAsMisplaced(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	if err := os.MkdirAll(filepath.Join(work, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(work, ".juex", "juex.yaml"),
		[]byte("fleet:\n  addr: 127.0.0.1:6842\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	_, err := LoadForWorkDirForValidation(work)
	if err == nil || !strings.Contains(err.Error(), "fleet is only supported") {
		t.Fatalf("error = %v, want misplaced fleet config", err)
	}
}

func TestValidateStableFleetAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "127.0.0.1", "localhost:nope"} {
		if err := ValidateStableFleetAddr(addr); err == nil {
			t.Fatalf("ValidateStableFleetAddr(%q) succeeded", addr)
		}
	}
	if err := ValidateStableFleetAddr("127.0.0.1:5839"); err != nil {
		t.Fatal(err)
	}
}
