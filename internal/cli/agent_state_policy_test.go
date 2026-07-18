package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCommandAgentStatePolicyCoversExecutableTree(t *testing.T) {
	root := newRootCmd()
	want := map[string]agentStatePolicy{
		"juex run":               agentStateMint,
		"juex repl":              agentStateMint,
		"juex init":              agentStateNone,
		"juex doctor":            agentStateNone,
		"juex version":           agentStateNone,
		"juex schema":            agentStateNone,
		"juex sessions list":     agentStateExisting,
		"juex sessions show":     agentStateExisting,
		"juex sessions context":  agentStateExisting,
		"juex sessions compact":  agentStateExisting,
		"juex sessions activate": agentStateExisting,
		"juex sessions delete":   agentStateExisting,
		"juex bundle":            agentStateExisting,
		"juex serve":             agentStateMint,
	}

	var visited []string
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Runnable() && cmd != root {
			policy, err := commandAgentStatePolicy(cmd)
			if err != nil {
				t.Errorf("%s: %v", cmd.CommandPath(), err)
			} else if strings.HasPrefix(cmd.CommandPath(), "juex fleet") {
				if policy != agentStateNone {
					t.Errorf("%s policy = %v, want none", cmd.CommandPath(), policy)
				}
			} else {
				expected, ok := want[cmd.CommandPath()]
				if !ok {
					t.Errorf("%s missing from expected policy table", cmd.CommandPath())
				} else if policy != expected {
					t.Errorf("%s policy = %v, want %v", cmd.CommandPath(), policy, expected)
				}
			}
			visited = append(visited, cmd.CommandPath())
		}
		for _, child := range cmd.Commands() {
			if child.Name() != "help" && child.Name() != "completion" {
				walk(child)
			}
		}
	}
	walk(root)

	for path := range want {
		found := false
		for _, actual := range visited {
			if actual == path {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected executable command %s was not visited", path)
		}
	}
}

func TestRuntimeCommandsExposeEphemeralFlags(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{{"run"}, {"repl"}, {"serve"}} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"ephemeral", "keep"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Errorf("%s missing --%s", cmd.CommandPath(), name)
			}
		}
	}
}

func TestInvalidEphemeralFlagCombinationsDoNotMint(t *testing.T) {
	home := setHomeForCLITest(t)
	cases := [][]string{
		{"run", "--keep", "hello"},
		{"run", "--dry-run", "--keep", "hello"},
		{"run", "--dry-run", "--ephemeral", "hello"},
		{"repl", "--keep"},
		{"serve", "--keep"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			work := t.TempDir()
			root := newRootCmd()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs(append([]string{"-C", work}, args...))
			err := root.Execute()
			var usage *usageError
			if !errors.As(err, &usage) {
				t.Fatalf("err = %T %v, want usageError\n%s", err, err, output.String())
			}
			if _, err := os.Stat(filepath.Join(work, ".juex", "juex.local.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid flags created marker: %v", err)
			}
			if _, err := os.Stat(filepath.Join(home, ".juex", "agents")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid flags created registry: %v", err)
			}
		})
	}
}

func TestSessionsListMissingAgentIsNotFoundWithoutWrites(t *testing.T) {
	home := setHomeForCLITest(t)
	work := t.TempDir()
	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"-C", work, "sessions", "list"})

	err := root.Execute()
	var notFound *notFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %T %v, want notFoundError\n%s", err, err, output.String())
	}
	if !strings.Contains(err.Error(), "no agent exists") {
		t.Fatalf("err = %q, want no-agent guidance", err)
	}
	entries, readErr := os.ReadDir(work)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("workspace entries = %v, want empty", entries)
	}
	if _, err := os.Stat(filepath.Join(home, ".juex", "agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only command created registry: %v", err)
	}
}

func TestRunDryRunUsesCleanedScratchState(t *testing.T) {
	home := setHomeForCLITest(t)
	work := t.TempDir()
	tempParent := t.TempDir()
	t.Setenv("TMPDIR", tempParent)
	if err := writeJuexConfigFile(filepath.Join(home, ".juex", "juex.yaml"), "openai", "https://example.invalid", "k", "m"); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"-C", work, "run", "--dry-run", "hello"})

	err := root.Execute()
	var dryRunOK *dryRunOK
	if !errors.As(err, &dryRunOK) {
		t.Fatalf("err = %T %v, want dryRunOK\n%s", err, err, output.String())
	}
	assertEmptyDirectory(t, work)
	assertEmptyDirectory(t, tempParent)
	if _, err := os.Stat(filepath.Join(home, ".juex", "agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created durable registry: %v", err)
	}
}

func TestEphemeralRunCleanupAndKeepOnStartupError(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup", true: "keep"}[keep], func(t *testing.T) {
			home := setHomeForCLITest(t)
			work := t.TempDir()
			tempParent := t.TempDir()
			t.Setenv("TMPDIR", tempParent)
			root := newRootCmd()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			args := []string{"-C", work, "run", "--ephemeral"}
			if keep {
				args = append(args, "--keep")
			}
			args = append(args, "hello")
			root.SetArgs(args)

			if err := root.Execute(); err == nil {
				t.Fatal("expected missing runtime config error")
			}
			assertEmptyDirectory(t, work)
			if _, err := os.Stat(filepath.Join(home, ".juex", "agents")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ephemeral run created durable registry: %v", err)
			}
			if !keep {
				assertEmptyDirectory(t, tempParent)
				return
			}
			path := keptEphemeralPath(output.String())
			if path == "" {
				t.Fatalf("missing kept-state path:\n%s", output.String())
			}
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				t.Fatalf("kept state %s is not a directory: %v", path, err)
			}
			if err := os.RemoveAll(filepath.Dir(filepath.Dir(path))); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func keptEphemeralPath(output string) string {
	const prefix = "juex: kept ephemeral state at "
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func assertEmptyDirectory(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s entries = %v, want empty", path, entries)
	}
}
