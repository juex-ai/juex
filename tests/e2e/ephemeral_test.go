package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
)

func TestLiveBinary_EphemeralStateLifecycle(t *testing.T) {
	bin := buildJuex(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionResponse("ephemeral-ok")))
	}))
	defer provider.Close()

	t.Run("run cleans state in a fresh workspace", func(t *testing.T) {
		home, work, tempParent := prepareEphemeralBinaryTest(t, provider.URL)
		stdout, stderr, err := runEphemeralBinary(bin, ephemeralBinaryEnv(home, tempParent), work, "run", "--ephemeral", "--json", "hello")
		if err != nil {
			t.Fatalf("run --ephemeral: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		result := parseEphemeralRunResult(t, stdout)
		if _, err := os.Stat(result.SessionDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ephemeral session remains after exit: %v", err)
		}
		assertFreshWorkspaceHasNoIdentityWrites(t, home, work, tempParent)
	})

	t.Run("keep prints and retains state", func(t *testing.T) {
		home, work, tempParent := prepareEphemeralBinaryTest(t, provider.URL)
		stdout, stderr, err := runEphemeralBinary(bin, ephemeralBinaryEnv(home, tempParent), work, "run", "--ephemeral", "--keep", "--json", "hello")
		if err != nil {
			t.Fatalf("run --ephemeral --keep: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		result := parseEphemeralRunResult(t, stdout)
		stateDir := parseKeptStatePath(stderr)
		if stateDir == "" {
			t.Fatalf("stderr missing kept-state path:\n%s", stderr)
		}
		if filepath.Dir(filepath.Dir(result.SessionDir)) != stateDir {
			t.Fatalf("session dir = %q, kept state = %q", result.SessionDir, stateDir)
		}
		if info, err := os.Stat(stateDir); err != nil || !info.IsDir() {
			t.Fatalf("kept state is not inspectable: %v", err)
		}
		assertNoDurableIdentityWrites(t, home, work)
		if err := os.RemoveAll(filepath.Dir(filepath.Dir(stateDir))); err != nil {
			t.Fatal(err)
		}
		assertDirectoryEmptyE2E(t, tempParent)
	})

	t.Run("marked workspace state remains byte-identical", func(t *testing.T) {
		home, work, tempParent := prepareEphemeralBinaryTest(t, provider.URL)
		env := ephemeralBinaryEnv(home, tempParent)
		stdout, stderr, err := runEphemeralBinary(bin, env, work, "run", "--json", "durable")
		if err != nil {
			t.Fatalf("durable setup run: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		durable := parseEphemeralRunResult(t, stdout)
		agentDir := filepath.Dir(filepath.Dir(durable.SessionDir))
		beforeDigest := digestTree(t, agentDir)
		markerPath := filepath.Join(work, ".juex", "juex.local.json")
		markerBefore, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatal(err)
		}
		excludePath := filepath.Join(home, ".config", "git", "ignore")
		excludeBefore, err := os.ReadFile(excludePath)
		if err != nil {
			t.Fatal(err)
		}
		tempBefore := digestTree(t, tempParent)

		stdout, stderr, err = runEphemeralBinary(bin, env, work, "run", "--ephemeral", "--json", "isolated")
		if err != nil {
			t.Fatalf("marked ephemeral run: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		ephemeral := parseEphemeralRunResult(t, stdout)
		if _, err := os.Stat(ephemeral.SessionDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ephemeral session remains: %v", err)
		}
		if after := digestTree(t, agentDir); after != beforeDigest {
			t.Fatalf("durable agent state changed: before=%x after=%x", beforeDigest, after)
		}
		assertFileContentE2E(t, markerPath, markerBefore)
		assertFileContentE2E(t, excludePath, excludeBefore)
		if tempAfter := digestTree(t, tempParent); tempAfter != tempBefore {
			t.Fatalf("ephemeral run changed pre-existing temp locks: before=%x after=%x", tempBefore, tempAfter)
		}
	})

	t.Run("read-only commands do not mint", func(t *testing.T) {
		home, work, tempParent := prepareEphemeralBinaryTest(t, provider.URL)
		env := ephemeralBinaryEnv(home, tempParent)
		for _, args := range [][]string{
			{"sessions", "list"},
			{"bundle", "--session", "missing", "--out", filepath.Join(tempParent, "bundle.tar.gz")},
		} {
			stdout, stderr, err := runEphemeralBinary(bin, env, work, args...)
			if exitCode(err) != 3 || !strings.Contains(stderr, "no agent exists") {
				t.Fatalf("%v: exit=%d\nstdout:\n%s\nstderr:\n%s", args, exitCode(err), stdout, stderr)
			}
		}

		stdout, stderr, err := runEphemeralBinary(bin, env, work, "doctor", "--format", "json", "--offline")
		if exitCode(err) != 6 || !strings.Contains(stdout, `"name": "agent"`) || !strings.Contains(stdout, "no agent exists") {
			t.Fatalf("doctor: exit=%d\nstdout:\n%s\nstderr:\n%s", exitCode(err), stdout, stderr)
		}
		for _, args := range [][]string{{"version", "--json"}, {"schema"}} {
			stdout, stderr, err = runEphemeralBinary(bin, env, work, args...)
			if err != nil {
				t.Fatalf("%v: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout, stderr)
			}
		}
		assertFreshWorkspaceHasNoIdentityWrites(t, home, work, tempParent)
	})

	t.Run("repl cleans state after EOF", func(t *testing.T) {
		home, work, tempParent := prepareEphemeralBinaryTest(t, provider.URL)
		command := exec.Command(bin, "-C", work, "repl", "--ephemeral")
		command.Env = ephemeralBinaryEnv(home, tempParent)
		command.Stdin = strings.NewReader("hello\n")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("repl --ephemeral: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "ephemeral-ok") {
			t.Fatalf("repl output missing provider response:\n%s", output)
		}
		assertFreshWorkspaceHasNoIdentityWrites(t, home, work, tempParent)
	})
}

func TestLiveBinary_EphemeralServeEndpointAndCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt process signalling is platform-specific in this e2e")
	}
	bin := buildJuex(t)
	home, work, tempParent := prepareEphemeralBinaryTest(t, "https://example.invalid")
	env := ephemeralBinaryEnv(home, tempParent)
	command := exec.Command(bin, "-C", work, "serve", "--ephemeral")
	command.Env = env
	stdout := &lockedBuffer{}
	stderr := &lockedBuffer{}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	root, runtimeState := waitForEphemeralRuntime(t, tempParent, 10*time.Second)
	target, err := endpoint.Parse(runtimeState.Endpoint)
	if err != nil {
		_ = command.Process.Kill()
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target.URL("/healthz"), nil)
	if err != nil {
		_ = command.Process.Kill()
		t.Fatal(err)
	}
	response, err := target.NewClient().Do(request)
	if err != nil {
		_ = command.Process.Kill()
		t.Fatalf("GET ephemeral health: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_ = command.Process.Kill()
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}
	fleetOut, fleetErr, err := runEphemeralBinary(bin, env, "", "fleet", "status", "--format", "json")
	if err != nil || strings.TrimSpace(fleetOut) != "[]" {
		_ = command.Process.Kill()
		t.Fatalf("fleet saw ephemeral serve: %v\nstdout:\n%s\nstderr:\n%s", err, fleetOut, fleetErr)
	}

	if err := command.Process.Signal(os.Interrupt); err != nil {
		_ = command.Process.Kill()
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("ephemeral serve did not exit after interrupt")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ephemeral serve root remains: %v", err)
	}
	assertFreshWorkspaceHasNoIdentityWrites(t, home, work, tempParent)
}

type ephemeralRunResult struct {
	SessionDir string `json:"session_dir"`
}

func prepareEphemeralBinaryTest(t *testing.T, providerURL string) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "workspace")
	tempParent := filepath.Join(root, "tmp")
	for _, path := range []string{home, work, tempParent} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configBody := fmt.Sprintf(`model: local-chat:chat-test
providers:
  - id: local-chat
    protocol: openai/chat
    base_url: %s
    api_key: k
    capabilities:
      streaming: false
    models:
      - id: chat-test
`, providerURL)
	if err := writeText(filepath.Join(home, ".juex", "juex.yaml"), configBody); err != nil {
		t.Fatal(err)
	}
	return home, work, tempParent
}

func ephemeralBinaryEnv(home, tempParent string) []string {
	env := filteredEnv(
		"HOME", "USERPROFILE", "CODEX_HOME", "JUEX_HOME", "XDG_CONFIG_HOME",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "TMPDIR",
		"PROVIDER_API_ID", "PROVIDER_API_PROTOCOL", "PROVIDER_API_BASE",
		"PROVIDER_API_KEY", "PROVIDER_API_MODEL", "PROVIDER_THINKING_EFFORT",
		"PROVIDER_CONTEXT_WINDOW",
	)
	return append(env,
		"HOME="+home,
		"USERPROFILE="+home,
		"CODEX_HOME="+filepath.Join(home, "missing-codex-home"),
		"JUEX_HOME="+filepath.Join(home, ".juex"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"TMPDIR="+tempParent,
	)
}

func runEphemeralBinary(bin string, env []string, work string, args ...string) (string, string, error) {
	commandArgs := args
	if work != "" {
		commandArgs = append([]string{"-C", work}, args...)
	}
	command := exec.Command(bin, commandArgs...)
	command.Env = env
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

func parseEphemeralRunResult(t *testing.T, body string) ephemeralRunResult {
	t.Helper()
	var result ephemeralRunResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("parse run result: %v\n%s", err, body)
	}
	if result.SessionDir == "" {
		t.Fatalf("run result missing session_dir:\n%s", body)
	}
	return result
}

func parseKeptStatePath(stderr string) string {
	const prefix = "juex: kept ephemeral state at "
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func assertFreshWorkspaceHasNoIdentityWrites(t *testing.T, home, work, tempParent string) {
	t.Helper()
	assertNoDurableIdentityWrites(t, home, work)
	assertDirectoryEmptyE2E(t, work)
	assertDirectoryEmptyE2E(t, tempParent)
}

func assertNoDurableIdentityWrites(t *testing.T, home, work string) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(work, ".juex", "juex.local.json"),
		filepath.Join(home, ".juex", "agents"),
		filepath.Join(home, ".juex", ".locks"),
		filepath.Join(home, "gitconfig"),
		filepath.Join(home, ".config", "git", "ignore"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected durable identity path %s: %v", path, err)
		}
	}
}

func assertDirectoryEmptyE2E(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s entries = %v, want empty", path, entries)
	}
}

func assertFileContentE2E(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s changed:\ngot:  %q\nwant: %q", path, got, want)
	}
}

func digestTree(t *testing.T, root string) [sha256.Size]byte {
	t.Helper()
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", filepath.ToSlash(relative), entry.Type())
		if entry.Type().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, _ = hash.Write(body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func waitForEphemeralRuntime(t *testing.T, tempParent string, timeout time.Duration) (string, endpoint.Runtime) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		roots, _ := os.ReadDir(tempParent)
		for _, rootEntry := range roots {
			if !rootEntry.IsDir() || !strings.HasPrefix(rootEntry.Name(), "juex-ephemeral-") {
				continue
			}
			root := filepath.Join(tempParent, rootEntry.Name())
			agents, _ := os.ReadDir(filepath.Join(root, "agents"))
			for _, agentEntry := range agents {
				address, err := agentstate.NewAgentAddress(root, agentEntry.Name())
				if err != nil {
					continue
				}
				runtimeState, err := endpoint.ReadRuntime(address)
				if err == nil {
					return root, runtimeState
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("ephemeral runtime was not published under %s", tempParent)
	return "", endpoint.Runtime{}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
