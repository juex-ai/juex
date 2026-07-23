package agentstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveExistingMissingMarkerHasNoSideEffects(t *testing.T) {
	home, workDir := prepareResolveTest(t)

	_, err := ResolveExisting(Options{HomeDir: home, WorkDir: workDir})
	var noAgent *NoAgentError
	if !errors.As(err, &noAgent) {
		t.Fatalf("err = %v, want NoAgentError", err)
	}
	if noAgent.WorkDir != workDir {
		t.Fatalf("no-agent workspace = %q, want %q", noAgent.WorkDir, workDir)
	}
	assertDirectoryEmpty(t, workDir)
	assertDirectoryEmpty(t, home)
}

func TestResolveExistingReadsIdentityWithoutMaintenanceWrites(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	markerBefore, err := os.ReadFile(resolved.MarkerPath)
	if err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(resolved.Address.StateDir(), agentFileName)
	agentBefore, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	excludePath := filepath.Join(home, ".config", "git", "ignore")
	excludeBefore, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".locks")); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(workDir, ".juex", "sessions", "legacy", "conversation.jsonl")
	writeText(t, legacyPath, "legacy\n")

	existing, err := ResolveExisting(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if existing.Agent.ID != resolved.Agent.ID || existing.Address.StateDir() != resolved.Address.StateDir() || existing.Created {
		t.Fatalf("existing resolution = %+v, durable = %+v", existing, resolved)
	}
	assertFileBytes(t, resolved.MarkerPath, markerBefore)
	assertFileBytes(t, agentPath, agentBefore)
	assertFileBytes(t, excludePath, excludeBefore)
	assertFileBytes(t, legacyPath, []byte("legacy\n"))
	if _, err := os.Stat(filepath.Join(home, ".locks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only resolution created locks: %v", err)
	}
	if _, err := os.Stat(filepath.Join(resolved.Address.StateDir(), "sessions", "legacy")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only resolution migrated legacy state: %v", err)
	}
}

func TestResolveExistingRequiresStatefulRebind(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(resolved.Address.StateDir(), agentFileName)
	agentBefore, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	movedDir := filepath.Join(filepath.Dir(workDir), "moved")
	if err := os.Rename(workDir, movedDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".locks")); err != nil {
		t.Fatal(err)
	}

	_, err = ResolveExisting(Options{HomeDir: home, WorkDir: movedDir})
	var rebind *RebindRequiredError
	if !errors.As(err, &rebind) {
		t.Fatalf("err = %v, want RebindRequiredError", err)
	}
	for _, want := range []string{resolved.Agent.ID, workDir, movedDir, "run", "repl", "serve"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want %q", err, want)
		}
	}
	assertFileBytes(t, agentPath, agentBefore)
	if _, err := os.Stat(filepath.Join(home, ".locks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only rebind check created locks: %v", err)
	}
}

func TestCreateEphemeralOwnsIsolatedRemovableState(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	tempParent := filepath.Join(filepath.Dir(home), "ephemeral")
	if err := os.MkdirAll(tempParent, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tempParent)

	state, err := CreateEphemeral(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Resolution.Agent.ID == "" || state.Resolution.Address.StateDir() == "" {
		t.Fatalf("ephemeral resolution = %+v", state.Resolution)
	}
	if filepath.Dir(filepath.Dir(state.Resolution.Address.StateDir())) != state.RootDir {
		t.Fatalf("agent dir = %q, root = %q", state.Resolution.Address.StateDir(), state.RootDir)
	}
	if filepath.Base(filepath.Dir(state.Resolution.Address.StateDir())) != "agents" {
		t.Fatalf("agent dir = %q, want <root>/agents/<id>", state.Resolution.Address.StateDir())
	}
	if strings.HasPrefix(state.Resolution.Address.StateDir(), filepath.Join(home, "agents")+string(os.PathSeparator)) {
		t.Fatalf("ephemeral state leaked into durable registry: %s", state.Resolution.Address.StateDir())
	}
	assertDir(t, state.Resolution.Address.StateDir())
	if _, err := os.Stat(filepath.Join(home, "agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("durable registry created: %v", err)
	}

	root := state.RootDir
	if err := state.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ephemeral root remains after Remove: %v", err)
	}
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s entries = %v, want empty", path, entries)
	}
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s changed:\ngot:  %q\nwant: %q", path, got, want)
	}
}
