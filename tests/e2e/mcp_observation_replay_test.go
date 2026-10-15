package e2e

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/features/mcp"
	observable "github.com/juex-ai/juex/internal/features/observables"
)

func TestMCPObservationReplayIsIdempotentAndDistinctOccurrencesQueue(t *testing.T) {
	provider := newPendingWebProvider()
	var once sync.Once
	release := func() { once.Do(func() { close(provider.release) }) }
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), Preset: config.PresetMinimal, WorkDir: t.TempDir(), AgentStateDir: t.TempDir()}
	a, err := app.New(app.Options{Config: cfg, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		release()
		if err := a.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	notification := mcp.Notification{ServerName: "events", Method: "notifications/claude/channel", EventType: "reminder", Content: "check queue", Params: map[string]any{"content": "check queue", "meta": map[string]any{"event_id": "first", "scheduled_at": "2026-09-13T01:00:00Z"}}}
	first := a.ObservationFromMCPNotification(notification)
	done := make(chan error, 1)
	go func() { _, err := a.DeliverObservation(context.Background(), first); done <- err }()
	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("first notification did not reach Main")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	duplicate := a.ObservationFromMCPNotification(notification)
	if duplicate.ID != first.ID {
		t.Fatal("replay changed event identity")
	}
	outcome, err := a.DeliverObservation(ctx, duplicate)
	if err != nil || outcome.State != observable.ObservationStateDelivered {
		t.Fatalf("duplicate = %+v %v", outcome, err)
	}
	notification.Params = map[string]any{"content": "check queue", "meta": map[string]any{"event_id": "second", "scheduled_at": "2026-09-14T01:00:00Z"}}
	second := a.ObservationFromMCPNotification(notification)
	if second.ID == first.ID {
		t.Fatal("distinct occurrences collapsed")
	}
	outcome, err = a.DeliverObservation(ctx, second)
	if err != nil || outcome.State != observable.ObservationStateQueued {
		t.Fatalf("busy Main = %+v %v", outcome, err)
	}
	if provider.callCount() != 1 {
		t.Fatal("duplicate started another Turn")
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("delivery did not settle")
	}
}
