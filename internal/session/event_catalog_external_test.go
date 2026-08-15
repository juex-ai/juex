package session_test

import (
	"testing"

	"github.com/juex-ai/juex/internal/eventcatalog"
	"github.com/juex-ai/juex/internal/events"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
)

func TestEventReplayUsesCatalogCodec(t *testing.T) {
	catalog := eventcatalog.Default()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := sess.Dir
	prepared, err := catalog.Prepare(events.Event{
		Type:    "turn.started",
		Payload: juexruntime.TurnStartedPayload{Input: "hello", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendEvent(prepared); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	replayed, err := session.ReadEventsWithCatalog(dir, catalog)
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := replayed[0].Payload.(juexruntime.TurnStartedPayload)
	if !ok || payload.Input != "hello" {
		t.Fatalf("replayed payload = %#v", replayed[0].Payload)
	}
}

func TestEventReplayHonorsUnknownPolicy(t *testing.T) {
	for _, test := range []struct {
		name      string
		policy    events.ReplayPolicy
		wantError bool
	}{
		{name: "required", policy: events.ReplayRequired, wantError: true},
		{name: "ignorable", policy: events.ReplayIgnorable},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := eventcatalog.Default()
			sess, err := session.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir := sess.Dir
			prepared, err := catalog.Prepare(events.Event{
				Type:          "plugin.future",
				SchemaVersion: 2,
				ReplayPolicy:  test.policy,
				Payload:       map[string]any{"value": "future"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.AppendEvent(prepared); err != nil {
				t.Fatal(err)
			}
			if err := sess.Close(); err != nil {
				t.Fatal(err)
			}

			replayed, err := session.ReadEventsWithCatalog(dir, catalog)
			if test.wantError {
				if err == nil {
					t.Fatal("ReadEventsWithCatalog() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(replayed) != 1 || !replayed[0].Opaque {
				t.Fatalf("replayed events = %+v, want one opaque event", replayed)
			}
		})
	}
}
