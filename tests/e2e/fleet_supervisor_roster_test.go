package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fleetweb "github.com/juex-ai/juex/internal/entrypoints/fleethttp"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/framework/agentstate"
)

func TestFleetSupervisorRoleInRosterAndEvents(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	manager, err := fleet.New(fleet.Options{HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.EnsureSupervisor(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	name := "Supervisor"
	ordinary, err := manager.Add(ctx, fleet.AddOptions{Workspace: t.TempDir(), Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	name = "Support"
	if _, err := agentstate.UpdateAgent(home, bound.Agent.ID, agentstate.AgentUpdate{Name: &name}); err != nil {
		t.Fatal(err)
	}
	server, err := fleetweb.New(fleetweb.Options{Manager: manager})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client := &http.Client{Timeout: 10 * time.Second}
	checkRoster := func(agents []fleet.AgentStatus) {
		t.Helper()
		if len(agents) != 2 {
			t.Fatalf("roster=%+v", agents)
		}
		for _, agent := range agents {
			if agent.IsSupervisor != (agent.ID == bound.Agent.ID) {
				t.Fatalf("incorrect role: %+v", agent)
			}
			if agent.ID == ordinary.Agent.ID && agent.Name != "Supervisor" {
				t.Fatal("ordinary name changed")
			}
		}
	}
	var agents []fleet.AgentStatus
	fleetWebJSON(t, client, http.MethodGet, httpServer.URL+"/api/agents", "", 200, &agents)
	checkRoster(agents)
	var disabled fleet.AgentStatus
	fleetWebJSON(t, client, http.MethodPost, httpServer.URL+"/api/agents/"+bound.Agent.ID+"/disable", "", 200, &disabled)
	if !disabled.IsSupervisor || disabled.Enabled {
		t.Fatalf("disable=%+v", disabled)
	}

	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(streamCtx, http.MethodGet, httpServer.URL+"/api/fleet/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream status=%d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event struct {
			Type   string              `json:"type"`
			Agents []fleet.AgentStatus `json:"agents"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "fleet.roster" {
			checkRoster(event.Agents)
			return
		}
	}
	t.Fatalf("no roster event: %v", scanner.Err())
}
