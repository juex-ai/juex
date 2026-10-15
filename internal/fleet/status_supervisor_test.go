package fleet

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/framework/agentstate"
)

func TestStatusProjectsCurrentSupervisorBinding(t *testing.T) {
	ctx := context.Background()
	m, err := New(Options{HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	name := "Supervisor"
	ordinary, err := m.Add(ctx, AddOptions{Workspace: t.TempDir(), Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	assertRole := func(wantID string) {
		t.Helper()
		statuses, err := m.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := wantID == ""
		for _, status := range statuses {
			want := status.ID == wantID
			if want {
				found = true
			}
			one, err := m.StatusOne(ctx, status.ID)
			if err != nil || status.IsSupervisor != want || one.IsSupervisor != want {
				t.Fatalf("role want=%v list=%+v one=%+v err=%v", want, status, one, err)
			}
		}
		if !found {
			t.Fatalf("missing Supervisor %s", wantID)
		}
	}
	assertRole("")
	supervisor, err := m.EnsureSupervisor(ctx, true)
	if err != nil || !supervisor.Agent.IsSupervisor {
		t.Fatalf("initialize=%+v err=%v", supervisor, err)
	}
	name = "Renamed support"
	if _, err := agentstate.UpdateAgent(m.homeDir, supervisor.Agent.ID, agentstate.AgentUpdate{Name: &name}); err != nil {
		t.Fatal(err)
	}
	assertRole(supervisor.Agent.ID)
	disabled, err := m.SetEnabled(ctx, supervisor.Agent.ID, false)
	if err != nil || !disabled.IsSupervisor || disabled.Enabled {
		t.Fatalf("disable lost role: %+v %v", disabled, err)
	}
	assertRole(supervisor.Agent.ID)
	reset, err := m.ResetSupervisor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertRole(reset.Agent.ID)
	if _, err := m.RemoveSupervisor(ctx); err != nil {
		t.Fatal(err)
	}
	assertRole("")
	// Malformed bindings may decode an ID before validation fails.
	if err := os.WriteFile(m.supervisorPath(), []byte(`{"state":"invalid","agent":{"id":"`+ordinary.Agent.ID+`"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	assertRole("")
	status, err := m.StatusOne(ctx, ordinary.Agent.ID)
	if err != nil || !strings.Contains(status.Problem, "Supervisor binding") {
		t.Fatalf("binding failure not reported: %+v %v", status, err)
	}
}
