package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/statusapi"
)

func TestAgentActivityReportsRunningSession(t *testing.T) {
	server := NewServer(Options{})
	turns := newWebTurnTransport(nil)
	turns.activeTurn = "turn-1"
	turns.states["turn-1"] = &webTurnState{
		ID:    "turn-1",
		State: "running",
	}
	status := runtime.NewStatusStore(runtime.StatusSeed{
		SessionID:    "session-1",
		SessionAlias: "Release prep",
	})
	status.Publish(events.Event{
		ID:      "event-1",
		Type:    runtime.TurnAdmittedType,
		TurnID:  "turn-1",
		Payload: runtime.TurnAdmittedPayload{},
	})
	server.sessions.Store("session-1", &activeSession{
		app: &app.App{
			Session: &session.Session{ID: "session-1", Alias: "Release prep"},
			Status:  status,
		},
		StartedAt: time.Now().UTC(),
		turns:     turns,
	})

	request := httptest.NewRequest(http.MethodGet, "/api/activity", nil)
	response := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got agentActivityResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode activity: %v", err)
	}
	if got.State != agentActivityWorking ||
		got.PendingInputCount != 0 ||
		got.SelectedStatus == nil ||
		got.SelectedStatus.Session.ID != "session-1" ||
		got.SelectedStatus.Session.Alias != "Release prep" ||
		got.SelectedStatus.Cursor != "event-1" {
		t.Fatalf("activity = %+v", got)
	}
}

func TestAgentActivityAggregatesWorkingPendingAndMirrorsSelectedSession(t *testing.T) {
	server := NewServer(Options{})
	add := func(id, alias string, pending int, started time.Time) {
		status := runtime.NewStatusStore(runtime.StatusSeed{
			SessionID:        id,
			SessionAlias:     alias,
			MaxPendingInputs: 4,
		})
		status.Publish(events.Event{
			ID:      id + "-admitted",
			Type:    runtime.TurnAdmittedType,
			TurnID:  "turn-" + id,
			Payload: runtime.TurnAdmittedPayload{},
		})
		status.Publish(events.Event{
			ID:     id + "-queued",
			Type:   "pending_input.queued",
			TurnID: "turn-" + id,
			Payload: runtime.PendingInputQueuedPayload{
				PendingCount: pending, MaxPendingInputs: 4,
			},
		})
		server.sessions.Store(id, &activeSession{
			app: &app.App{
				Session: &session.Session{ID: id, Alias: alias},
				Status:  status,
			},
			StartedAt: started,
		})
	}
	now := time.Now().UTC()
	add("session-old", "Old", 1, now.Add(-time.Minute))
	add("session-new", "New", 2, now)

	got := server.agentActivity()
	if got.State != agentActivityWorking || got.PendingInputCount != 3 {
		t.Fatalf("activity = %+v", got)
	}
	if got.SelectedStatus == nil ||
		got.SelectedStatus.Session.ID != "session-new" ||
		got.SelectedStatus.Session.PendingCount != 2 {
		t.Fatalf("selected activity = %+v", got)
	}
}

func TestAgentActivityRejectsNonGET(t *testing.T) {
	server := NewServer(Options{})
	request := httptest.NewRequest(http.MethodPost, "/api/activity", nil)
	response := httptest.NewRecorder()

	server.APIHandler().ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

func TestAgentStatusHubSameCursorSubscriptionReturnsCurrentSnapshot(t *testing.T) {
	hub := newAgentStatusHub()
	status := runtime.NewStatusStore(runtime.StatusSeed{SessionID: "session-1"})
	status.Publish(events.Event{
		ID:        "cursor-1",
		Type:      runtime.TurnAdmittedType,
		TurnID:    "turn-1",
		Timestamp: time.Now().UTC(),
	})
	snapshot := status.Snapshot()
	publicStatus := statusapi.FromRuntime(snapshot)
	hub.publish(agentActivityResponse{
		State:          agentActivityWorking,
		SelectedStatus: &publicStatus,
	})

	subscription := hub.subscribe("cursor-1")
	defer subscription.cancel()
	if len(subscription.initial) != 1 ||
		subscription.initial[0].SelectedStatus == nil ||
		subscription.initial[0].SelectedStatus.Cursor != "cursor-1" {
		t.Fatalf("initial = %+v", subscription.initial)
	}
}

func TestAgentStatusHubInitialSubscriptionIsIdle(t *testing.T) {
	hub := newAgentStatusHub()
	subscription := hub.subscribe("")
	defer subscription.cancel()

	if len(subscription.initial) != 1 || subscription.initial[0].State != agentActivityIdle {
		t.Fatalf("initial = %+v, want idle activity", subscription.initial)
	}
}

func TestAgentStatusHubConcurrentPublishDeliversCurrentStatusLast(t *testing.T) {
	hub := newAgentStatusHub()
	subscriptions := make([]agentStatusSubscription, 64)
	for i := range subscriptions {
		subscriptions[i] = hub.subscribe("")
		defer subscriptions[i].cancel()
	}

	const publishCount = 256
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(publishCount)
	for i := 1; i <= publishCount; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			hub.publish(agentActivityResponse{
				SelectedStatus: &statusapi.Snapshot{
					Session: statusapi.SessionStatus{ID: strconv.Itoa(i)},
				},
			})
		}(i)
	}
	close(start)
	wg.Wait()

	hub.mu.Lock()
	want := hub.current
	hub.mu.Unlock()
	for i, subscription := range subscriptions {
		var last agentActivityResponse
		for {
			select {
			case last = <-subscription.updates:
			default:
				if selectedSessionID(last) != selectedSessionID(want) {
					t.Fatalf(
						"subscription %d last session = %q, want %q",
						i,
						selectedSessionID(last),
						selectedSessionID(want),
					)
				}
				goto nextSubscription
			}
		}
	nextSubscription:
	}
}

func selectedSessionID(activity agentActivityResponse) string {
	if activity.SelectedStatus == nil {
		return ""
	}
	return activity.SelectedStatus.Session.ID
}
