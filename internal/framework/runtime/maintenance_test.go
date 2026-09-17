package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestIdleMaintenanceRejectsInputBeforePersistence(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	release, err := eng.ReserveIdleMaintenance()
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.ReceivePendingInput(context.Background(), PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, "new observation"), Options: &PendingInputOptions{ID: "during-maintenance", TTL: time.Hour}, DeferDelivery: true})
	if !errors.Is(err, ErrMaintenance) {
		t.Fatalf("ingress error=%v", err)
	}
	if _, found, err := eng.PersistedPendingMessage("during-maintenance"); err != nil || found {
		t.Fatalf("input accepted during maintenance: found=%v err=%v", found, err)
	}
	if err := eng.ReserveTurnID("late"); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("turn reservation=%v", err)
	}
	release()
	if _, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, "after rollback")}); err != nil {
		t.Fatal(err)
	}
}

func TestIdleMaintenanceRejectsDurableDeferredInput(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	_, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, "waiting recovery"), Options: &PendingInputOptions{ID: "deferred", TTL: time.Hour}, DeferDelivery: true})
	if err != nil {
		t.Fatal(err)
	}
	if release, err := eng.ReserveIdleMaintenance(); err == nil {
		release()
		t.Fatal("durable deferred input was treated as idle")
	}
}
