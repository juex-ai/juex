package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/endpoint"
)

func TestRestartClientReadsActivityAndPostsContinuation(t *testing.T) {
	var gotPrompt string
	var gotKind string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
					"state":"working",
					"pending_input_count":0,
					"selected_status":{
						"session":{"id":"session-one","state":"turn_active","working":true},
						"turn":{
							"id":"turn-original",
							"state":"active",
							"error":{"message":"restart","kind":"runtime_restart"}
						}
					}
				}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions/session-one/turns":
			var body struct {
				Prompt string `json:"prompt"`
				Kind   string `json:"kind"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode continuation body: %v", err)
			}
			gotPrompt = body.Prompt
			gotKind = body.Kind
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"turn_id":"turn-resume"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	state := endpoint.Runtime{
		Endpoint: "tcp://" + strings.TrimPrefix(server.URL, "http://"),
	}

	activity, err := readRestartActivity(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if activity.SessionID != "session-one" ||
		activity.TurnID != "turn-original" ||
		activity.State != "working" ||
		activity.TurnState != "active" ||
		activity.TurnErrorKind != "runtime_restart" {
		t.Fatalf("activity = %+v", activity)
	}
	turnID, err := postRestartResume(
		context.Background(),
		state,
		activity.SessionID,
		restartResumePrompt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if turnID != "turn-resume" ||
		gotPrompt != restartResumePrompt ||
		gotKind != "system_notice" {
		t.Fatalf("turn id/prompt/kind = %q/%q/%q", turnID, gotPrompt, gotKind)
	}
}

func TestRestartClientRejectsActiveStatusWithoutSessionID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"state":"working",
			"pending_input_count":0,
			"selected_status":{"session":{"state":"draining_pending","working":true}}
		}`))
	}))
	defer server.Close()

	_, err := readRestartActivity(context.Background(), endpoint.Runtime{
		Endpoint: "tcp://" + strings.TrimPrefix(server.URL, "http://"),
	})
	if err == nil || !strings.Contains(err.Error(), "omitted session id") {
		t.Fatalf("error = %v", err)
	}
}

func TestRestartClientRejectsActiveStatusWithoutSelectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"state":"working","pending_input_count":0}`))
	}))
	defer server.Close()

	_, err := readRestartActivity(context.Background(), endpoint.Runtime{
		Endpoint: "tcp://" + strings.TrimPrefix(server.URL, "http://"),
	})
	if err == nil || !strings.Contains(err.Error(), "omitted selected status") {
		t.Fatalf("error = %v", err)
	}
}

func TestRestartClientRejectsActiveStatusWithoutTurnID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"state":"working",
			"pending_input_count":0,
			"selected_status":{
				"session":{"id":"session-one","state":"turn_active","working":true},
				"turn":{"state":"active"}
			}
		}`))
	}))
	defer server.Close()

	_, err := readRestartActivity(context.Background(), endpoint.Runtime{
		Endpoint: "tcp://" + strings.TrimPrefix(server.URL, "http://"),
	})
	if err == nil || !strings.Contains(err.Error(), "omitted turn id") {
		t.Fatalf("error = %v", err)
	}
}
