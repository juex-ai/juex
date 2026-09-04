package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
)

func TestLiveBinary_SendWaitUsesMainThreadJournal(t *testing.T) {
	bin := buildJuex(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatCompletionResponse("send wait complete"))
	}))
	defer provider.Close()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(work, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := strings.ReplaceAll(`models: [local-chat:chat-test]
providers:
  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: test-key
    capabilities:
      streaming: false
    models:
      - id: chat-test
`, "BASE_URL", provider.URL)
	if err := os.WriteFile(filepath.Join(work, ".juex", "juex.yaml"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })

	stdout, stderr, err := runAgentStateCommand(bin, home, work, "agent", "send", "--wait", "--json", "hello Main")
	if err != nil {
		t.Fatalf("juex agent send --wait: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"type":"input.terminal"`) || !strings.Contains(stdout, `"state":"succeeded"`) {
		t.Fatalf("send output missing terminal success:\n%s", stdout)
	}
	resolution, err := agentstate.ResolveExisting(agentstate.Options{HomeDir: home, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	journal := []byte(threadJournalText(t, filepath.Join(home, "agents", resolution.Agent.ID, "threads", "0")))
	for _, want := range []string{"hello Main", "send wait complete", `"thread_id":"0"`} {
		if !strings.Contains(string(journal), want) {
			t.Fatalf("Main Thread journal missing %q:\n%s", want, journal)
		}
	}
}

func TestLiveBinary_HiddenAgentRuntimeHasNoExtraTCPListener(t *testing.T) {
	bin := buildJuex(t)
	for _, test := range []struct {
		name               string
		scannerUnavailable bool
	}{
		{name: "flagless"},
		{name: "listener scanner unavailable", scannerUnavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := startLiveListen(t, bin)
			defer process.stop()

			target, err := endpoint.Parse(process.runtime.Endpoint)
			if err != nil {
				t.Fatal(err)
			}
			if test.scannerUnavailable {
				t.Setenv("PATH", t.TempDir())
			}
			assertProcessTCPListeners(t, process.cmd.Process.Pid, target)
			waitForListenOutput(t, process.stdout, "juex listen agent endpoint listening on ")
			if strings.Contains(process.stdout.String(), "agent JSON/SSE API (no web UI)") {
				t.Fatalf("endpoint-only listen reported a TCP API:\n%s", process.stdout.String())
			}

			client := target.NewClient()
			for path, want := range map[string]int{"/healthz": http.StatusOK, "/": http.StatusNotFound} {
				request, err := http.NewRequest(http.MethodGet, target.URL(path), nil)
				if err != nil {
					t.Fatal(err)
				}
				requestCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				response, err := client.Do(request.WithContext(requestCtx))
				cancel()
				if err != nil {
					t.Fatalf("GET %s through %s: %v", path, process.runtime.Endpoint, err)
				}
				_ = response.Body.Close()
				if response.StatusCode != want {
					t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, want)
				}
			}
		})
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type liveListenProcess struct {
	cmd     *exec.Cmd
	done    chan error
	stdout  *lockedBuffer
	stderr  *lockedBuffer
	runtime endpoint.Runtime
	once    sync.Once
}

func startLiveListen(t *testing.T, bin string) *liveListenProcess {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "workspace")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(work, ".juex", "juex.yaml")
	configBody := "models: [openai:test-model]\n" +
		"providers:\n" +
		"  - id: openai\n" +
		"    base_url: https://example.invalid\n" +
		"    api_key: test-key\n" +
		"    models:\n" +
		"      - id: test-model\n"
	if err := os.MkdirAll(filepath.Dir(configFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	resolution, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	commandArgs := []string{"listen", "--agent-id", resolution.Agent.ID}
	cmd := exec.Command(bin, commandArgs...)
	cmd.Env = filteredEnv(
		"HOME", "USERPROFILE", "JUEX_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM",
	)
	cmd.Env = append(cmd.Env,
		"HOME="+home,
		"USERPROFILE="+home,
		"JUEX_HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
	)
	stdout := &lockedBuffer{}
	stderr := &lockedBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runtimeState endpoint.Runtime
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf(
				"listen exited before readiness: %v\nstdout:\n%s\nstderr:\n%s",
				err,
				stdout.String(),
				stderr.String(),
			)
		default:
		}
		resolution, err := agentstate.ResolveExisting(agentstate.Options{HomeDir: home, WorkDir: work})
		if err == nil {
			runtimeState, err = endpoint.ReadRuntime(resolution.Address)
			if err == nil {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runtimeState.Endpoint == "" {
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("runtime endpoint was not published\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	return &liveListenProcess{
		cmd:     cmd,
		done:    done,
		stdout:  stdout,
		stderr:  stderr,
		runtime: runtimeState,
	}
}

func (p *liveListenProcess) stop() {
	p.once.Do(func() {
		_ = p.cmd.Process.Kill()
		<-p.done
	})
}

func waitForListenOutput(t *testing.T, output *lockedBuffer, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body := output.String()
		if strings.Contains(body, want) {
			return body
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listen output missing %q:\n%s", want, output.String())
	return ""
}

var errProcessListenerScanUnavailable = errors.New("process TCP listener scan unavailable")

func assertProcessTCPListeners(t *testing.T, pid int, endpointTarget endpoint.Target, additional ...string) {
	t.Helper()
	want := append([]string(nil), additional...)
	if endpointTarget.Network() == "tcp" {
		want = append(want, endpointTarget.Address())
	}
	deadline := time.Now().Add(5 * time.Second)
	var (
		got     []string
		scanErr error
	)
	for time.Now().Before(deadline) {
		got, scanErr = processTCPListeners(pid)
		if errors.Is(scanErr, errProcessListenerScanUnavailable) {
			t.Logf("skipping process listener scan: %v", scanErr)
			return
		}
		if scanErr == nil && sameStringSet(got, want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("TCP listeners for pid %d = %v, want %v (scan error: %v)", pid, got, want, scanErr)
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, item := range left {
		counts[item]++
	}
	for _, item := range right {
		counts[item]--
		if counts[item] < 0 {
			return false
		}
	}
	return true
}

func runAgentStateCommand(bin, home, work string, args ...string) (string, string, error) {
	if _, stderr, err := runJuexHomeCommand(bin, home, "agent", "add", work); err != nil {
		return "", stderr, err
	}
	commandArgs := append([]string(nil), args...)
	if len(commandArgs) >= 2 && (commandArgs[0] == "agent" || commandArgs[0] == "thread") {
		commandArgs = append([]string{commandArgs[0], commandArgs[1], "--cwd", work}, commandArgs[2:]...)
	}
	return runJuexHomeCommand(bin, home, commandArgs...)
}

func runJuexHomeCommand(bin, home string, args ...string) (string, string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = filteredEnv(
		"HOME", "USERPROFILE", "JUEX_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM",
	)
	cmd.Env = append(cmd.Env,
		"HOME="+home,
		"USERPROFILE="+home,
		"JUEX_HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func filteredEnv(remove ...string) []string {
	removed := make(map[string]struct{}, len(remove))
	for _, key := range remove {
		removed[key] = struct{}{}
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, skip := removed[key]; !skip {
			env = append(env, entry)
		}
	}
	return env
}

// buildJuex compiles the real juex binary into the test's tempdir.
func buildJuex(t *testing.T) string {
	t.Helper()
	name := "juex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(t.TempDir(), name)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/juex")
	cmd.Dir = root
	if buildOut, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build juex: %v\n%s", err, buildOut)
	}
	return out
}

func writeMCPConfig(workDir, command string, args []string) error {
	return writeMCPConfigFile(filepath.Join(workDir, ".agents", "mcp.json"), "local", command, args)
}

func writeMCPConfigFile(path, serverName, command string, args []string) error {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			serverName: map[string]any{
				"command": command,
				"args":    args,
			},
		},
	}
	body, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

// findRepoRoot walks up from cwd until it sees go.mod.
func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}
