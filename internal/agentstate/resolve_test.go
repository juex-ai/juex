package agentstate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestResolveCreatesAndReusesWorkspaceIdentity(t *testing.T) {
	home, workDir := prepareResolveTest(t)

	first, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created {
		t.Fatal("first resolution did not report a newly created identity")
	}
	if !regexp.MustCompile(`^[a-z2-7]{6}$`).MatchString(first.Agent.ID) {
		t.Fatalf("agent id = %q, want 6-character lowercase base32", first.Agent.ID)
	}
	if first.Agent.Name != filepath.Base(workDir) || first.Agent.Workspace != workDir {
		t.Fatalf("agent = %+v", first.Agent)
	}
	if !first.Agent.Enabled || first.Agent.Autostart || first.Agent.CreatedAt.IsZero() {
		t.Fatalf("agent defaults = %+v", first.Agent)
	}
	for _, path := range []string{
		first.Address.StateDir(),
		filepath.Join(first.Address.StateDir(), "sessions"),
		filepath.Join(first.Address.StateDir(), "logs"),
	} {
		assertDir(t, path)
	}
	entries, err := os.ReadDir(first.Address.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if got, want := strings.Join(names, ","), "agent.json,history.json,logs,sessions"; got != want {
		t.Fatalf("new Agent state entries = %q, want %q", got, want)
	}
	for _, path := range []string{
		filepath.Join(first.Address.StateDir(), "agent.json"),
		filepath.Join(first.Address.StateDir(), "history.json"),
		first.MarkerPath,
	} {
		assertFile(t, path)
	}

	var marker Marker
	readJSONTest(t, first.MarkerPath, &marker)
	if marker.AgentID != first.Agent.ID {
		t.Fatalf("marker = %+v, want id %q", marker, first.Agent.ID)
	}
	ignorePath := filepath.Join(home, ".config", "git", "ignore")
	assertContainsOnce(t, ignorePath, "**/juex.local.json")

	second, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created {
		t.Fatal("idempotent resolution reported a newly created identity")
	}
	if second.Agent.ID != first.Agent.ID || !second.Agent.CreatedAt.Equal(first.Agent.CreatedAt) {
		t.Fatalf("second resolution = %+v, first = %+v", second.Agent, first.Agent)
	}
	if len(second.Notices) != 0 {
		t.Fatalf("idempotent resolution notices = %v", second.Notices)
	}
	assertContainsOnce(t, ignorePath, "**/juex.local.json")
}

func TestResolveRetriesAgentIDCollision(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	collidingID := "aaaaaa"
	wantID := "bbbbbb"
	if err := os.MkdirAll(filepath.Join(home, "agents", collidingID), 0o755); err != nil {
		t.Fatal(err)
	}

	previousGenerateID := generateID
	t.Cleanup(func() { generateID = previousGenerateID })
	candidates := []string{collidingID, wantID}
	generateID = func() (string, error) {
		candidate := candidates[0]
		candidates = candidates[1:]
		return candidate, nil
	}

	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Agent.ID != wantID {
		t.Fatalf("agent id = %q, want second candidate %q", resolved.Agent.ID, wantID)
	}
	if len(candidates) != 0 {
		t.Fatalf("unused generated candidates = %v", candidates)
	}
	assertDir(t, filepath.Join(home, "agents", collidingID))
	assertFile(t, filepath.Join(home, "agents", wantID, agentFileName))
}

func TestResolveRejectsUnknownMarkerIdentity(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	markerPath := filepath.Join(workDir, ".juex", "juex.local.json")
	writeJSON(t, markerPath, Marker{AgentID: "abcd23"})

	_, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	var unknown *UnknownAgentError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want UnknownAgentError", err)
	}
	for _, want := range []string{"abcd23", markerPath, home, "restore"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want %q", err, want)
		}
	}
}

func TestResolveRejectsUnsafeMarkerIdentity(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	markerPath := filepath.Join(workDir, ".juex", "juex.local.json")
	writeJSON(t, markerPath, Marker{AgentID: "../../outside"})

	_, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err == nil || !strings.Contains(err.Error(), "invalid agent_id") {
		t.Fatalf("err = %v, want invalid agent_id", err)
	}
}

func TestResolveRebindsMovedWorkspace(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	first, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	movedDir := filepath.Join(filepath.Dir(workDir), "moved-workspace")
	if err := os.Rename(workDir, movedDir); err != nil {
		t.Fatal(err)
	}

	moved, err := Resolve(Options{HomeDir: home, WorkDir: movedDir})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Agent.ID != first.Agent.ID || moved.Agent.Workspace != movedDir {
		t.Fatalf("moved agent = %+v, first = %+v", moved.Agent, first.Agent)
	}
	if len(moved.Notices) != 1 || !strings.Contains(moved.Notices[0], "moved") {
		t.Fatalf("move notices = %v", moved.Notices)
	}
	var persisted Agent
	readJSONTest(t, filepath.Join(first.Address.StateDir(), "agent.json"), &persisted)
	if persisted.Workspace != movedDir {
		t.Fatalf("persisted workspace = %q, want %q", persisted.Workspace, movedDir)
	}
}

func TestResolveRejectsCopiedWorkspace(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	first, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	copyDir := filepath.Join(filepath.Dir(workDir), "copied-workspace")
	if err := os.MkdirAll(filepath.Join(copyDir, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(first.MarkerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copyDir, ".juex", "juex.local.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = Resolve(Options{HomeDir: home, WorkDir: copyDir})
	var copied *WorkspaceCopyError
	if !errors.As(err, &copied) {
		t.Fatalf("err = %v, want WorkspaceCopyError", err)
	}
	for _, want := range []string{workDir, copyDir, "remove", "juex.local.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want %q", err, want)
		}
	}
}

func TestResolveIgnoresWorkspaceRuntimeState(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	workspaceStateDir := filepath.Join(workDir, ".juex")
	files := map[string]string{
		filepath.Join("sessions", "s1", "conversation.jsonl"): "{\"id\":\"m1\",\"role\":\"user\"}\n",
		"history.json":                                     "{\"sessions\":[{\"id\":\"s1\"}]}\n",
		filepath.Join("logs", "listen.log"):                "ready\n",
		filepath.Join("observables", "observations.jsonl"): "{\"id\":\"o1\"}\n",
		"juex.yaml":        "models: [local:test]\n",
		"observables.json": "[]\n",
	}
	for rel, body := range files {
		writeText(t, filepath.Join(workspaceStateDir, rel), body)
	}

	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		assertText(t, filepath.Join(workspaceStateDir, rel), body)
	}
	for _, rel := range []string{
		filepath.Join("sessions", "s1", "conversation.jsonl"),
		filepath.Join("logs", "listen.log"),
		filepath.Join("observables", "observations.jsonl"),
	} {
		if _, err := os.Lstat(filepath.Join(resolved.Address.StateDir(), rel)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("agent state unexpectedly contains %s: %v", rel, err)
		}
	}
	assertText(t, filepath.Join(resolved.Address.StateDir(), "history.json"), "{\"sessions\":[]}\n")
	if len(resolved.Notices) != 0 {
		t.Fatalf("resolution notices = %v, want none", resolved.Notices)
	}
}

func TestResolveExistingIdentityIgnoresWorkspaceRuntimeState(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	first, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Join(workDir, ".juex", "observables", "observations.jsonl")
	writeText(t, workspacePath, "{\"id\":\"o1\"}\n")

	second, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if second.Agent.ID != first.Agent.ID || len(second.Notices) != 0 {
		t.Fatalf("second resolution = %+v, want same identity without notices", second)
	}
	assertText(t, workspacePath, "{\"id\":\"o1\"}\n")
	if _, err := os.Stat(filepath.Join(first.Address.StateDir(), "observables")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace observable state unexpectedly copied: %v", err)
	}
}

func TestResolveConcurrentFirstUseMintsOneIdentity(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	const callers = 8
	results := make([]Resolution, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = Resolve(Options{HomeDir: home, WorkDir: workDir})
		}()
	}
	wg.Wait()

	wantID := results[0].Agent.ID
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("Resolve[%d] error: %v", i, errs[i])
		}
		if results[i].Agent.ID != wantID {
			t.Fatalf("Resolve[%d] id = %q, want %q", i, results[i].Agent.ID, wantID)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, "agents"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != wantID {
		t.Fatalf("agent registry entries = %v, want only %q", entries, wantID)
	}
}

func TestResolveConcurrentFirstUseAcrossHomesPublishesOneMarker(t *testing.T) {
	homeOne, workDir := prepareResolveTest(t)
	homeTwo := filepath.Join(filepath.Dir(homeOne), "second-home")
	if err := os.MkdirAll(homeTwo, 0o755); err != nil {
		t.Fatal(err)
	}
	homes := []string{homeOne, homeTwo}
	results := make([]Resolution, len(homes))
	errs := make([]error, len(homes))
	var wg sync.WaitGroup
	for i := range homes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = Resolve(Options{HomeDir: homes[i], WorkDir: workDir})
		}()
	}
	wg.Wait()

	successes := 0
	unknowns := 0
	for i := range homes {
		if errs[i] == nil {
			successes++
			continue
		}
		var unknown *UnknownAgentError
		if errors.As(errs[i], &unknown) {
			unknowns++
			continue
		}
		t.Fatalf("Resolve[%d] unexpected error: %v", i, errs[i])
	}
	if successes != 1 || unknowns != 1 {
		t.Fatalf("successes=%d unknowns=%d results=%+v errs=%v", successes, unknowns, results, errs)
	}
}

func TestResolveUsesConfiguredGlobalExcludesFile(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	customIgnore := filepath.Join(home, "git", "global-ignore")
	globalConfig := filepath.Join(home, "gitconfig")
	writeText(t, globalConfig, "[core]\n\texcludesFile = "+filepath.ToSlash(customIgnore)+"\n")

	if _, err := Resolve(Options{HomeDir: home, WorkDir: workDir}); err != nil {
		t.Fatal(err)
	}
	assertContainsOnce(t, customIgnore, "**/juex.local.json")
	if _, err := os.Stat(filepath.Join(home, ".config", "git", "ignore")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default excludes file unexpectedly exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".gitignore")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository .gitignore unexpectedly exists: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(customIgnore))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(customIgnore) {
		t.Fatalf("custom excludes directory entries = %v, want only %s", entries, filepath.Base(customIgnore))
	}
}

func TestResolveRejectsSymlinkedAgentStateDir(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	first, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	stateDir := first.Address.StateDir()
	physical := filepath.Join(filepath.Dir(stateDir), "physical-agent-state")
	if err := os.Rename(stateDir, physical); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, stateDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err = Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Resolve() error = %v, want AgentStateDir symlink rejection", err)
	}
}

func TestResolveUsesXDGDefaultGlobalExcludesFile(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	xdgConfig := filepath.Join(home, "custom-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	if _, err := Resolve(Options{HomeDir: home, WorkDir: workDir}); err != nil {
		t.Fatal(err)
	}
	assertContainsOnce(t, filepath.Join(xdgConfig, "git", "ignore"), "**/juex.local.json")
	if _, err := os.Stat(filepath.Join(home, ".config", "git", "ignore")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("HOME fallback excludes file unexpectedly exists: %v", err)
	}
}

func prepareResolveTest(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workDir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var err error
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	workDir, err = filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return home, workDir
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, path, string(data)+"\n")
}

func readJSONTest(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func writeText(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", path)
	}
}

func assertFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file", path)
	}
}

func assertText(t *testing.T, gotPath, want string) {
	t.Helper()
	data, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", gotPath, data, want)
	}
}

func assertContainsOnce(t *testing.T, path, line string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), line); got != 1 {
		t.Fatalf("%s contains %q %d times:\n%s", path, line, got, data)
	}
}
