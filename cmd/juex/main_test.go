package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCLI_BuildAndVersion compiles the binary and runs the no-network
// subcommands to make sure the CLI wiring stays sound. We do this in a
// subtest so the build cost is amortised.
func TestCLI_BuildAndVersion(t *testing.T) {
	bin := buildBinary(t)
	t.Run("version", func(t *testing.T) {
		out, err := exec.Command(bin, "version").CombinedOutput()
		if err != nil {
			t.Fatalf("juex version: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "juex") {
			t.Fatalf("unexpected: %s", out)
		}
	})
	t.Run("versionVerbose", func(t *testing.T) {
		out, err := exec.Command(bin, "version", "-v").CombinedOutput()
		if err != nil {
			t.Fatalf("juex version -v: %v\n%s", err, out)
		}
		body := string(out)
		for _, want := range []string{"juex", "commit:", "built:", "go:", "os/arch:"} {
			if !strings.Contains(body, want) {
				t.Errorf("verbose output missing %q in:\n%s", want, body)
			}
		}
	})
	t.Run("help", func(t *testing.T) {
		out, _ := exec.Command(bin, "help").CombinedOutput()
		body := string(out)
		// cobra renders subcommand list under "Available Commands"
		for _, want := range []string{"init", "doctor", "run", "repl", "sessions", "serve", "version"} {
			if !strings.Contains(body, want) {
				t.Errorf("help output missing %q in:\n%s", want, body)
			}
		}
	})
	t.Run("rootHelpFlag", func(t *testing.T) {
		out, _ := exec.Command(bin, "--help").CombinedOutput()
		if !strings.Contains(string(out), "Available Commands") {
			t.Fatalf("--help output: %s", out)
		}
	})
	t.Run("unknownExitsNonZero", func(t *testing.T) {
		err := exec.Command(bin, "totally-bogus").Run()
		if err == nil {
			t.Fatal("expected non-zero exit")
		}
	})
	t.Run("runRequiresPrompt", func(t *testing.T) {
		cmd := exec.Command(bin, "run")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil {
			t.Fatal("expected non-zero exit when prompt missing")
		}
		body := stderr.String()
		// Either our usageError text or cobra's own arg-validation message.
		if !strings.Contains(body, "prompt required") &&
			!strings.Contains(body, "requires") {
			t.Fatalf("stderr: %s", body)
		}
	})
	t.Run("runFailsCleanlyWithoutEnv", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command(bin, "run", "hi")
		cmd.Dir = dir
		cmd.Env = isolatedCLIEnv(dir)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil {
			t.Fatalf("expected error, stderr was: %s", stderr.String())
		}
		if !strings.Contains(stderr.String(), "juex init") {
			t.Fatalf("stderr: %s", stderr.String())
		}
	})
	t.Run("cwdFlagAcceptedAtRoot", func(t *testing.T) {
		// `juex --cwd <dir> run "..."` parses; we just verify the flag is
		// recognised. The command will fail on missing config, which is fine;
		// we only care about flag parsing.
		dir := t.TempDir()
		cmd := exec.Command(bin, "--cwd", dir, "run", "hi")
		cmd.Env = isolatedCLIEnv(dir)
		out, _ := cmd.CombinedOutput()
		body := string(out)
		// Should NOT see "unknown flag" / "unknown shorthand"
		if strings.Contains(body, "unknown flag") || strings.Contains(body, "unknown shorthand") {
			t.Fatalf("--cwd not recognised: %s", body)
		}
	})
}

func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "juex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func isolatedCLIEnv(home string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"HOME="+home,
		"USERPROFILE="+home,
		"CODEX_HOME="+filepath.Join(home, "missing-codex-home"),
		"PROVIDER_API_ID=",
		"PROVIDER_API_PROTOCOL=",
		"PROVIDER_API_BASE=",
		"PROVIDER_API_KEY=",
		"PROVIDER_API_MODEL=",
		"PROVIDER_THINKING_EFFORT=",
		"PROVIDER_CONTEXT_WINDOW=",
	)
	return env
}
