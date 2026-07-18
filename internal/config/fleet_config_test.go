package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadHomeFleetConfigDefaultsAndLoadsAddress(t *testing.T) {
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

	if err := os.WriteFile(
		filepath.Join(home, "juex.yaml"),
		[]byte("providers: definitely-not-parsed\nfleet:\n  addr: 127.0.0.1:6840\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	got, err = LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "127.0.0.1:6840" {
		t.Fatalf("configured addr = %q", got.Addr)
	}
	if !got.AddrConfigured {
		t.Fatal("configured fleet address was not marked explicit")
	}
}

func TestSetHomeFleetAddrMergesYAMLAtomically(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	path := filepath.Join(home, "juex.yaml")
	const original = "# keep this comment\nmodel: openai:test\nproviders:\n  - id: openai\n    protocol: openai/chat\nfleet:\n  addr: 127.0.0.1:6840 # keep addr comment\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	gotPath, err := SetHomeFleetAddr("127.0.0.1:6841")
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
		"model: openai:test",
		"id: openai",
		"fleet:",
		"addr: 127.0.0.1:6841",
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

func TestWorkspaceFleetConfigIsRejectedAsMisplaced(t *testing.T) {
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
