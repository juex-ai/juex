package fleethttp

import (
	"context"
	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/foundation/fleetclient"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypedManagementChecksCallerIdentityAndConfigRevision(t *testing.T) {
	home := t.TempDir()
	manager, err := app.NewFleet(fleet.Options{HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := manager.EnsureSupervisor(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Manager: manager})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	discovery := server.managementIdentity
	discovery.URL = httpServer.URL
	if err := fleetclient.Publish(home, discovery); err != nil {
		t.Fatal(err)
	}
	ordinary := fleetclient.New(home, fleetclient.ProfileAgent, supervisor.Agent.ID)
	if _, err := ordinary.Agents(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("ordinary call: %v", err)
	}
	client := fleetclient.New(home, fleetclient.ProfileSupervisor, supervisor.Agent.ID)
	created, err := client.Create(context.Background(), fleetclient.CreateRequest{Workspace: t.TempDir(), Name: "managed"})
	if err != nil || !created.Published || created.Applied {
		t.Fatalf("create=%+v %v", created, err)
	}
	for _, existing := range []fleetclient.Agent{{ID: supervisor.Agent.ID, Name: supervisor.Agent.Name, Workspace: supervisor.Agent.Workspace}, created.Agent} {
		if _, err := client.Create(context.Background(), fleetclient.CreateRequest{Workspace: existing.Workspace, Name: "unexpected rename"}); err == nil || !strings.Contains(err.Error(), "409") {
			t.Errorf("existing workspace creation must conflict: %v", err)
		}
		statuses, err := client.Agents(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range statuses {
			if status.ID == existing.ID && status.Name != existing.Name {
				t.Errorf("creation renamed existing Agent: %+v", status)
			}
		}
	}
	before, err := client.Config(context.Background(), created.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Configure(context.Background(), created.Agent.ID, fleetclient.ConfigRequest{Content: "preset: missing\n", ExpectedRevision: before.Revision}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("invalid config: %v", err)
	}
	if _, err := client.Configure(context.Background(), created.Agent.ID, fleetclient.ConfigRequest{Content: "preset: [", ExpectedRevision: before.Revision}); err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "yaml") {
		t.Errorf("malformed config must expose a validation error: %v", err)
	}
	result, err := client.Configure(context.Background(), created.Agent.ID, fleetclient.ConfigRequest{Content: "preset: minimal\n", ExpectedRevision: before.Revision})
	if err != nil || !result.Saved || !result.RestartRequired || result.Applied {
		t.Fatalf("save=%+v %v", result, err)
	}
	if _, err := manager.SetEnabled(context.Background(), created.Agent.ID, false); err != nil {
		t.Fatal(err)
	}
	failedApply, err := client.Configure(context.Background(), created.Agent.ID, fleetclient.ConfigRequest{Content: "preset: minimal\n", ExpectedRevision: result.Revision, Apply: true})
	if err != nil || !failedApply.Saved || !failedApply.Published || failedApply.Applied || !failedApply.RestartRequired || failedApply.Error == "" || failedApply.Agent.ID != created.Agent.ID || failedApply.Agent.Enabled {
		t.Fatalf("failed application must preserve published target status: %+v %v", failedApply, err)
	}
	if _, err := client.Configure(context.Background(), created.Agent.ID, fleetclient.ConfigRequest{Content: "preset: standard\n", ExpectedRevision: before.Revision}); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("stale config: %v", err)
	}
	if _, err := client.Lifecycle(context.Background(), supervisor.Agent.ID, fleetclient.LifecycleRequest{Action: "stop"}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("self-stop: %v", err)
	}
	discovery.InstanceID = "stale-instance"
	if err := fleetclient.Publish(home, discovery); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Agents(context.Background()); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("stale instance: %v", err)
	}
}

func TestSupervisorStatusReportsRepairRequired(t *testing.T) {
	home := t.TempDir()
	manager, err := app.NewFleet(fleet.Options{HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := manager.EnsureSupervisor(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, "agents", supervisor.Agent.ID, "agent.json")); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Manager: manager})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/supervisor", nil))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "requires explicit repair") {
		t.Fatalf("missing binding response=%d %s", response.Code, response.Body.String())
	}
}
