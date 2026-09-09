package agenthttp

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestThreadInputStatusPagesArchivedAndEffectiveModuleGate(t *testing.T) {
	s := newTestServer(t)
	store := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir)
	main, err := store.OpenActive(thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	main.Close()
	worker, err := store.CreateWorker(thread.MainID, "tracked", 2)
	if err != nil {
		t.Fatal(err)
	}
	scope := worker.ContextScopeID()
	first := llm.TextMessage(llm.RoleUser, "original request")
	first.ID = "msg-original"
	second := llm.TextMessage(llm.RoleUser, "not tracked")
	second.ID = "msg-untracked"
	association := events.Event{Type: runtime.InputTrackedType, Payload: runtime.InputTrackedPayload{InputID: "input-original", MessageID: first.ID, ScopeID: scope}}
	if _, err := worker.AppendAssigned(first, association); err != nil {
		t.Fatal(err)
	}
	if err := worker.Append(second); err != nil {
		t.Fatal(err)
	}
	if err := worker.AppendEvent(events.Event{Type: runtime.InputCheckedType, Payload: runtime.InputCheckedPayload{InputIDs: []string{"input-original"}, ScopeID: scope, CheckedAt: time.Now().UTC(), MessageID: "msg-answer", ToolUseID: "tool-check"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(s.APIHandler())
	defer httpServer.Close()
	enabled, disabled := true, false

	for _, tc := range []struct {
		name, preset string
		override     *bool
		want         bool
	}{
		{"standard", config.PresetStandard, nil, true},
		{"minimal", config.PresetMinimal, nil, false},
		{"standard-off", config.PresetStandard, &disabled, false},
		{"minimal-on", config.PresetMinimal, &enabled, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.opts.Cfg.Preset = tc.preset
			s.opts.Cfg.Modules = nil
			if tc.override != nil {
				s.opts.Cfg.Modules = config.ModulePolicy{"input-tracking": {Enabled: *tc.override}}
			}
			var latest threadShowResponse
			doJSON(t, http.MethodGet, httpServer.URL+"/api/threads/"+worker.ID+"?limit=1", "", http.StatusOK, &latest)
			if (latest.InputTracking != nil) != tc.want {
				t.Fatalf("gate: %+v", latest.InputTracking)
			}
			if !tc.want {
				return
			}
			if len(latest.InputTracking.Messages) != 0 {
				t.Fatal("untracked message was retroactively labeled")
			}
			var older threadShowResponse
			doJSON(t, http.MethodGet, httpServer.URL+"/api/threads/"+worker.ID+"?limit=1&before="+latest.PreviousCursor, "", http.StatusOK, &older)
			status := older.InputTracking.Messages[first.ID]
			if len(older.InputTracking.Messages) != 1 || status.CheckedAt == nil || status.CheckMessageID != "msg-answer" || status.MessageID != first.ID {
				t.Fatalf("old page association: %+v", older.InputTracking)
			}
		})
	}
}

func TestDisabledInputStatusDoesNotOpenJournals(t *testing.T) {
	s := newTestServer(t)
	s.opts.Cfg.Modules = config.ModulePolicy{"input-tracking": {Enabled: false}}
	response := threadShowResponse{}
	s.annotateInputs("nonexistent", &response)
	if response.InputTracking != nil || !reflect.DeepEqual(s.inputStatuses, runtime.InputStatusReader{}) {
		t.Fatal("disabled projection read tracking state")
	}
}

func TestInputTrackingLiveProjectionGateKeepsQueueEvents(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		status := runtime.NewStatusStore(runtime.StatusSeed{ThreadID: thread.MainID})
		stream := newBroadcaster()
		defer stream.close()
		sub := stream.subscribe()
		defer sub.unsubscribe()
		projection := browserEventProjection{status: status, stream: stream, inputTracking: enabled}
		projection.Publish(events.Event{ID: "tracked", Type: runtime.InputTrackedType, Payload: runtime.InputTrackedPayload{InputID: "input-one", MessageID: "msg-one", ScopeID: "g000001"}})
		projection.Publish(events.Event{ID: "checked", Type: runtime.InputCheckedType, Payload: runtime.InputCheckedPayload{InputIDs: []string{"input-one"}, ScopeID: "g000001", CheckedAt: time.Now().UTC()}})
		projection.Publish(events.Event{ID: "queue", Type: "pending_input.queued", Payload: runtime.PendingInputQueuedPayload{}})
		if enabled {
			if got := receiveBrowserEvent(t, sub); got.Type != runtime.InputTrackedType {
				t.Fatalf("tracked event: %+v", got)
			}
			if got := receiveBrowserEvent(t, sub); got.Type != runtime.InputCheckedType {
				t.Fatalf("checked event: %+v", got)
			}
		}
		if got := receiveBrowserEvent(t, sub); got.Type != "pending_input.queued" {
			t.Fatalf("queue event gate: %+v", got)
		}
	}
}
