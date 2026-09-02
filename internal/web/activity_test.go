package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/runtime"
)

func TestAgentStatusReportsRunningThread(t *testing.T) {
	server := NewServer(Options{})
	status := runtime.NewStatusStore(runtime.StatusSeed{
		ThreadID:    "123456",
		ThreadAlias: "Release prep",
	})
	status.Publish(events.Event{
		ID:      "event-1",
		Type:    runtime.TurnAdmittedType,
		TurnID:  "turn-1",
		Payload: runtime.TurnAdmittedPayload{},
	})
	server.threads.Store("123456", &activeThread{
		app: &app.App{
			Status: status,
		},
		StartedAt: time.Now().UTC(),
	})

	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
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
		got.SelectedStatus.Thread.ID != "123456" ||
		got.SelectedStatus.Thread.Alias != "Release prep" ||
		got.SelectedStatus.Cursor != "event-1" {
		t.Fatalf("activity = %+v", got)
	}
}

func TestAgentStatusAggregatesWorkingPendingAndSelectsThread(t *testing.T) {
	server := NewServer(Options{})
	add := func(id, alias string, pending int, started time.Time) {
		status := runtime.NewStatusStore(runtime.StatusSeed{
			ThreadID:         id,
			ThreadAlias:      alias,
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
		server.threads.Store(id, &activeThread{
			app: &app.App{
				Status: status,
			},
			StartedAt: started,
		})
	}
	now := time.Now().UTC()
	add("123456", "Old", 1, now.Add(-time.Minute))
	add("654321", "New", 2, now)

	got := server.agentActivity()
	if got.State != agentActivityWorking || got.PendingInputCount != 3 {
		t.Fatalf("activity = %+v", got)
	}
	if got.SelectedStatus == nil ||
		got.SelectedStatus.Thread.ID != "654321" ||
		got.SelectedStatus.Thread.PendingCount != 2 {
		t.Fatalf("selected activity = %+v", got)
	}
}

func TestAgentStatusRejectsNonGET(t *testing.T) {
	server := NewServer(Options{})
	request := httptest.NewRequest(http.MethodPost, "/api/status", nil)
	response := httptest.NewRecorder()

	server.APIHandler().ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

func TestAgentStatusStreamReturnsCurrentSnapshotOnSameCursorReconnect(t *testing.T) {
	server := NewServer(Options{})
	status := runtime.NewStatusStore(runtime.StatusSeed{ThreadID: "123456"})
	status.Publish(events.Event{
		ID:        "cursor-1",
		Type:      runtime.TurnAdmittedType,
		TurnID:    "turn-1",
		Timestamp: time.Now().UTC(),
	})
	server.threads.Store("123456", &activeThread{
		app: &app.App{
			Status: status,
		},
		StartedAt: time.Now().UTC(),
	})
	server.statusStream.Publish(server.agentActivity())

	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		httpServer.URL+"/api/status/events?since=older-cursor",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", "cursor-1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	scanner := bufio.NewScanner(response.Body)
	var (
		eventID string
		event   agentStatusStreamEvent
	)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "id:"):
			eventID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "data:"):
			if err := json.Unmarshal(
				[]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))),
				&event,
			); err != nil {
				t.Fatal(err)
			}
			cancel()
		}
		if event.Type != "" {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if eventID != "cursor-1" ||
		event.Type != "agent.status" ||
		event.Activity.SelectedStatus == nil ||
		event.Activity.SelectedStatus.Thread.ID != "123456" {
		t.Fatalf("event id/body = %q/%+v", eventID, event)
	}
}
