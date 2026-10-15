package agentstate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

func TestResolveCreatesAndReusesWorkspaceIdentity(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	workspaceConfig := filepath.Join(workDir, ".juex", "juex.yaml")
	writeText(t, workspaceConfig, "models: []\n")
	first, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || !regexp.MustCompile(`^[a-z2-7]{6}$`).MatchString(first.Agent.ID) {
		t.Fatalf("first resolution = %+v", first)
	}
	canonicalWorkDir, err := canonicalPath(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Agent.Name != filepath.Base(workDir) || first.Agent.Workspace != canonicalWorkDir || !first.Agent.Enabled || first.Agent.Autostart || first.Agent.CreatedAt.IsZero() {
		t.Fatalf("agent = %+v", first.Agent)
	}
	for _, path := range []string{first.Address.StateDir(), filepath.Join(first.Address.StateDir(), "threads"), filepath.Join(first.Address.StateDir(), "archive", "threads"), filepath.Join(first.Address.StateDir(), "logs"), filepath.Join(first.Address.StateDir(), agentFileName)} {
		assertPath(t, path)
	}
	assertText(t, workspaceConfig, "models: []\n")
	second, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Agent.ID != first.Agent.ID || !second.Agent.CreatedAt.Equal(first.Agent.CreatedAt) {
		t.Fatalf("second resolution = %+v, first = %+v", second, first)
	}
}

func TestResolveRetriesAgentIDCollision(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	if err := os.MkdirAll(filepath.Join(home, "agents", "aaaaaa"), 0o755); err != nil {
		t.Fatal(err)
	}
	previousGenerateID := generateID
	t.Cleanup(func() { generateID = previousGenerateID })
	candidates := []string{"aaaaaa", "bbbbbb"}
	generateID = func() (string, error) {
		candidate := candidates[0]
		candidates = candidates[1:]
		return candidate, nil
	}
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Agent.ID != "bbbbbb" {
		t.Fatalf("agent id = %q, want bbbbbb", resolved.Agent.ID)
	}
}

func TestResolveExistingDoesNotMint(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	_, err := ResolveExisting(Options{HomeDir: home, WorkDir: workDir})
	var missing *NoAgentError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want NoAgentError", err)
	}
	entries, listErr := ListRegistry(home)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("ResolveExisting minted entries: %+v", entries)
	}
}

func TestResolveConcurrentFirstUseMintsOneIdentity(t *testing.T) {
	home, workDir := prepareResolveTest(t)
	const workers = 12
	results := make(chan Resolution, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resolved, err := Resolve(Options{HomeDir: home, WorkDir: workDir})
			results <- resolved
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]struct{}{}
	created := 0
	for result := range results {
		ids[result.Agent.ID] = struct{}{}
		if result.Created {
			created++
		}
	}
	if len(ids) != 1 || created != 1 {
		t.Fatalf("ids = %v created = %d", ids, created)
	}
}

func TestResolveKeepsHomesIndependent(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := Resolve(Options{HomeDir: filepath.Join(root, "home-a"), WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(Options{HomeDir: filepath.Join(root, "home-b"), WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if first.Address.StateDir() == second.Address.StateDir() {
		t.Fatalf("independent homes shared state: %+v %+v", first, second)
	}
}

func prepareResolveTest(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, workDir
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
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

func assertPath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func assertText(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
