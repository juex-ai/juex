//go:build darwin || linux

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	sandboxTargetHelperTestKey    = "JUEX_SANDBOX_TARGET_HELPER_TEST"
	sandboxTargetHelperOutputKey  = "JUEX_SANDBOX_TARGET_HELPER_OUTPUT"
	sandboxTargetHelperTestSecret = "restored-inside-boundary"
)

func TestSandboxTargetHelperRestoresDeferredEnvironment(t *testing.T) {
	output := filepath.Join(t.TempDir(), "target-environment")
	_, _, launcher, err := sandboxTargetLaunch(ExecSpec{
		Binary: "/bin/sh",
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"LD_LIBRARY_PATH=" + sandboxTargetHelperTestSecret,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	launcher = append(launcher,
		sandboxTargetHelperTestKey+"=1",
		sandboxTargetHelperOutputKey+"="+output,
	)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSandboxTargetHelperProcess$")
	cmd.Env = launcher
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("target helper failed: %v\n%s", err, combined)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sandboxTargetHelperTestSecret {
		t.Fatalf("restored target environment = %q, want %q", data, sandboxTargetHelperTestSecret)
	}
}

func TestSandboxTargetHelperProcess(t *testing.T) {
	if os.Getenv(sandboxTargetHelperTestKey) != "1" {
		return
	}
	script := `printf '%s' "$LD_LIBRARY_PATH" > "$JUEX_SANDBOX_TARGET_HELPER_OUTPUT"`
	handled, err := MaybeExecTarget([]string{
		os.Args[0],
		sandboxTargetHelperArgument,
		"--",
		"/bin/sh",
		"-c",
		script,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("sandbox target helper protocol was not recognized")
	}
}

func TestSandboxTargetHelperRejectsMalformedProtocol(t *testing.T) {
	handled, err := MaybeExecTarget([]string{"juex", sandboxTargetHelperArgument, "missing-separator", "sh"})
	if !handled || err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("malformed helper result = handled:%t err:%v", handled, err)
	}
}
