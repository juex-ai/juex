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
	if !regexp.MustCompile(`^[a-z2-7]{16}$`).MatchString(first.Agent.ID) {
		t.Fatalf("agent id = %q, want 16-character lowercase base32", first.Agent.ID)
	}
	if first.Agent.Name != filepath.Base(workDir) || first.Agent.Workspace != workDir {
		t.Fatalf("agent = %+v", first.Agent)
	}
	if !first.Agent.Enabled || first.Agent.Autostart || first.Agent.CreatedAt.IsZero() {
		t.Fatalf("agent defaults = %+v", first.Agent)
	}
	for _, path := range []string{
		first.AgentDir,
		filepath.Join(first.AgentDir, "sessions"),
		filepath.Join(first.AgentDir, "memory"),
		filepath.Join(first.AgentDir, "logs"),
	} {
		assertDir(t, path)
	}
	for _, path := range []string{
		filepath.Join(first.AgentDir, "agent.json"),
		filepath.Join(first.AgentDir, "history.json"),
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
	if second.Agent.ID != first.Agent.ID || !second.Agent.CreatedAt.Equal(first.Agent.CreatedAt) {
		t.Fatalf("second resolution = %+v, first = %+v", second.Agent, first.Agent)
	}
	if len(second.Notices) != 0 {
		t.Fatalf("idempotent resolution notices = %v", second.Notices)
	}
	assertContainsOnce(t, ignorePath, "**/juex.local.json")
}

func TestResolveRejectsUnknownMarkerIdentity(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	markerPath := filepath.Join(workDir, ".juex", "juex.local.json")
	writeJSON(t, markerPath, Marker{AgentID: "abcdefgh2345672a"})

	_, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	var unknown *UnknownAgentError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want UnknownAgentError", err)
	}
	for _, want := range []string{"abcdefgh2345672a", markerPath, home, "restore"} {
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
	readJSONTest(t, filepath.Join(first.AgentDir, "agent.json"), &persisted)
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

func TestResolveMigratesLegacyStateAndPreservesWorkspaceConfig(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	legacyDir := filepath.Join(workDir, ".juex")
	writeText(t, filepath.Join(legacyDir, "sessions", "s1", "conversation.jsonl"), "{\"role\":\"user\"}\n")
	writeText(t, filepath.Join(legacyDir, "memory", "MEMORY.md"), "# durable\n")
	writeText(t, filepath.Join(legacyDir, "history.json"), "{\"sessions\":[]}\n")
	writeText(t, filepath.Join(legacyDir, "logs", "serve.log"), "ready\n")
	writeText(t, filepath.Join(legacyDir, "juex.yaml"), "model: local:test\n")
	writeText(t, filepath.Join(legacyDir, "observables.json"), "[]\n")
	writeText(t, filepath.Join(legacyDir, "artifacts", "keep.txt"), "workspace-local\n")

	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		filepath.Join("sessions", "s1", "conversation.jsonl"): "{\"role\":\"user\"}\n",
		filepath.Join("memory", "MEMORY.md"):                  "# durable\n",
		"history.json":                                        "{\"sessions\":[]}\n",
		filepath.Join("logs", "serve.log"):                    "ready\n",
	} {
		assertText(t, filepath.Join(resolved.AgentDir, rel), want)
		if _, err := os.Lstat(filepath.Join(legacyDir, rel)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy %s still exists or stat failed: %v", rel, err)
		}
	}
	for _, rel := range []string{"juex.yaml", "observables.json", filepath.Join("artifacts", "keep.txt")} {
		assertFile(t, filepath.Join(legacyDir, rel))
	}
	if len(resolved.Notices) != 1 || !strings.Contains(resolved.Notices[0], "migrated") {
		t.Fatalf("migration notices = %v", resolved.Notices)
	}
}

func TestResolveMigratesSymlinkWithoutFollowingIt(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink creation is not generally available to unprivileged Windows tests")
	}
	home, workDir := prepareResolveTest(t)
	target := filepath.Join(workDir, "shared-memory.md")
	writeText(t, target, "# shared\n")
	legacyLink := filepath.Join(workDir, ".juex", "memory", "shared.md")
	if err := os.MkdirAll(filepath.Dir(legacyLink), 0o755); err != nil {
		t.Fatal(err)
	}
	linkTarget := filepath.Join("..", "..", "shared-memory.md")
	if err := os.Symlink(linkTarget, legacyLink); err != nil {
		t.Fatal(err)
	}

	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	migratedLink := filepath.Join(resolved.AgentDir, "memory", "shared.md")
	info, err := os.Lstat(migratedLink)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s mode = %s, want symlink", migratedLink, info.Mode())
	}
	gotTarget, err := os.Readlink(migratedLink)
	if err != nil {
		t.Fatal(err)
	}
	if gotTarget != linkTarget {
		t.Fatalf("symlink target = %q, want %q", gotTarget, linkTarget)
	}
}

func TestResolvePreservesLegacyStateWhenVerificationFails(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	legacyPath := filepath.Join(workDir, ".juex", "memory", "MEMORY.md")
	writeText(t, legacyPath, "keep me\n")
	originalVerify := verifyCopiedTree
	verifyCopiedTree = func(_, _ string) error {
		return errors.New("injected verification failure")
	}
	t.Cleanup(func() { verifyCopiedTree = originalVerify })

	_, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err == nil || !strings.Contains(err.Error(), "injected verification failure") {
		t.Fatalf("err = %v, want verification failure", err)
	}
	assertFile(t, legacyPath)
	if _, err := os.Stat(filepath.Join(workDir, ".juex", "juex.local.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker exists after failed migration: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "agents"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("agent registry entries after rollback = %v", entries)
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
	writeText(t, globalConfig, "[core]\n\texcludesFile = "+customIgnore+"\n")

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
