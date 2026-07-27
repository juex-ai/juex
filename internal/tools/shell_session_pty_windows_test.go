//go:build windows

package tools

import (
	"context"
	"os"
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

func TestShellSessionConPTYReceivesExplicitEnvironment(t *testing.T) {
	const (
		key      = "JUEX_CONPTY_ENVIRONMENT_TEST"
		sentinel = "conpty-explicit-environment"
	)
	manager := NewShellSessionManager(context.Background())
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close shell session manager: %v", err)
		}
	})

	env := append(os.Environ(), key+"="+sentinel)
	result, err := manager.Start(ShellStartRequest{
		Binary:          "cmd.exe",
		Args:            []string{"/d", "/s", "/c"},
		Command:         "echo %" + key + "%",
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
		t.Fatalf("ConPTY exit code = %v, output = %q", result.ExitCode, result.Output)
	}
	if !strings.Contains(result.Output, sentinel) {
		t.Fatalf("ConPTY output = %q, want explicit environment value", result.Output)
	}
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
