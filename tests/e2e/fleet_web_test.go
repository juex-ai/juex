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
	secondWorkspace := t.TempDir()
	secondAgentID := "bbbbbbbb"
	secondAgentDir := writeFleetE2EAgent(
		t,
		home,
		secondWorkspace,
		secondAgentID,
	)
	configPath := filepath.Join(workspace, ".juex", "juex.yaml")
	initialConfig := fleetWebConfig("old-model")
	if err := os.WriteFile(configPath, initialConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(secondWorkspace, ".juex", "juex.yaml"),
		fleetWebConfig("second-model"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	environment := fleetWebEnvironment(home)

	t.Cleanup(func() {
		for _, dir := range []string{agentDir, secondAgentDir} {
			runtimeState, err := endpoint.ReadRuntime(dir)
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = endpoint.RequestShutdown(ctx, runtimeState)
				cancel()
				process, _ := os.FindProcess(runtimeState.PID)
				_ = process.Kill()
			}
		}
	})

	for _, id := range []string{agentID, secondAgentID} {
		if stdout, stderr, err := runFleetE2E(binary, environment, "", "start", id); err != nil {
			t.Fatalf("fleet start %s: %v\nstdout:\n%s\nstderr:\n%s", id, err, stdout, stderr)
		}
	}
	firstRuntime := waitFleetRuntime(t, agentDir)
	probeFleetRuntime(t, firstRuntime)
	probeFleetRuntime(t, waitFleetRuntime(t, secondAgentDir))
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
	if len(roster) != 2 {
		t.Fatalf("fleet roster = %+v", roster)
	}
	health := make(map[string]fleet.RuntimeHealth, len(roster))
	for _, agent := range roster {
		health[agent.ID] = agent.RuntimeHealth
	}
	for _, id := range []string{agentID, secondAgentID} {
		if health[id] != fleet.RuntimeHealthy {
			t.Fatalf("fleet roster health[%s] = %q, roster = %+v", id, health[id], roster)
		}
	}

	for _, path := range []string{
		"/",
		"/agents/" + agentID,
		"/agents/" + agentID + "/sessions/arbitrary-session",
		"/agents/" + agentID + "/history",
		"/agents/" + agentID + "/runtime",
		"/agents/" + agentID + "/observables",
		"/agents/" + agentID + "/logs",
		"/agents/" + agentID + "/config",
		"/agents/" + secondAgentID,
	} {
		assertFleetSPA(t, client, baseURL+path)
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

func assertFleetSPA(t *testing.T, client *http.Client, rawURL string) {
	t.Helper()
	response, err := client.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", rawURL, response.StatusCode, body)
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "text/html") ||
		!strings.Contains(string(body), `<div id="root"></div>`) {
		t.Fatalf("GET %s did not return fleet SPA: %s", rawURL, body)
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
