//go:build windows

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestWindowsEnvironmentBlock(t *testing.T) {
	if block, err := windowsEnvironmentBlock(nil); err != nil || block != nil {
		t.Fatalf("nil environment block = %#v, %v", block, err)
	}
	if block, err := windowsEnvironmentBlock([]string{}); err != nil || !reflect.DeepEqual(block, []uint16{0, 0}) {
		t.Fatalf("empty environment block = %#v, %v", block, err)
	}

	block, err := windowsEnvironmentBlock([]string{
		"z_KEY=last",
		"A_KEY=first",
		"M_KEY=雪",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := decodeWindowsEnvironmentBlock(block), []string{"A_KEY=first", "M_KEY=雪", "z_KEY=last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("environment block = %#v, want %#v", got, want)
	}
	if len(block) < 2 || block[len(block)-1] != 0 || block[len(block)-2] != 0 {
		t.Fatalf("environment block is not double-NUL terminated: %#v", block)
	}
	if _, err := windowsEnvironmentBlock([]string{"BAD=value\x00tail"}); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("NUL environment error = %v", err)
	}
}

func TestWindowsCommandEnvironmentUsesExecNormalization(t *testing.T) {
	cmd := exec.Command("cmd.exe")
	cmd.Env = []string{
		"Path=first",
		"PATH=second",
		"JUEX_TEST=value",
	}
	env := windowsCommandEnvironment(cmd)
	pathCount := 0
	systemRootFound := false
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		switch {
		case strings.EqualFold(key, "PATH"):
			pathCount++
			if value != "second" {
				t.Fatalf("normalized PATH = %q, want later value", value)
			}
		case strings.EqualFold(key, "SYSTEMROOT"):
			systemRootFound = true
		}
	}
	if pathCount != 1 {
		t.Fatalf("normalized PATH count = %d, environment = %#v", pathCount, env)
	}
	if !systemRootFound {
		t.Fatalf("normalized environment missing SYSTEMROOT: %#v", env)
	}
	if got := windowsCommandEnvironment(&exec.Cmd{}); got != nil {
		t.Fatalf("nil command environment = %#v, want inherited nil", got)
	}
}

func TestShellSessionConPTYReceivesExplicitEnvironment(t *testing.T) {
	const (
		sentinel = "conpty-explicit-environment"
	)
	manager := NewShellSessionManager(context.Background())
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close shell session manager: %v", err)
		}
	})

	env := append(os.Environ(),
		"JUEX_FAKE_SHELL=1",
		"JUEX_FAKE_SHELL_MODE=environment-delayed",
		"SHELL_RUNTIME_ENV_MARKER="+sentinel,
	)
	shell := fakeShellProfile()
	result, err := manager.Start(ShellStartRequest{
		Binary:          shell.Binary,
		Args:            shell.Args,
		Command:         "ignored",
		Env:             env,
		TTY:             true,
		Yield:           10 * time.Second,
		MaxOutputTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Running {
		t.Fatalf("ConPTY command still running: %+v", result)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("ConPTY exit code = %s, output = %q", formatTestExitCode(result.ExitCode), result.Output)
	}
	if !strings.Contains(result.Output, sentinel) {
		t.Fatalf("ConPTY output = %q, want explicit environment value", result.Output)
	}
}

func formatTestExitCode(code *int) string {
	if code == nil {
		return "<nil>"
	}
	return fmt.Sprint(*code)
}

func decodeWindowsEnvironmentBlock(block []uint16) []string {
	items := make([]string, 0)
	start := 0
	for index, value := range block {
		if value != 0 {
			continue
		}
		if index == start {
			break
		}
		items = append(items, string(utf16.Decode(block[start:index])))
		start = index + 1
	}
	return items
}
