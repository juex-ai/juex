package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/fleet"
)

func TestFleetWebProxyAndConfigRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary fleet web test is slow")
	}
	binary := buildJuex(t)
	home, err := os.MkdirTemp("", "jfw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	workspace := t.TempDir()
	agentID := "aaaaaaaa"
	agentDir := writeFleetE2EAgent(t, home, workspace, agentID)
	configPath := filepath.Join(workspace, ".juex", "juex.yaml")
	initialConfig := fleetWebConfig("old-model")
	if err := os.WriteFile(configPath, initialConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	environment := fleetWebEnvironment(home)

	t.Cleanup(func() {
		runtimeState, err := endpoint.ReadRuntime(agentDir)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = endpoint.RequestShutdown(ctx, runtimeState)
			cancel()
			process, _ := os.FindProcess(runtimeState.PID)
			_ = process.Kill()
		}
	})

	if stdout, stderr, err := runFleetE2E(binary, environment, "", "start", agentID); err != nil {
		t.Fatalf("fleet start: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	firstRuntime := waitFleetRuntime(t, agentDir)
	probeFleetRuntime(t, firstRuntime)
	if runtime.GOOS != "windows" && !strings.HasPrefix(firstRuntime.Endpoint, "unix://") {
		t.Fatalf("headless agent endpoint = %q, want Unix socket", firstRuntime.Endpoint)
	}

	supervisor := startFleetSupervisor(t, binary, environment)
	t.Cleanup(func() {
		if supervisor.cmd.ProcessState == nil {
			_ = supervisor.cmd.Process.Kill()
			_ = supervisor.cmd.Wait()
		}
	})
	baseURL := "http://" + waitFleetWebReady(t, supervisor)
	client := &http.Client{Timeout: 30 * time.Second}

	var roster []fleet.AgentStatus
	fleetWebJSON(t, client, http.MethodGet, baseURL+"/api/agents", "", http.StatusOK, &roster)
	if len(roster) != 1 ||
		roster[0].ID != agentID ||
		roster[0].RuntimeHealth != fleet.RuntimeHealthy {
		t.Fatalf("fleet roster = %+v", roster)
	}

	var runtimeStatus struct {
		Provider struct {
			Model string `json:"model"`
		} `json:"provider"`
	}
	fleetWebJSON(
		t,
		client,
		http.MethodGet,
		baseURL+"/agents/"+agentID+"/api/runtime",
		"",
		http.StatusOK,
		&runtimeStatus,
	)
	if runtimeStatus.Provider.Model != "old-model" {
		t.Fatalf("initial proxied model = %q", runtimeStatus.Provider.Model)
	}

	var sessions struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	fleetWebJSON(
		t,
		client,
		http.MethodGet,
		baseURL+"/agents/"+agentID+"/api/sessions",
		"",
		http.StatusOK,
		&sessions,
	)
	if len(sessions.Sessions) == 0 || sessions.Sessions[0].ID == "" {
		t.Fatalf("proxied sessions = %+v", sessions)
	}
	assertFleetSSEHeaders(
		t,
		baseURL+"/agents/"+agentID+"/api/sessions/"+sessions.Sessions[0].ID+"/events",
	)

	invalid := `{"content":"model: [invalid"}`
	fleetWebJSON(
		t,
		client,
		http.MethodPut,
		baseURL+"/api/agents/"+agentID+"/config",
		invalid,
		http.StatusBadRequest,
		nil,
	)
	afterInvalid, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterInvalid) != string(initialConfig) {
		t.Fatalf("invalid update changed config:\n%s", afterInvalid)
	}
	unchangedRuntime := waitFleetRuntime(t, agentDir)
	if !unchangedRuntime.Matches(firstRuntime) {
		t.Fatalf("invalid update restarted runtime: before=%+v after=%+v", firstRuntime, unchangedRuntime)
	}

	validBody, err := json.Marshal(map[string]string{
		"content": string(fleetWebConfig("new-model")),
	})
	if err != nil {
		t.Fatal(err)
	}
	var update struct {
		Config fleet.AgentConfig `json:"config"`
		Agent  fleet.AgentStatus `json:"agent"`
	}
	fleetWebJSON(
		t,
		client,
		http.MethodPut,
		baseURL+"/api/agents/"+agentID+"/config",
		string(validBody),
		http.StatusOK,
		&update,
	)
	if !update.Config.Exists ||
		!strings.Contains(update.Config.Content, "new-model") ||
		update.Agent.RuntimeHealth != fleet.RuntimeHealthy {
		t.Fatalf("config update response = %+v", update)
	}
	secondRuntime := waitFleetRuntime(t, agentDir)
	if secondRuntime.InstanceID == firstRuntime.InstanceID {
		t.Fatalf("config update reused runtime instance %q", secondRuntime.InstanceID)
	}

	runtimeStatus.Provider.Model = ""
	fleetWebJSON(
		t,
		client,
		http.MethodGet,
		baseURL+"/agents/"+agentID+"/api/runtime",
		"",
		http.StatusOK,
		&runtimeStatus,
	)
	if runtimeStatus.Provider.Model != "new-model" {
		t.Fatalf("updated proxied model = %q", runtimeStatus.Provider.Model)
	}
}

func fleetWebJSON(
	t *testing.T,
	client *http.Client,
	method, rawURL, body string,
	wantStatus int,
	target any,
) {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, body = %s", method, rawURL, response.StatusCode, data)
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
	}
}

func assertFleetSSEHeaders(t *testing.T, rawURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open proxied SSE: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("SSE content type = %q", got)
	}
}

func fleetWebEnvironment(home string) []string {
	environment := filteredEnv(
		"HOME",
		"USERPROFILE",
		"JUEX_HOME",
		"GIT_CONFIG_GLOBAL",
		"GIT_CONFIG_NOSYSTEM",
		"PROVIDER_API_ID",
		"PROVIDER_API_PROTOCOL",
		"PROVIDER_API_BASE",
		"PROVIDER_API_KEY",
		"PROVIDER_API_MODEL",
		"PROVIDER_THINKING_EFFORT",
	)
	return append(
		environment,
		"HOME="+home,
		"USERPROFILE="+home,
		"JUEX_HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
	)
}

func fleetWebConfig(model string) []byte {
	return []byte(`model: local:` + model + `
providers:
  - id: local
    protocol: openai/chat
    base_url: http://127.0.0.1:1
    api_key: test-key
    models:
      - id: ` + model + `
`)
}
