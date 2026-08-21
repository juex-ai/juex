package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/fleetweb"
	"github.com/juex-ai/juex/internal/processmetrics"
)

func TestFleetRegistrationLifecycleThroughAPIAndCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary fleet registration lifecycle is slow")
	}
	binary := buildJuex(t)
	home := t.TempDir()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	unknownWorkspace := filepath.Join(root, "unknown-marker")
	for _, path := range []string{workspace, unknownWorkspace} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(workspace, ".juex", "juex.yaml"),
		fleetWebConfig("registration-model"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(unknownWorkspace, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFleetE2EJSON(
		t,
		filepath.Join(unknownWorkspace, ".juex", "juex.local.json"),
		map[string]string{"agent_id": "aaaaaa"},
	)
	environment := fleetWebEnvironment(home)
	supervisor := startFleetSupervisor(t, binary, environment)
	t.Cleanup(func() {
		if supervisor.cmd.ProcessState == nil {
			_ = supervisor.cmd.Process.Kill()
			_ = supervisor.cmd.Wait()
		}
	})
	baseURL := "http://" + waitFleetWebReady(t, supervisor)
	client := &http.Client{Timeout: 30 * time.Second}

	var listing fleetweb.DirectoryListing
	fleetWebJSON(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/fs/dirs?path="+url.QueryEscape(root),
		"",
		http.StatusOK,
		&listing,
	)
	registered := make(map[string]bool, len(listing.Dirs))
	for _, dir := range listing.Dirs {
		registered[dir.Name] = dir.Registered
	}
	if registered["workspace"] || !registered["unknown-marker"] {
		t.Fatalf("directory registration markers = %+v", registered)
	}

	createBody, err := json.Marshal(map[string]any{
		"workspace": workspace,
		"name":      "managed",
		"autostart": true,
		"start":     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var added fleet.AddResult
	fleetWebJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/agents",
		string(createBody),
		http.StatusCreated,
		&added,
	)
	if !added.Created ||
		added.Agent.Name != "managed" ||
		added.Agent.RuntimeHealth != fleet.RuntimeHealthy {
		t.Fatalf("created agent = %+v", added)
	}
	agentAddress, err := agentstate.NewAgentAddress(home, added.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	agentDir := agentAddress.StateDir()
	runtimeState := waitFleetRuntime(t, agentAddress)
	removedSuccessfully := false
	t.Cleanup(func() {
		if removedSuccessfully {
			return
		}
		process, _ := os.FindProcess(runtimeState.PID)
		_ = process.Kill()
	})

	var repeated fleet.AddResult
	fleetWebJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/agents",
		string(createBody),
		http.StatusOK,
		&repeated,
	)
	if repeated.Created || repeated.Agent.ID != added.Agent.ID {
		t.Fatalf("idempotent add = %+v, first = %+v", repeated, added)
	}

	unknownBody, err := json.Marshal(map[string]string{"workspace": unknownWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	fleetWebJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/agents",
		string(unknownBody),
		http.StatusConflict,
		nil,
	)

	var disabled fleet.AgentStatus
	fleetWebJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/agents/"+added.Agent.ID+"/disable",
		"",
		http.StatusOK,
		&disabled,
	)
	if disabled.Enabled || disabled.RuntimeHealth != fleet.RuntimeStopped {
		t.Fatalf("disabled agent = %+v", disabled)
	}
	var enabled fleet.AgentStatus
	fleetWebJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/agents/"+added.Agent.ID+"/enable",
		"",
		http.StatusOK,
		&enabled,
	)
	if !enabled.Enabled || enabled.RuntimeHealth != fleet.RuntimeStopped {
		t.Fatalf("enabled agent = %+v", enabled)
	}
	fleetWebJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/agents/"+added.Agent.ID+"/start",
		"",
		http.StatusOK,
		&enabled,
	)
	if enabled.RuntimeHealth != fleet.RuntimeHealthy {
		t.Fatalf("restarted agent = %+v", enabled)
	}

	fleetWebJSON(
		t,
		client,
		http.MethodDelete,
		baseURL+"/api/agents/"+added.Agent.ID,
		`{"confirm":"wrong"}`,
		http.StatusBadRequest,
		nil,
	)
	for _, path := range []string{
		agentDir,
		filepath.Join(workspace, ".juex", "juex.local.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("rejected removal changed %s: %v", path, err)
		}
	}

	var removed fleet.RemovedAgent
	fleetWebJSON(
		t,
		client,
		http.MethodDelete,
		baseURL+"/api/agents/"+added.Agent.ID,
		`{"confirm":"managed"}`,
		http.StatusOK,
		&removed,
	)
	if removed.ID != added.Agent.ID {
		t.Fatalf("removed agent = %+v", removed)
	}
	removedSuccessfully = true
	for _, path := range []string{
		agentDir,
		filepath.Join(workspace, ".juex", "juex.local.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("removed path still exists %s: %v", path, err)
		}
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	probeErr := endpoint.Probe(probeCtx, runtimeState)
	cancel()
	if probeErr == nil {
		t.Fatal("removed agent endpoint remains reachable")
	}

	stdout, stderr, err := runFleetE2E(
		binary,
		environment,
		"",
		"add",
		workspace,
		"--name",
		"cli-managed",
	)
	if err != nil {
		t.Fatalf("fleet add: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var roster []fleet.AgentStatus
	fleetWebJSON(t, client, http.MethodGet, baseURL+"/api/agents", "", http.StatusOK, &roster)
	if len(roster) != 1 ||
		roster[0].ID == added.Agent.ID ||
		roster[0].Name != "cli-managed" {
		t.Fatalf("CLI-created roster = %+v, removed = %+v", roster, added)
	}
	stdout, stderr, err = runFleetE2E(
		binary,
		environment,
		"",
		"remove",
		roster[0].ID,
		"--yes",
	)
	if err != nil {
		t.Fatalf("fleet remove: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
}

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
	agentID := "aaaaaa"
	agentAddress := writeFleetE2EAgent(t, home, workspace, agentID)
	secondWorkspace := t.TempDir()
	secondAgentID := "bbbbbb"
	secondAgentAddress := writeFleetE2EAgent(
		t,
		home,
		secondWorkspace,
		secondAgentID,
	)
	const configSecret = "fleet-web-config-secret-sentinel"
	configPath := filepath.Join(workspace, ".juex", "juex.yaml")
	initialConfig := append(
		fleetWebConfig("old-model"),
		[]byte("environment:\n  variables:\n    SECRET_TOKEN: "+configSecret+"\n")...,
	)
	if err := os.WriteFile(configPath, initialConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	remoteMCP := newWebRemoteMCPServer(t)
	if err := os.MkdirAll(filepath.Join(workspace, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	const mcpQuerySecret = "fleet-runtime-query-secret"
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"remote":{"type":"streamable-http","url":%q}}}`, remoteMCP.URL+"?token="+mcpQuerySecret)
	if err := os.WriteFile(filepath.Join(workspace, ".agents", "mcp.json"), []byte(mcpConfig), 0o600); err != nil {
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
		for _, address := range []agentstate.AgentAddress{agentAddress, secondAgentAddress} {
			runtimeState, err := endpoint.ReadRuntime(address)
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
	firstRuntime := waitFleetRuntime(t, agentAddress)
	probeFleetRuntime(t, firstRuntime)
	probeFleetRuntime(t, waitFleetRuntime(t, secondAgentAddress))
	if runtime.GOOS != "windows" && !strings.HasPrefix(firstRuntime.Endpoint, "unix://") {
		t.Fatalf("agent endpoint = %q, want Unix socket", firstRuntime.Endpoint)
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

	var fleetStatus struct {
		Process processmetrics.Usage `json:"process"`
	}
	fleetWebJSON(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/fleet/status",
		"",
		http.StatusOK,
		&fleetStatus,
	)
	assertProcessMetrics(t, "Fleet", &fleetStatus.Process, false)
	time.Sleep(10 * time.Millisecond)
	fleetWebJSON(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/fleet/status",
		"",
		http.StatusOK,
		&fleetStatus,
	)
	assertProcessMetrics(t, "Fleet", &fleetStatus.Process, true)

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
	time.Sleep(10 * time.Millisecond)
	fleetWebJSON(t, client, http.MethodGet, baseURL+"/api/agents", "", http.StatusOK, &roster)
	for _, agent := range roster {
		assertProcessMetrics(t, "Agent "+agent.ID, agent.Process, true)
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
		MCP struct {
			Servers []struct {
				Name    string `json:"name"`
				Type    string `json:"type"`
				URL     string `json:"url"`
				Command string `json:"command"`
			} `json:"servers"`
		} `json:"mcp"`
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
	if len(runtimeStatus.MCP.Servers) != 1 ||
		runtimeStatus.MCP.Servers[0].Name != "remote" ||
		runtimeStatus.MCP.Servers[0].Type != "http" ||
		runtimeStatus.MCP.Servers[0].URL != remoteMCP.URL ||
		runtimeStatus.MCP.Servers[0].Command != "" ||
		strings.Contains(runtimeStatus.MCP.Servers[0].URL, mcpQuerySecret) {
		t.Fatalf("proxied MCP runtime metadata = %+v", runtimeStatus.MCP.Servers)
	}

	var visibleConfig fleet.AgentConfig
	fleetWebJSON(
		t,
		client,
		http.MethodGet,
		baseURL+"/api/agents/"+agentID+"/config",
		"",
		http.StatusOK,
		&visibleConfig,
	)
	if strings.Contains(visibleConfig.Content, configSecret) ||
		!strings.Contains(visibleConfig.Content, "[REDACTED_ENV]") {
		t.Fatalf("fleet config response was not redacted:\n%s", visibleConfig.Content)
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

	invalid := `{"content":"models: [invalid"}`
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
	unchangedRuntime := waitFleetRuntime(t, agentAddress)
	if !unchangedRuntime.Matches(firstRuntime) {
		t.Fatalf("invalid update restarted runtime: before=%+v after=%+v", firstRuntime, unchangedRuntime)
	}

	validBody, err := json.Marshal(map[string]string{
		"content": strings.ReplaceAll(visibleConfig.Content, "old-model", "new-model"),
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
		strings.Contains(update.Config.Content, configSecret) ||
		!strings.Contains(update.Config.Content, "[REDACTED_ENV]") ||
		update.Agent.RuntimeHealth != fleet.RuntimeHealthy {
		t.Fatalf("config update response = %+v", update)
	}
	updatedRawConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updatedRawConfig), configSecret) {
		t.Fatalf("placeholder PUT did not retain the existing secret:\n%s", updatedRawConfig)
	}
	secondRuntime := waitFleetRuntime(t, agentAddress)
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

	literalContent := strings.Replace(
		update.Config.Content,
		`    SECRET_TOKEN: "[REDACTED_ENV]"`,
		"    SECRET_TOKEN: \"[REDACTED_ENV]\"\n    LITERAL_PLACEHOLDER: !juex/literal \"[REDACTED_ENV]\"",
		1,
	)
	if literalContent == update.Config.Content {
		t.Fatalf("redacted config did not contain the expected environment placeholder:\n%s", update.Config.Content)
	}
	literalBody, err := json.Marshal(map[string]string{"content": literalContent})
	if err != nil {
		t.Fatal(err)
	}
	var literalUpdate struct {
		Config fleet.AgentConfig `json:"config"`
		Agent  fleet.AgentStatus `json:"agent"`
	}
	fleetWebJSON(
		t,
		client,
		http.MethodPut,
		baseURL+"/api/agents/"+agentID+"/config",
		string(literalBody),
		http.StatusOK,
		&literalUpdate,
	)
	literalRawConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(literalRawConfig), `LITERAL_PLACEHOLDER: "[REDACTED_ENV]"`) ||
		strings.Contains(string(literalRawConfig), "!juex/literal") {
		t.Fatalf("literal placeholder escape was not persisted as a plain string:\n%s", literalRawConfig)
	}
	if strings.Contains(literalUpdate.Config.Content, "!juex/literal") ||
		strings.Count(literalUpdate.Config.Content, "[REDACTED_ENV]") < 2 ||
		literalUpdate.Agent.RuntimeHealth != fleet.RuntimeHealthy {
		t.Fatalf("literal placeholder update response = %+v", literalUpdate)
	}
	thirdRuntime := waitFleetRuntime(t, agentAddress)
	if thirdRuntime.InstanceID == secondRuntime.InstanceID {
		t.Fatalf("literal placeholder update reused runtime instance %q", thirdRuntime.InstanceID)
	}
}

func TestFleetWebConfigRestartResumesInterruptedTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary fleet web config restart resume is slow")
	}
	binary := buildJuex(t)
	firstRequestStarted := make(chan struct{})
	continuationRequests := make(chan map[string]any, 1)
	providerErrors := make(chan error, 2)
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			providerErrors <- err
			return
		}
		if providerCalls.Add(1) == 1 {
			close(firstRequestStarted)
			<-r.Context().Done()
			return
		}
		select {
		case continuationRequests <- body:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatCompletionResponse("continued after config restart"))
	}))
	t.Cleanup(provider.Close)

	home := t.TempDir()
	workspace := t.TempDir()
	agentID := "aaaaaa"
	agentAddress := writeFleetE2EAgent(t, home, workspace, agentID)
	writeFleetProviderConfig(t, workspace, provider.URL)
	environment := fleetE2EEnvironmentForProvider(
		home,
		"local-chat",
		"openai/chat",
		provider.URL,
		"chat-test",
	)
	supervisor := startFleetSupervisor(t, binary, environment)
	t.Cleanup(func() {
		shutdownFleetAgent(t, agentAddress)
		if supervisor.cmd.ProcessState == nil {
			_ = supervisor.cmd.Process.Kill()
			_ = supervisor.cmd.Wait()
		}
	})
	baseURL := "http://" + waitFleetWebReady(t, supervisor)
	client := &http.Client{Timeout: 30 * time.Second}

	var started fleet.AgentStatus
	fleetWebJSON(
		t,
		client,
		http.MethodPost,
		baseURL+"/api/agents/"+agentID+"/start",
		"",
		http.StatusOK,
		&started,
	)
	if started.RuntimeHealth != fleet.RuntimeHealthy {
		t.Fatalf("started agent = %+v", started)
	}
	oldRuntime := waitFleetRuntime(t, agentAddress)
	sessionID, originalTurnID := startFleetBlockingTurn(t, oldRuntime)
	select {
	case <-firstRequestStarted:
	case err := <-providerErrors:
		t.Fatalf("provider request: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("original provider request did not start")
	}

	configPath := filepath.Join(workspace, ".juex", "juex.yaml")
	configBody, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	updatedContent := string(configBody) + "\n# saved through Runtime Config\n"
	requestBody, err := json.Marshal(map[string]string{"content": updatedContent})
	if err != nil {
		t.Fatal(err)
	}
	var updated struct {
		Config fleet.AgentConfig   `json:"config"`
		Agent  fleet.RestartResult `json:"agent"`
	}
	fleetWebJSON(
		t,
		client,
		http.MethodPut,
		baseURL+"/api/agents/"+agentID+"/config",
		string(requestBody),
		http.StatusOK,
		&updated,
	)
	if updated.Config.Content != updatedContent ||
		!updated.Agent.Resume.Required ||
		!updated.Agent.Resume.Sent ||
		updated.Agent.Resume.SessionID != sessionID {
		t.Fatalf("config restart response = %+v", updated)
	}
	newRuntime := waitFleetRuntimeVersion(
		t,
		agentAddress,
		oldRuntime.InstanceID,
		oldRuntime.BinaryVersion,
	)
	if newRuntime.PID == oldRuntime.PID {
		t.Fatalf("config restart reused pid %d", newRuntime.PID)
	}
	select {
	case request := <-continuationRequests:
		encoded, _ := json.Marshal(request)
		if !strings.Contains(string(encoded), "System notice") {
			t.Fatalf("continuation request missing system notice: %s", encoded)
		}
	case err := <-providerErrors:
		t.Fatalf("provider request: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("config restart continuation did not reach provider")
	}
	waitFleetInterruptedAndContinuationEvents(
		t,
		filepath.Join(agentAddress.StateDir(), "sessions", sessionID, "events.jsonl"),
		originalTurnID,
	)
}

func TestFleetWebNewSessionRejectsStaleEventReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary fleet web session test is slow")
	}
	binary := buildJuex(t)
	newGreetingStarted := make(chan struct{})
	newGreetingRelease := make(chan struct{})
	var releaseOnce sync.Once
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := providerCalls.Add(1)
		if call == 2 {
			close(newGreetingStarted)
			select {
			case <-newGreetingRelease:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		message := "old session complete"
		if call == 2 {
			message = "new session complete"
		}
		_, _ = io.WriteString(w, chatCompletionResponse(message))
	}))
	t.Cleanup(provider.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(newGreetingRelease) }) })

	home, err := os.MkdirTemp("", "jns-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	workspace := t.TempDir()
	agentID := "aaaaaa"
	agentAddress := writeFleetE2EAgent(t, home, workspace, agentID)
	writeFleetProviderConfig(t, workspace, provider.URL)
	environment := fleetWebEnvironment(home)
	t.Cleanup(func() { shutdownFleetAgent(t, agentAddress) })

	if stdout, stderr, err := runFleetE2E(binary, environment, "", "start", agentID); err != nil {
		t.Fatalf("fleet start: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	probeFleetRuntime(t, waitFleetRuntime(t, agentAddress))
	supervisor := startFleetSupervisor(t, binary, environment)
	t.Cleanup(func() {
		if supervisor.cmd.ProcessState == nil {
			_ = supervisor.cmd.Process.Kill()
			_ = supervisor.cmd.Wait()
		}
	})
	fleetURL := "http://" + waitFleetWebReady(t, supervisor)
	agentURL := fleetURL + "/agents/" + agentID
	client := &http.Client{Timeout: 30 * time.Second}

	var oldSession struct {
		ID string `json:"id"`
	}
	fleetWebJSON(t, client, http.MethodPost, agentURL+"/api/sessions", "", http.StatusCreated, &oldSession)
	if oldSession.ID == "" {
		t.Fatal("created old session without id")
	}
	var oldTurn webStartTurnResponse
	fleetWebJSON(
		t,
		client,
		http.MethodPost,
		agentURL+"/api/sessions/"+oldSession.ID+"/turns",
		`{"prompt":"persist the old session"}`,
		http.StatusAccepted,
		&oldTurn,
	)
	hasAssistantText := func(messages []webTranscriptMessage, want string) bool {
		for _, message := range messages {
			if message.Role != "assistant" {
				continue
			}
			for _, block := range message.Blocks {
				if block.Type == "text" && strings.Contains(block.Text, want) {
					return true
				}
			}
		}
		return false
	}
	waitForWebTranscript(t, agentURL, oldSession.ID, oldTurn.TurnID, 30*time.Second, "old Fleet session", func(messages []webTranscriptMessage) bool {
		return hasAssistantText(messages, "old session complete")
	})

	var newTurn struct {
		TurnID  string `json:"turn_id"`
		Command struct {
			Status struct {
				SessionID string `json:"session_id"`
			} `json:"status"`
		} `json:"command"`
	}
	fleetWebJSON(
		t,
		client,
		http.MethodPost,
		agentURL+"/api/sessions/"+oldSession.ID+"/turns",
		`{"prompt":"/new"}`,
		http.StatusOK,
		&newTurn,
	)
	newSessionID := newTurn.Command.Status.SessionID
	if newSessionID == "" || newSessionID == oldSession.ID || newTurn.TurnID == "" {
		t.Fatalf("new session response = %+v, old id = %s", newTurn, oldSession.ID)
	}
	select {
	case <-newGreetingStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("new session greeting did not reach provider")
	}

	staleCtx, cancelStale := context.WithTimeout(context.Background(), 5*time.Second)
	staleRequest, err := http.NewRequestWithContext(
		staleCtx,
		http.MethodGet,
		agentURL+"/api/sessions/"+oldSession.ID+"/events",
		http.NoBody,
	)
	if err != nil {
		cancelStale()
		t.Fatal(err)
	}
	staleResponse, err := client.Do(staleRequest)
	if err != nil {
		cancelStale()
		t.Fatal(err)
	}
	staleResponse.Body.Close()
	cancelStale()
	if staleResponse.StatusCode != http.StatusConflict {
		t.Fatalf("stale event reconnect status = %d, want %d", staleResponse.StatusCode, http.StatusConflict)
	}

	var sessions struct {
		Sessions []struct {
			ID     string `json:"id"`
			Active bool   `json:"active"`
		} `json:"sessions"`
	}
	fleetWebJSON(t, client, http.MethodGet, agentURL+"/api/sessions", "", http.StatusOK, &sessions)
	var oldFound, newFound bool
	for _, info := range sessions.Sessions {
		switch info.ID {
		case oldSession.ID:
			oldFound = true
			if info.Active {
				t.Fatalf("old session %s remained active: %+v", oldSession.ID, sessions.Sessions)
			}
		case newSessionID:
			newFound = true
			if !info.Active {
				t.Fatalf("new session %s is inactive: %+v", newSessionID, sessions.Sessions)
			}
		}
	}
	if !oldFound || !newFound {
		t.Fatalf("session history missing old or new session: %+v", sessions.Sessions)
	}
	fleetWebJSON(t, client, http.MethodGet, agentURL+"/api/sessions/"+oldSession.ID, "", http.StatusOK, nil)

	releaseOnce.Do(func() { close(newGreetingRelease) })
	waitForWebTranscript(t, agentURL, newSessionID, newTurn.TurnID, 30*time.Second, "new Fleet session greeting", func(messages []webTranscriptMessage) bool {
		return hasAssistantText(messages, "new session complete")
	})
	assertFleetSSEHeaders(t, agentURL+"/api/sessions/"+newSessionID+"/events")
}

func assertProcessMetrics(
	t *testing.T,
	name string,
	usage *processmetrics.Usage,
	wantCPU bool,
) {
	t.Helper()
	if usage == nil || usage.RSSBytes == 0 {
		t.Fatalf("%s process metrics = %+v, want positive RSS", name, usage)
	}
	if !wantCPU {
		return
	}
	if usage.CPUPercent == nil || *usage.CPUPercent < 0 {
		t.Fatalf("%s process metrics = %+v, want non-negative CPU percentage", name, usage)
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
		!strings.Contains(strings.ToLower(string(body)), "<!doctype html") {
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
	return []byte(`models: [local:` + model + `]
providers:
  - id: local
    protocol: openai/chat
    base_url: http://127.0.0.1:1
    api_key: test-key
    models:
      - id: ` + model + `
`)
}
