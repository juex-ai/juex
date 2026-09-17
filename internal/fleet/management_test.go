package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/fleetclient"
)

func TestManagedCreateConcurrentWorkspaceDoesNotRenameWinner(t *testing.T) {
	m, err := New(Options{HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := m.EnsureSupervisor(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	caller := fleetclient.Caller{Profile: fleetclient.ProfileSupervisor, AgentID: supervisor.Agent.ID}
	workspace := t.TempDir()
	type outcome struct {
		name   string
		result fleetclient.Result
		err    error
	}
	outcomes := make(chan outcome, 6)
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("creator-%d", i)
			result, err := m.ManagedCreate(context.Background(), caller, fleetclient.CreateRequest{Workspace: workspace, Name: name})
			outcomes <- outcome{name, result, err}
		}()
	}
	wg.Wait()
	close(outcomes)
	var winner outcome
	successes := 0
	for result := range outcomes {
		if result.err == nil {
			winner = result
			successes++
		} else {
			var conflict *ConflictError
			if !errors.As(result.err, &conflict) {
				t.Errorf("unexpected creation error: %v", result.err)
			}
		}
	}
	if successes != 1 {
		t.Fatalf("successful creations = %d, want 1", successes)
	}
	entry, err := m.reload(winner.result.Agent.ID)
	if err != nil || entry.Agent.Name != winner.name {
		t.Fatalf("winner renamed: %+v %v", entry, err)
	}
}

func TestManagementRejectsOrdinaryProfileAndSelfMutation(t *testing.T) {
	m, _ := New(Options{HomeDir: t.TempDir()})
	s, err := m.EnsureSupervisor(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	caller := fleetclient.Caller{Profile: fleetclient.ProfileAgent, AgentID: s.Agent.ID}
	if _, err := m.ManagedAgents(context.Background(), caller); err == nil {
		t.Fatal("ordinary profile allowed management")
	}
	caller.Profile = fleetclient.ProfileSupervisor
	if _, err := m.ManagedAgents(context.Background(), caller); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ManagedLifecycle(context.Background(), caller, s.Agent.ID, fleetclient.LifecycleRequest{Action: "stop"}); err == nil {
		t.Fatal("self-stop accepted")
	}
}

func TestManagementConfigReportsSavedButUnapplied(t *testing.T) {
	home := t.TempDir()
	m, _ := New(Options{HomeDir: home, ConfigUpdater: func(home, id string, body []byte, revision string) error {
		return os.WriteFile(filepath.Join(home, "agents", id, "juex.yaml"), body, 0600)
	}})
	s, err := m.EnsureSupervisor(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	target, err := m.Add(context.Background(), AddOptions{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	caller := fleetclient.Caller{Profile: fleetclient.ProfileSupervisor, AgentID: s.Agent.ID}
	before, err := m.ManagedConfig(context.Background(), caller, target.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.ManagedConfigure(context.Background(), caller, target.Agent.ID, fleetclient.ConfigRequest{Content: "preset: minimal\n", ExpectedRevision: before.Revision})
	if err != nil || !result.Published || result.Applied || !result.RestartRequired || result.BehaviorVerified {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	m2, _ := New(Options{HomeDir: home})
	after, err := m2.ManagedConfig(context.Background(), caller, target.Agent.ID)
	if err != nil || !after.RestartRequired {
		t.Fatalf("pending application lost: %+v %v", after, err)
	}
}
