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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
					"state":"working",
					"session_id":"session-one",
					"status":{
						"session":{"id":"session-one","state":"turn_active"},
						"turn":{"id":"turn-original","state":"active"}
					}
				}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions/session-one/turns":
			var body struct {
				Prompt string `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode continuation body: %v", err)
			}
			gotPrompt = body.Prompt
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
		activity.State != "turn_active" ||
		activity.TurnState != "active" {
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
	if turnID != "turn-resume" || gotPrompt != restartResumePrompt {
		t.Fatalf("turn id/prompt = %q/%q", turnID, gotPrompt)
	}
}

func TestRestartClientRejectsActiveStatusWithoutSessionID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"state":"working",
			"status":{"session":{"state":"draining_pending"}}
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

func TestRestartClientRejectsActiveStatusWithoutTurnID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"state":"working",
			"session_id":"session-one",
			"status":{
				"session":{"id":"session-one","state":"turn_active"},
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
