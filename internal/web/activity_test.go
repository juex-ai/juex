package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
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
		got.SessionID != "session-1" ||
		got.SessionAlias != "Release prep" ||
		got.Status == nil ||
		got.Status.Cursor != "event-1" {
		t.Fatalf("activity = %+v", got)
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
	hub.publish(agentActivityResponse{
		State:     agentActivityWorking,
		SessionID: "session-1",
		Status:    &snapshot,
	})

	subscription := hub.subscribe("cursor-1")
	defer subscription.cancel()
	if len(subscription.initial) != 1 ||
		subscription.initial[0].Status == nil ||
		subscription.initial[0].Status.Cursor != "cursor-1" {
		t.Fatalf("initial = %+v", subscription.initial)
	}
}
