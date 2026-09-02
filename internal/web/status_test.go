package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
)

func TestThreadStatusSnapshotPreservesProviderStreamingOnRefresh(t *testing.T) {
	provider := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	server := newTestServer(t)
	server.opts.Provider = provider
	active, err := server.openThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
		active.turns.wait()
	})
	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()

	response, err := http.Post(
		httpServer.URL+"/api/threads/0/inputs",
		"application/json",
		strings.NewReader(`{"prompt":"stream"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("input status = %d body = %s", response.StatusCode, body)
	}
	select {
	case <-provider.started:
	case <-time.After(30 * time.Second):
		t.Fatal("provider did not start")
	}

	statusResponse, err := http.Get(httpServer.URL + "/api/threads/0/status")
	if err != nil {
		t.Fatal(err)
	}
	defer statusResponse.Body.Close()
	var snapshot runtime.StatusSnapshot
	if err := json.NewDecoder(statusResponse.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Turn == nil ||
		snapshot.Turn.Phase != runtime.TurnPhaseProviderIteration ||
		!snapshot.Turn.Streaming ||
		!snapshot.Thread.CanAcceptInput {
		t.Fatalf("status = %+v", snapshot)
	}
}

func TestStatusRoutesExposePublicDTOOnly(t *testing.T) {
	server := newTestServer(t)
	active, err := server.openThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	active.app.Status = runtime.NewStatusStore(runtime.StatusSeed{ThreadID: thread.MainID, ThreadAlias: thread.MainAlias, MaxPendingInputs: 4})
	active.app.Status.Publish(events.Event{ID: "event-admitted", Type: runtime.TurnAdmittedType, TurnID: "turn-one", Timestamp: now})
	active.app.Status.Publish(events.Event{
		ID: "event-tool-phase", Type: runtime.TurnPhaseType, TurnID: "turn-one", Timestamp: now,
		Payload: runtime.TurnPhasePayload{Phase: runtime.TurnPhaseToolBatch},
	})
	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()

	for _, path := range []string{"/api/threads/0/status", "/api/status"} {
		response, err := http.Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, response.StatusCode, body)
		}
		if strings.Contains(string(body), "resume_state") || strings.Contains(string(body), "resume_phase") {
			t.Fatalf("%s leaked recovery bookkeeping: %s", path, body)
		}
		if !strings.Contains(string(body), `"working": true`) {
			t.Fatalf("%s omitted computed working: %s", path, body)
		}
	}
}

func TestThreadStatusStreamResumesAfterSnapshotCursor(t *testing.T) {
	server := newTestServer(t)
	active, err := server.openThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	if err := active.app.Engine.ReserveTurnID("turn-1"); err != nil {
		t.Fatal(err)
	}
	cursor := active.app.Status.Snapshot().Cursor
	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/threads/0/status/events?since="+cursor, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if err := active.app.Bus.Emit(events.Event{
		Type: runtime.TurnPhaseType, TurnID: "turn-1",
		Payload: runtime.TurnPhasePayload{Phase: runtime.TurnPhaseToolBatch},
	}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	var snapshot runtime.StatusSnapshot
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Cursor != cursor {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if snapshot.Cursor == "" || snapshot.Cursor == cursor || snapshot.Turn == nil || snapshot.Turn.Phase != runtime.TurnPhaseToolBatch {
		t.Fatalf("resumed status = %+v, snapshot cursor = %q", snapshot, cursor)
	}
}

func TestSSEResumeCursorPrefersLastEventIDOnReconnect(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/events?since=initial-cursor", nil)
	request.Header.Set("Last-Event-ID", "latest-cursor")
	if got := sseResumeCursor(request); got != "latest-cursor" {
		t.Fatalf("resume cursor = %q, want latest-cursor", got)
	}
}

func TestSSEResumeCursorPresenceContract(t *testing.T) {
	for _, target := range []string{"/events?since=", "/events?since=%20", "/events"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		cursor, present := sseResumeCursorWithPresence(request)
		if cursor != "" || present {
			t.Fatalf("%s: cursor = %q, present = %v", target, cursor, present)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/events?since=real-cursor", nil)
	cursor, present := sseResumeCursorWithPresence(request)
	if cursor != "real-cursor" || !present {
		t.Fatalf("cursor = %q, present = %v", cursor, present)
	}

	request = httptest.NewRequest(http.MethodGet, "/events?"+sseReplayParam+"="+url.QueryEscape(sseReplayJournalStart), nil)
	cursor, present = sseResumeCursorWithPresence(request)
	if cursor != "" || !present {
		t.Fatalf("journal-start cursor = %q, present = %v", cursor, present)
	}
}

func TestPersistedWorkerStatusReadDoesNotOpenRuntime(t *testing.T) {
	server := newTestServer(t)
	store := thread.NewStore(server.opts.Cfg.RuntimePaths().StateDir)
	worker, err := store.CreateWorker(thread.MainID, "status-only")
	if err != nil {
		t.Fatal(err)
	}
	workerID := worker.ID
	if err := worker.AppendEvent(events.Event{ID: "worker-status", Type: runtime.TurnAdmittedType, TurnID: "turn-worker"}); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}

	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()
	response, err := http.Get(httpServer.URL + "/api/threads/" + workerID + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var snapshot runtime.StatusSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Thread.ID != workerID || snapshot.Cursor != "worker-status" {
		t.Fatalf("persisted status = %+v", snapshot)
	}
	if _, loaded := server.threads.Load(workerID); loaded {
		t.Fatal("status read opened Worker runtime")
	}
}
