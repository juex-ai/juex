package agent

import (
	"context"
	"testing"
)

func TestMaintenancePreservesWorkerHandoffAndRollsBackAdmission(t *testing.T) {
	for _, kind := range []string{"running", "handoff", "creation", "child-busy"} {
		t.Run(kind, func(t *testing.T) {
			parent, _ := newStubAgent(t)
			child, _ := newStubAgent(t)
			managed := &managedWorkerThread{app: child}
			manager := newWorkerThreadManager(parent, nil, 1)
			parent.workers = manager
			manager.mu.Lock()
			manager.threads["child"] = managed
			switch kind {
			case "running":
				managed.status.State = WorkerThreadStateRunning
			case "handoff":
				managed.resultHandoffs = 1
				manager.resultHandoffs["result"] = managed
			case "creation":
				manager.reservations["creating"] = &workerThreadReservation{ready: make(chan struct{})}
			}
			manager.mu.Unlock()
			// The fixture owns child cleanup separately from the Worker manager.
			defer func() {
				manager.mu.Lock()
				clear(manager.threads)
				clear(manager.reservations)
				clear(manager.resultHandoffs)
				manager.mu.Unlock()
			}()
			if kind == "child-busy" {
				result := child.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "ongoing"})
				if result.Kind != TurnAdmissionStarted {
					t.Fatalf("child=%+v", result)
				}
			}
			if release, err := parent.ReserveIdleMaintenance(); err == nil {
				release()
				t.Fatal("busy Worker tree reserved")
			}
			manager.mu.Lock()
			transitioning := manager.transitioning
			handoffs := len(manager.resultHandoffs)
			manager.mu.Unlock()
			if transitioning || (kind == "handoff" && handoffs != 1) {
				t.Fatal("failed reservation changed handoff delivery")
			}
			if result := parent.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "after failed reservation"}); result.Kind != TurnAdmissionStarted {
				t.Fatalf("parent admission not restored: %+v", result)
			}
		})
	}
}

func TestMaintenanceBlocksInputsUntilRelease(t *testing.T) {
	a, _ := newStubAgent(t)
	release, err := a.ReserveIdleMaintenance()
	if err != nil {
		t.Fatal(err)
	}
	if result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "late"}); result.Kind == TurnAdmissionStarted || result.Kind == TurnAdmissionQueued {
		t.Fatalf("admitted: %+v", result)
	}
	release()
	release()
	if result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "released"}); result.Kind != TurnAdmissionStarted {
		t.Fatalf("release: %+v", result)
	}
}
