package cli

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestManagedCLIExposesResourceScopedCommandTree(t *testing.T) {
	root := newRootCmd()
	assertVisibleCommands(t, root, []string{
		"agent", "completion", "config", "diagnose", "fleet", "help", "thread", "version",
	})

	agent, _, err := root.Find([]string{"agent"})
	if err != nil {
		t.Fatal(err)
	}
	assertVisibleCommands(t, agent, []string{
		"add", "config", "disable", "enable", "list", "logs", "remove", "restart", "send", "show", "start", "stop",
	})

	thread, _, err := root.Find([]string{"thread"})
	if err != nil {
		t.Fatal(err)
	}
	assertVisibleCommands(t, thread, []string{
		"archive", "bundle", "create", "delete", "list", "rename", "send", "show", "stop", "unarchive",
	})

	fleet, _, err := root.Find([]string{"fleet"})
	if err != nil {
		t.Fatal(err)
	}
	assertVisibleCommands(t, fleet, []string{"gc", "install", "serve", "status", "uninstall"})

	config, _, err := root.Find([]string{"config"})
	if err != nil {
		t.Fatal(err)
	}
	assertVisibleCommands(t, config, []string{"init"})
}

func TestManagedCLISelectorFlagsAreScopedToAgentCommands(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"agent", "cwd", "config", "models", "debug", "log-level", "verbose", "enable-user-agents-resources", "agent-id"} {
		if flag := root.PersistentFlags().Lookup(name); flag != nil {
			t.Errorf("root persistent flag --%s must not exist", name)
		}
	}

	for _, path := range [][]string{
		{"agent", "show"},
		{"agent", "start"},
		{"agent", "send"},
		{"thread", "list"},
		{"thread", "send"},
		{"diagnose"},
	} {
		command, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		if flag := command.Flags().Lookup("agent"); flag == nil {
			t.Errorf("%s missing --agent", command.CommandPath())
		}
		if flag := command.Flags().Lookup("cwd"); flag == nil || flag.Shorthand != "C" {
			t.Errorf("%s --cwd = %+v, want -C", command.CommandPath(), flag)
		}
	}

	for _, path := range [][]string{{"agent", "add"}, {"agent", "list"}, {"fleet", "status"}, {"version"}} {
		command, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		for _, name := range []string{"agent", "cwd"} {
			if flag := command.Flags().Lookup(name); flag != nil {
				t.Errorf("%s unexpectedly exposes --%s", command.CommandPath(), name)
			}
		}
	}

	initCommand, _, err := root.Find([]string{"config", "init"})
	if err != nil {
		t.Fatal(err)
	}
	if flag := initCommand.Flags().Lookup("cwd"); flag == nil || flag.Shorthand != "C" {
		t.Fatalf("config init --cwd = %+v, want local -C", flag)
	}
	if flag := initCommand.Flags().Lookup("agent"); flag != nil {
		t.Fatal("config init must not expose --agent")
	}

	listen, _, err := root.Find([]string{"listen"})
	if err != nil {
		t.Fatal(err)
	}
	if !listen.Hidden {
		t.Fatal("internal listen command must be hidden")
	}
	if flag := listen.Flags().Lookup("agent-id"); flag == nil || !flag.Hidden {
		t.Fatalf("listen --agent-id = %+v, want hidden local flag", flag)
	}
}

func TestManagedCLIRejectsCompetingSelectorSourcesBeforeWritingState(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("JUEX_HOME", home)

	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"agent", "show", "--agent", "aaaaaa", "--cwd", workspace})
	err := root.Execute()
	var usage *usageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %T %v, want selector usage error", err, err)
	}
	if _, err := os.Stat(filepath.Join(home, "agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selector error created Agent registry: %v", err)
	}
}

func TestManagedCLIUnregisteredWorkspaceDoesNotMint(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("JUEX_HOME", home)

	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"agent", "send", "--cwd", workspace, "hello"})
	err := root.Execute()
	var missing *notFoundError
	if !errors.As(err, &missing) || !strings.Contains(err.Error(), "juex agent add") {
		t.Fatalf("error = %T %v, want managed Agent not-found", err, err)
	}
	if _, err := os.Stat(filepath.Join(home, "agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unregistered send created Agent registry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".juex", "juex.local.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unregistered send created workspace marker: %v", err)
	}
}

func TestManagedCLIResolvesCanonicalWorkspaceFromDotAndSymlink(t *testing.T) {
	home := t.TempDir()
	rootDir := t.TempDir()
	workspace := filepath.Join(rootDir, "workspace")
	link := filepath.Join(rootDir, "workspace-link")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(workspace, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUEX_HOME", home)

	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"agent", "add", workspace})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	for _, cwd := range []string{link, "."} {
		if cwd == "." {
			t.Chdir(workspace)
		}
		_, state, err := resolveSelectedAgent(&agentSelectorFlags{cwd: cwd})
		if err != nil {
			t.Fatalf("resolve %q: %v", cwd, err)
		}
		canonical, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			t.Fatal(err)
		}
		if state.Workspace != canonical {
			t.Fatalf("resolve %q Workspace = %q, want %q", cwd, state.Workspace, canonical)
		}
	}
}

func TestManagedCLIUnknownExplicitAgentSuggestsDiscoveryOrRegistration(t *testing.T) {
	t.Setenv("JUEX_HOME", t.TempDir())
	_, _, err := resolveSelectedAgent(&agentSelectorFlags{agent: "missing"})
	var missing *notFoundError
	if !errors.As(err, &missing) || !strings.Contains(err.Error(), "juex agent list") || !strings.Contains(err.Error(), "juex agent add") {
		t.Fatalf("error = %T %v, want actionable Agent not-found", err, err)
	}
}

func TestManagedCLIThreadCreateAcceptsOnlyResourceOptions(t *testing.T) {
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"thread", "create", "unexpected positional input"})

	err := root.Execute()
	var usage *usageError
	if !errors.As(err, &usage) {
		t.Fatalf("error = %T %v, want usage error", err, err)
	}
}

func TestManagedCLIMapsAgentAPIErrorsToStableExitClasses(t *testing.T) {
	for _, test := range []struct {
		status int
		want   any
	}{
		{status: http.StatusBadRequest, want: &usageError{}},
		{status: http.StatusNotFound, want: &notFoundError{}},
		{status: http.StatusConflict, want: &conflictError{}},
	} {
		got := mapAgentAPIError(http.MethodGet, "/api/test", test.status, []byte(`{"error":"request_failed","message":"specific problem"}`))
		switch test.want.(type) {
		case *usageError:
			var target *usageError
			if !errors.As(got, &target) {
				t.Fatalf("status %d error = %T", test.status, got)
			}
		case *notFoundError:
			var target *notFoundError
			if !errors.As(got, &target) {
				t.Fatalf("status %d error = %T", test.status, got)
			}
		case *conflictError:
			var target *conflictError
			if !errors.As(got, &target) {
				t.Fatalf("status %d error = %T", test.status, got)
			}
		}
		if !strings.Contains(got.Error(), "specific problem") {
			t.Fatalf("status %d error = %q", test.status, got)
		}
	}
}

func TestManagedCLIRelativeAttachmentsResolveAgainstRegisteredWorkspace(t *testing.T) {
	workspace := filepath.Join(string(filepath.Separator), "workspace")
	if got, want := resolveAttachmentPath(workspace, "artifacts/report.txt"), filepath.Join(workspace, "artifacts/report.txt"); got != want {
		t.Fatalf("attachment path = %q, want %q", got, want)
	}
}

func assertVisibleCommands(t *testing.T, command *cobra.Command, want []string) {
	t.Helper()
	var got []string
	for _, child := range command.Commands() {
		if !child.Hidden {
			got = append(got, child.Name())
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s visible commands = %v, want %v", command.CommandPath(), got, want)
	}
}
