package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
)

func TestSessionStatusSnapshotPreservesProviderStreamingOnRefresh(t *testing.T) {
	provider := &blockingProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	srv := newTestServer(t)
	srv.opts.Provider = provider
	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
		as.turns.wait()
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	response, err := http.Post(
		ts.URL+"/api/sessions/"+as.app.Session.ID+"/turns",
		"application/json",
		strings.NewReader(`{"prompt":"stream"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}

	statusResponse, err := http.Get(ts.URL + "/api/sessions/" + as.app.Session.ID + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer statusResponse.Body.Close()
	var snapshot juexruntime.StatusSnapshot
	if err := json.NewDecoder(statusResponse.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Turn == nil ||
		snapshot.Turn.Phase != juexruntime.TurnPhaseProviderIteration ||
		!snapshot.Turn.Streaming ||
		!snapshot.Session.CanAcceptInput {
		t.Fatalf("status = %+v", snapshot)
	}
}

func TestStatusRoutesExposePublicDTOOnly(t *testing.T) {
	srv := newTestServer(t)
	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	as.app.Status = juexruntime.NewStatusStore(juexruntime.StatusSeed{
		SessionID:        as.app.Session.ID,
		MaxPendingInputs: 4,
	})
	as.app.Status.Publish(events.Event{
		ID:        "event-admitted",
		Type:      juexruntime.TurnAdmittedType,
		TurnID:    "turn-one",
		Timestamp: now,
	})
	as.app.Status.Publish(events.Event{
		ID:        "event-tool-phase",
		Type:      juexruntime.TurnPhaseType,
		TurnID:    "turn-one",
		Timestamp: now,
		Payload: juexruntime.TurnPhasePayload{
			Phase: juexruntime.TurnPhaseToolBatch,
		},
	})
	as.app.Status.Publish(events.Event{
		ID:        "event-compact",
		Type:      "context.compact.started",
		TurnID:    "turn-one",
		Timestamp: now,
		Payload:   juexruntime.ContextCompactStartedPayload{},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, path := range []string{
		"/api/sessions/" + as.app.Session.ID + "/status",
		"/api/status",
	} {
		response, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, response.StatusCode, body)
		}
		if strings.Contains(string(body), "resume_state") ||
			strings.Contains(string(body), "resume_phase") {
			t.Fatalf("%s leaked recovery bookkeeping: %s", path, body)
		}
		if !strings.Contains(string(body), `"working": true`) {
			t.Fatalf("%s omitted computed working: %s", path, body)
		}
		if path == "/api/status" {
			var activity map[string]json.RawMessage
			if err := json.Unmarshal(body, &activity); err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"state", "pending_input_count", "selected_status"} {
				if _, ok := activity[required]; !ok {
					t.Fatalf("%s omitted %q: %s", path, required, body)
				}
			}
			for _, forbidden := range []string{
				"session_id",
				"session_alias",
				"pending_count",
				"status",
			} {
				if _, ok := activity[forbidden]; ok {
					t.Fatalf("%s leaked compatibility field %q: %s", path, forbidden, body)
				}
			}
		}
	}
}

func TestSessionStatusStreamResumesAfterSnapshotCursor(t *testing.T) {
	srv := newTestServer(t)
	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	if err := as.app.Engine.ReserveTurnID("turn-1"); err != nil {
		t.Fatal(err)
	}
	cursor := as.app.Status.Snapshot().Cursor
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		ts.URL+"/api/sessions/"+as.app.Session.ID+"/status/events?since="+cursor,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	as.app.Bus.Emit(events.Event{
		Type:   juexruntime.TurnPhaseType,
		TurnID: "turn-1",
		Payload: juexruntime.TurnPhasePayload{
			Phase: juexruntime.TurnPhaseToolBatch,
		},
	})
	scanner := bufio.NewScanner(response.Body)
	var snapshot juexruntime.StatusSnapshot
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Cursor == cursor {
			continue
		}
		break
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if snapshot.Cursor == "" || snapshot.Cursor == cursor ||
		snapshot.Turn == nil || snapshot.Turn.Phase != juexruntime.TurnPhaseToolBatch {
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

func TestSSEResumeCursorPreservesExplicitEmptySince(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/events?since=", nil)
	cursor, present := sseResumeCursorWithPresence(request)
	if cursor != "" || !present {
		t.Fatalf("resume cursor = %q, present = %v; want empty and present", cursor, present)
	}
}

func TestHistoricalSessionStatusDoesNotActivateIt(t *testing.T) {
	srv := newTestServer(t)
	historical, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	if err := historical.app.Session.Append(llm.TextMessage(llm.RoleUser, "historical")); err != nil {
		t.Fatal(err)
	}
	historicalID := historical.app.Session.ID
	historicalDir := historical.app.Session.Dir

	current, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.app.Session.Append(llm.TextMessage(llm.RoleUser, "current")); err != nil {
		t.Fatal(err)
	}
	currentID := current.app.Session.ID
	if _, loaded := srv.sessions.Load(historicalID); loaded {
		t.Fatal("historical primary remained active in memory")
	}
	if err := os.WriteFile(filepath.Join(historicalDir, "events.jsonl"), []byte(
		"{\"id\":\"status-1\",\"type\":\"turn.admitted\",\"turn_id\":\"turn-1\"}\nnot-json\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	response, err := http.Get(ts.URL + "/api/sessions/" + historicalID + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var snapshot juexruntime.StatusSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.ID != historicalID {
		t.Fatalf("status session = %q, want %q", snapshot.Session.ID, historicalID)
	}
	if snapshot.Cursor != "status-1" {
		t.Fatalf("status cursor = %q, want recovered cursor status-1", snapshot.Cursor)
	}
	if _, loaded := srv.sessions.Load(historicalID); loaded {
		t.Fatal("status read activated historical primary")
	}
	history, err := session.LoadHistory(srv.opts.Cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if history.Active == nil || history.Active.ID != currentID {
		t.Fatalf("active history = %+v, want current %q", history.Active, currentID)
	}
}
