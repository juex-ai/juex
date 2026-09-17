package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/foundation/fleetclient"
	"github.com/juex-ai/juex/internal/framework/agentstate"
	"github.com/juex-ai/juex/internal/framework/endpoint"
)

func TestSupervisorAgentCompiledLifecycleAndBusyConfiguration(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled Fleet/Supervisor lifecycle")
	}
	binary := buildJuex(t)
	home := t.TempDir()
	environment := fleetWebEnvironment(home)
	process := startFleetSupervisor(t, binary, environment)
	defer func() { _ = process.cmd.Process.Kill(); _ = process.cmd.Wait() }()
	baseURL := "http://" + waitFleetWebReady(t, process)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	var supervisor fleet.SupervisorStatus
	fleetWebJSON(t, httpClient, http.MethodGet, baseURL+"/api/supervisor", "", 200, &supervisor)
	if supervisor.State != "ready" || supervisor.Agent.ID == "" {
		t.Fatalf("Supervisor resources not initialized: %+v", supervisor)
	}
	// No provider exists in this Home; the Fleet API remains independently ready.
	fleetWebJSON(t, httpClient, http.MethodGet, baseURL+"/api/supervisor", "", 200, &supervisor)
	client := fleetclient.New(home, fleetclient.ProfileSupervisor, supervisor.Agent.ID)
	failed, err := client.Create(context.Background(), fleetclient.CreateRequest{Workspace: t.TempDir(), Start: true})
	if err != nil || !failed.Published || failed.Applied || failed.Error == "" {
		t.Fatalf("registration with unavailable provider=%+v %v", failed, err)
	}

	if _, err := fleetclient.New(home, fleetclient.ProfileAgent, supervisor.Agent.ID).Agents(context.Background()); err == nil {
		t.Fatal("ordinary profile admitted")
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatCompletionResponse("completed support request"))
	}))
	defer provider.Close()
	defer close(release)
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".juex"), 0700); err != nil {
		t.Fatal(err)
	}
	writeFleetProviderConfig(t, workspace, provider.URL)
	created, err := client.Create(context.Background(), fleetclient.CreateRequest{Workspace: workspace, Name: "managed target"})
	if err != nil || !created.Published || created.Applied {
		t.Fatalf("create/start=%+v %v", created, err)
	}
	address, _ := agentstate.NewAgentAddress(home, created.Agent.ID)
	defer shutdownFleetAgent(t, address)
	initialConfig, err := client.Config(context.Background(), created.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstApply, err := client.Configure(context.Background(), created.Agent.ID, fleetclient.ConfigRequest{Content: "preset: standard\n", ExpectedRevision: initialConfig.Revision, Apply: true})
	if err != nil || !firstApply.Applied || firstApply.Restarted {
		t.Fatalf("initial application=%+v %v", firstApply, err)
	}
	original := waitFleetRuntime(t, address)
	startFleetBlockingTurn(t, original)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("provider never received turn")
	}
	before, err := client.Config(context.Background(), created.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := client.Configure(context.Background(), created.Agent.ID, fleetclient.ConfigRequest{Content: "preset: minimal\n", ExpectedRevision: before.Revision, Apply: true})
	if err != nil || !deferred.Saved || !deferred.Deferred || deferred.Applied || !deferred.RestartRequired {
		t.Fatalf("busy apply=%+v %v", deferred, err)
	}
	if err := endpoint.Probe(context.Background(), original); err != nil {
		t.Fatalf("deferred configuration interrupted target: %v", err)
	}
	stopped, err := client.Lifecycle(context.Background(), created.Agent.ID, fleetclient.LifecycleRequest{Action: "stop"})
	if err != nil || !stopped.Deferred || stopped.Applied {
		t.Fatalf("busy stop=%+v %v", stopped, err)
	}
	// Explicit interruption is permitted and runtime restart recovery stays intact.
	applied, err := client.Configure(context.Background(), created.Agent.ID, fleetclient.ConfigRequest{Content: "preset: minimal\n", ExpectedRevision: deferred.Revision, Apply: true, Interrupt: true})
	if err != nil || !applied.Applied || !applied.Restarted || applied.RestartRequired || applied.BehaviorVerified {
		t.Fatalf("authorized apply=%+v %v", applied, err)
	}
	current := waitFleetRuntime(t, address)
	if current.InstanceID == original.InstanceID {
		t.Fatal("runtime was not restarted")
	}
	actual, err := endpoint.Inspect(context.Background(), current)
	if err != nil || actual.ConfigRevision != applied.Revision {
		t.Fatalf("loaded revision=%+v %v", actual, err)
	}
	// CLI lifecycle uses the same persisted role, without requiring the model.
	command := exec.Command(binary, "fleet", "supervisor", "stop")
	command.Env = environment
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Supervisor stop: %v %s", err, output)
	}
	command = exec.Command(binary, "fleet", "supervisor", "status")
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	var stoppedSupervisor fleet.SupervisorStatus
	if err := json.Unmarshal(output, &stoppedSupervisor); err != nil {
		t.Fatal(err)
	}
	if stoppedSupervisor.Agent.ID != supervisor.Agent.ID || stoppedSupervisor.Agent.Autostart {
		t.Fatalf("role identity/stop lost: %+v", stoppedSupervisor)
	}
	marker := filepath.Join(home, "shared-proof")
	if err := os.WriteFile(marker, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	fleetWebJSON(t, httpClient, http.MethodPost, baseURL+"/api/supervisor", `{"action":"reset"}`, 200, &supervisor)
	if supervisor.Agent.ID == stoppedSupervisor.Agent.ID || len(supervisor.Retained) != 1 {
		t.Fatalf("reset=%+v", supervisor)
	}
	if _, err := client.Agents(context.Background()); err == nil {
		t.Fatal("retired Supervisor retains management authority")
	}
	if content, _ := os.ReadFile(marker); string(content) != "retained" {
		t.Fatal("shared data changed")
	}
}

func TestSupervisorSupportUsesMainToolsWithOrdinaryRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled Supervisor support")
	}
	binary := buildJuex(t)
	home := t.TempDir()
	var mu sync.Mutex
	var observed bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		body, _ := json.Marshal(request)
		mu.Lock()
		observed = strings.Contains(string(body), "fleet_agents") && strings.Contains(string(body), "# Supervisor")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatCompletionResponse("I can help manage this Fleet."))
	}))
	defer provider.Close()
	content := fmt.Sprintf("models: [local:chat-test]\nenable_user_agents_resources: false\nextensions:\n  allow: []\nproviders:\n  - id: local\n    protocol: openai/chat\n    base_url: %s\n    api_key: test-key\n    capabilities:\n      streaming: false\n    models:\n      - id: chat-test\n", provider.URL)
	if err := os.WriteFile(filepath.Join(home, "juex.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	process := startFleetSupervisor(t, binary, fleetWebEnvironment(home))
	defer func() { _ = process.cmd.Process.Kill(); _ = process.cmd.Wait() }()
	baseURL := "http://" + waitFleetWebReady(t, process)
	var supervisor fleet.SupervisorStatus
	fleetWebJSON(t, &http.Client{Timeout: 10 * time.Second}, http.MethodGet, baseURL+"/api/supervisor", "", 200, &supervisor)
	address, _ := agentstate.NewAgentAddress(home, supervisor.Agent.ID)
	defer shutdownFleetAgent(t, address)
	runtimeState := waitFleetRuntime(t, address)
	startFleetBlockingTurn(t, runtimeState)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := observed
		mu.Unlock()
		if found {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Supervisor support request did not receive Main management tools and guidance")
}
