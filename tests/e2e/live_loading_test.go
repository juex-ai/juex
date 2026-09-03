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

	markerPath := filepath.Join(work, ".juex", "juex.local.json")
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })

	stdout, stderr, err := runAgentStateCommand(bin, home, work, "send", "--wait", "--json", "hello Main")
	if err != nil {
		t.Fatalf("juex send --wait: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"type":"input.terminal"`) || !strings.Contains(stdout, `"state":"succeeded"`) {
		t.Fatalf("send output missing terminal success:\n%s", stdout)
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var marker struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(data, &marker); err != nil || marker.AgentID == "" {
		t.Fatalf("workspace marker = %s, err=%v", data, err)
	}
	journal := []byte(threadJournalText(t, filepath.Join(home, "agents", marker.AgentID, "threads", "0")))
	for _, want := range []string{"hello Main", "send wait complete", `"thread_id":"0"`} {
		if !strings.Contains(string(journal), want) {
			t.Fatalf("Main Thread journal missing %q:\n%s", want, journal)
		}
	}
}

func TestLiveBinary_EndpointOnlyListenHasNoExtraTCPListener(t *testing.T) {
	bin := buildJuex(t)
	for _, test := range []struct {
		name               string
		args               []string
		scannerUnavailable bool
	}{
		{name: "flagless"},
		{name: "listener scanner unavailable", scannerUnavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := startLiveListen(t, bin, test.args...)
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

func TestLiveBinary_ExplicitListenTCPHasFriendlyPointer(t *testing.T) {
	bin := buildJuex(t)
	process := startLiveListen(t, bin, "--addr", "127.0.0.1:0")
	defer process.stop()

	target, err := endpoint.Parse(process.runtime.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	tcpAddress := waitForListenTCPAddress(t, process.stdout)
	assertProcessTCPListeners(t, process.cmd.Process.Pid, target, tcpAddress)

	for path, want := range map[string]int{
		"/healthz":          http.StatusOK,
		"/":                 http.StatusOK,
		"/some-browser-url": http.StatusOK,
		"/api/not-a-route":  http.StatusNotFound,
	} {
		response, err := http.Get("http://" + tcpAddress + path)
		if err != nil {
			t.Fatalf("GET TCP %s: %v", path, err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != want {
			t.Fatalf("GET TCP %s status = %d, want %d: %s", path, response.StatusCode, want, body)
		}
		if want == http.StatusOK && path != "/healthz" {
			for _, pointer := range []string{"agent JSON/SSE API", "no web UI", "juex fleet serve", "127.0.0.1:5839"} {
				if !strings.Contains(string(body), pointer) {
					t.Fatalf("GET TCP %s body missing %q:\n%s", path, pointer, body)
				}
			}
		}
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

func startLiveListen(t *testing.T, bin string, listenArgs ...string) *liveListenProcess {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "workspace")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(work, "juex.yaml")
	configBody := "models: [openai:test-model]\n" +
		"providers:\n" +
		"  - id: openai\n" +
		"    base_url: https://example.invalid\n" +
		"    api_key: test-key\n" +
		"    models:\n" +
		"      - id: test-model\n"
	if err := os.WriteFile(configFile, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	commandArgs := append([]string{"-C", work, "--config", configFile, "listen"}, listenArgs...)
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
		markerData, err := os.ReadFile(filepath.Join(work, ".juex", "juex.local.json"))
		if err == nil {
			var marker struct {
				AgentID string `json:"agent_id"`
			}
			if json.Unmarshal(markerData, &marker) == nil && marker.AgentID != "" {
				address, addressErr := agentstate.NewAgentAddress(home, marker.AgentID)
				if addressErr != nil {
					t.Fatal(addressErr)
				}
				runtimeState, err = endpoint.ReadRuntime(address)
				if err == nil {
					break
				}
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

func waitForListenTCPAddress(t *testing.T, output *lockedBuffer) string {
	t.Helper()
	const prefix = "juex listen agent JSON/SSE API (no web UI) listening on http://"
	body := waitForListenOutput(t, output, prefix)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("listen output did not contain a parseable TCP address:\n%s", body)
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
	commandArgs := append([]string{"-C", work}, args...)
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
