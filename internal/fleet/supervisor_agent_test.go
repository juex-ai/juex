package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/juex-ai/juex/internal/framework/agentstate"
)

func TestSupervisorSettlementIsSeparateAndDurable(t *testing.T) {
	ctx := context.Background()
	var calls []string
	fail := true
	m, err := New(Options{HomeDir: t.TempDir(), SettleSupervisor: func(ctx context.Context, home, id string) (ExecutorSettlement, error) {
		calls = append(calls, id)
		resolved, err := agentstate.ResolveByID(agentstate.Options{HomeDir: home}, id)
		if err != nil {
			return ExecutorSettlement{}, err
		}
		if resolved.Agent.Enabled {
			t.Error("settlement preceded disable")
		}
		if fail {
			return ExecutorSettlement{}, errors.New("Memory unavailable")
		}
		return ExecutorSettlement{Confirmed: true, Released: 1}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := m.EnsureSupervisor(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	reset, err := m.ResetSupervisor(ctx)
	if err != nil || reset.Agent.ID == initial.Agent.ID || reset.Settlement == nil || reset.Settlement.Confirmed || reset.Settlement.Reason != "Memory unavailable" {
		t.Fatalf("reset=%+v %v", reset, err)
	}
	status, err := m.Supervisor(ctx)
	if err != nil || status.Settlement == nil || status.Settlement.AgentID != initial.Agent.ID {
		t.Fatalf("durable receipt %+v %v", status, err)
	}
	fail = false
	removed, err := m.RemoveSupervisor(ctx)
	if err != nil || removed.Settlement == nil || !removed.Settlement.Confirmed || removed.Settlement.Released != 1 || len(calls) != 2 {
		t.Fatalf("remove %+v %v", removed, err)
	}
}

func TestSupervisorInitializationKeepsIdentityAndCustomization(t *testing.T) {
	m, err := New(Options{HomeDir: t.TempDir(), SupervisorTemplate: func() ([]byte, []byte) {
		return []byte("preset: standard\n"), []byte("Supervisor support guidance\n")
	}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			s, err := m.EnsureSupervisor(context.Background(), true)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- s.Agent.ID
		})
	}
	wg.Wait()
	close(ids)
	var id string
	for got := range ids {
		if id != "" && id != got {
			t.Fatalf("multiple Supervisor identities: %s %s", id, got)
		}
		id = got
	}
	if id == "" {
		t.Fatal("Supervisor was not created")
	}
	address, _ := agentstate.NewAgentAddress(m.homeDir, id)
	if err := os.WriteFile(address.ConfigPath(), []byte("# customized\n"), 0600); err != nil {
		t.Fatal(err)
	}
	name := "Renamed support"
	if _, err := agentstate.UpdateAgent(m.homeDir, id, agentstate.AgentUpdate{Name: &name}); err != nil {
		t.Fatal(err)
	}
	s, err := m.EnsureSupervisor(context.Background(), true)
	if err != nil || s.Agent.ID != id || s.Agent.Name != name {
		t.Fatalf("status=%+v err=%v", s, err)
	}
	if body, _ := os.ReadFile(address.ConfigPath()); string(body) != "# customized\n" {
		t.Fatal("customization overwritten")
	}
	if _, err := m.Remove(context.Background(), id, RemoveOptions{SkipConfirmation: true}); err == nil {
		t.Fatal("generic removal accepted bound Supervisor")
	}
}

func TestSupervisorStopRepairResetAndRemoval(t *testing.T) {
	m, _ := New(Options{HomeDir: t.TempDir()})
	s, err := m.EnsureSupervisor(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	id := s.Agent.ID
	address, _ := agentstate.NewAgentAddress(m.homeDir, id)
	history := filepath.Join(address.StateDir(), "threads", "preserved.txt")
	if err := os.WriteFile(history, []byte("history"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	s, err = m.EnsureSupervisor(context.Background(), true)
	if err != nil || s.Agent.Autostart {
		t.Fatalf("manual stop was not retained: %+v %v", s, err)
	}
	for _, action := range []string{"disable", "enable"} {
		next, err := m.SupervisorAction(context.Background(), action)
		if err != nil || next.Agent.ID != id || next.Agent.Enabled != (action == "enable") {
			t.Fatalf("%s=%+v %v", action, next, err)
		}
	}
	if err := os.Remove(filepath.Join(address.StateDir(), "agent.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureSupervisor(context.Background(), true); err == nil {
		t.Fatal("missing binding silently repaired")
	}
	s, err = m.RepairSupervisor(context.Background())
	if err != nil || s.Agent.ID != id {
		t.Fatalf("repair=%+v err=%v", s, err)
	}
	s, err = m.ResetSupervisor(context.Background())
	if err != nil || s.Agent.ID == id {
		t.Fatalf("reset=%+v err=%v", s, err)
	}
	if body, _ := os.ReadFile(history); string(body) != "history" {
		t.Fatal("old history lost")
	}
	old, err := agentstate.ResolveByID(agentstate.Options{HomeDir: m.homeDir}, id)
	if err != nil || old.Agent.Enabled {
		t.Fatalf("old executor not disabled: %+v %v", old, err)
	}
	if _, err := m.RemoveSupervisor(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err = m.EnsureSupervisor(context.Background(), true)
	if err != nil || s.State != "removed" {
		t.Fatalf("removed Supervisor recreated: %+v %v", s, err)
	}
}

func TestSupervisorRepairMissingDirectoryAndResumeInitialization(t *testing.T) {
	m, _ := New(Options{HomeDir: t.TempDir()})
	binding, err := m.planSupervisor(nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := m.ResetSupervisor(context.Background())
	if err != nil || status.Agent.ID != binding.Agent.ID {
		t.Fatalf("initialization retry: %+v %v", status, err)
	}
	address, _ := agentstate.NewAgentAddress(m.homeDir, binding.Agent.ID)
	if err := os.RemoveAll(address.StateDir()); err != nil {
		t.Fatal(err)
	}
	repaired, err := m.RepairSupervisor(context.Background())
	if err != nil || repaired.Agent.ID != binding.Agent.ID || repaired.Agent.Autostart || repaired.Agent.Enabled {
		t.Fatalf("repair missing state: %+v %v", repaired, err)
	}
}

func TestBoundSupervisorCannotBeGarbageCollected(t *testing.T) {
	m, _ := New(Options{HomeDir: t.TempDir()})
	status, err := m.EnsureSupervisor(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(status.Agent.Workspace); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteOrphans(context.Background(), []string{status.Agent.ID}); err == nil {
		t.Fatal("garbage collection removed bound Supervisor")
	}
	if _, err := os.Stat(filepath.Join(m.homeDir, "agents", status.Agent.ID, "agent.json")); err != nil {
		t.Fatal(err)
	}
}
