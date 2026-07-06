package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/web"
)

type webProvider struct {
	steps []llm.Response
	calls int
}

func (p *webProvider) Name() string { return "web-test" }
func (p *webProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	if p.calls >= len(p.steps) {
		return llm.Response{}, context.DeadlineExceeded
	}
	r := p.steps[p.calls]
	p.calls++
	return r, nil
}

func TestWeb_TurnRoundTripPersists(t *testing.T) {
	work := t.TempDir()
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "noted"), StopReason: llm.StopEndTurn},
	}}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Create session.
	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	if c.ID == "" {
		t.Fatal("no session id")
	}

	// 2. Submit a turn.
	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 202 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("turn POST status = %d body=%s", resp.StatusCode, body)
	}
	var turn webStartTurnResponse
	if err := json.NewDecoder(resp.Body).Decode(&turn); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if turn.TurnID == "" {
		t.Fatal("turn response missing turn id")
	}

	// 3. Wait until turn shows in transcript.
	waitForWebTranscript(t, ts.URL, c.ID, turn.TurnID, 30*time.Second, "assistant reply", func(messages []webTranscriptMessage) bool {
		for _, m := range messages {
			if m.Role == "assistant" {
				for _, b := range m.Blocks {
					if b.Type == "text" && b.Text == "noted" {
						return true
					}
				}
			}
		}
		return false
	})
}

type pendingWebProvider struct {
	started chan struct{}
	release chan struct{}

	mu        sync.Mutex
	calls     int
	histories [][]llm.Message
}

func newPendingWebProvider() *pendingWebProvider {
	return &pendingWebProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *pendingWebProvider) Name() string { return "web-pending-test" }

func (p *pendingWebProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.histories = append(p.histories, append([]llm.Message(nil), h...))
	p.mu.Unlock()
	if idx == 0 {
		close(p.started)
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn}, nil
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn}, nil
}

func (p *pendingWebProvider) secondHistory() []llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.histories) < 2 {
		return nil
	}
	return append([]llm.Message(nil), p.histories[1]...)
}

func TestWeb_PendingInputQueuesDuringActiveTurn(t *testing.T) {
	work := t.TempDir()
	prov := newPendingWebProvider()
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	start, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if start.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(start.Body)
		start.Body.Close()
		t.Fatalf("start status = %d body=%s", start.StatusCode, body)
	}
	start.Body.Close()
	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}

	queued, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"steer now"}`))
	if err != nil {
		t.Fatal(err)
	}
	if queued.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(queued.Body)
		queued.Body.Close()
		t.Fatalf("queued status = %d body=%s", queued.StatusCode, body)
	}
	var queuedBody struct {
		Queued       bool `json:"queued"`
		PendingCount int  `json:"pending_count"`
	}
	if err := json.NewDecoder(queued.Body).Decode(&queuedBody); err != nil {
		t.Fatal(err)
	}
	queued.Body.Close()
	if !queuedBody.Queued || queuedBody.PendingCount != 1 {
		t.Fatalf("queued body = %+v", queuedBody)
	}

	close(prov.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		history := prov.secondHistory()
		if len(history) > 0 {
			if got := history[len(history)-1].FirstText(); got != "steer now" {
				t.Fatalf("second provider call last message = %q", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("pending input never reached second provider call")
}

func TestWeb_ObservablesStartAndSurfaceObservation(t *testing.T) {
	work := t.TempDir()
	writeE2EObservableConfig(t, work)
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "observable handled"), StopReason: llm.StopEndTurn},
	}}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	if c.ID == "" {
		t.Fatal("no session id")
	}

	var snapshot struct {
		Observables []observable.ObservableStatus `json:"observables"`
	}
	var records []observable.ObservationRecord
	waitForCondition(t, 5*time.Second, func() bool {
		resp, err := http.Get(ts.URL + "/api/observables")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var next struct {
			Observables []observable.ObservableStatus `json:"observables"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&next); err != nil {
			return false
		}
		snapshot = next
		if len(next.Observables) != 1 {
			return false
		}
		last := next.Observables[0].LastObservation
		if last.ID == "" || !strings.Contains(last.Content, "observable e2e payload") {
			return false
		}
		fetched, err := fetchObservableRecords(ts.URL, next.Observables[0].ID)
		if err != nil {
			return false
		}
		records = fetched
		return len(records) == 1 && records[0].State == observable.ObservationStateDelivered
	})
	got := snapshot.Observables[0]
	if got.ID != "observable-e2e" {
		t.Fatalf("observable id = %q", got.ID)
	}
	eventsData, err := os.ReadFile(filepath.Join(work, ".juex", "sessions", c.ID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"observable.started"`, `"type":"observation.delivered"`} {
		if !strings.Contains(string(eventsData), want) {
			t.Fatalf("events missing %s:\n%s", want, eventsData)
		}
	}
}

type webStartTurnResponse struct {
	TurnID string `json:"turn_id"`
}

type webTranscriptMessage struct {
	Role   string `json:"role"`
	Blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"blocks"`
}

func waitForWebTranscript(t *testing.T, baseURL, sessionID, turnID string, timeout time.Duration, label string, match func([]webTranscriptMessage) bool) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr, lastState string
	var lastMessages []webTranscriptMessage
	for time.Now().Before(deadline) {
		messages, err := fetchWebTranscript(client, baseURL, sessionID)
		if err != nil {
			lastErr = err.Error()
		} else {
			lastMessages = messages
			if match(messages) {
				return
			}
		}
		state, turnErr, err := fetchWebTurnState(client, baseURL, sessionID, turnID)
		if err != nil {
			lastState = err.Error()
		} else {
			lastState = state
			if state == "errored" {
				t.Fatalf("turn %s errored while waiting for %s: %s", turnID, label, turnErr)
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last_state=%q last_error=%q last_messages=%+v", label, lastState, lastErr, lastMessages)
}

func waitForCondition(t *testing.T, timeout time.Duration, match func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if match() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func fetchWebTranscript(client *http.Client, baseURL, sessionID string) ([]webTranscriptMessage, error) {
	resp, err := client.Get(baseURL + "/api/sessions/" + sessionID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("session status=%d body=%s", resp.StatusCode, body)
	}
	var parsed struct {
		Messages []webTranscriptMessage `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return parsed.Messages, nil
}

func fetchObservableRecords(baseURL, id string) ([]observable.ObservationRecord, error) {
	resp, err := http.Get(baseURL + "/api/observables/" + id + "/observations")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("observations status=%d body=%s", resp.StatusCode, body)
	}
	var body struct {
		Observations []observable.ObservationRecord `json:"observations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Observations, nil
}

func writeE2EObservableConfig(t *testing.T, work string) {
	t.Helper()
	cfg := map[string]any{
		"observables": []map[string]any{
			{
				"id":      "observable-e2e",
				"command": os.Args[0],
				"args":    []string{"-test.run=TestE2EObservableHelperProcess"},
				"env":     map[string]string{"JUEX_E2E_OBSERVABLE": "1"},
				"streams": []string{"stdout"},
				"parser": map[string]string{
					"type":           "jsonl",
					"content_field":  "content",
					"kind_field":     "type",
					"severity_field": "level",
				},
				"batch": map[string]int{
					"interval_seconds": 5,
					"max_chars":        1000,
				},
			},
		},
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, ".juex", "observables.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestE2EObservableHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_E2E_OBSERVABLE") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(`{"type":"e2e_event","level":"info","content":"observable e2e payload"}` + "\n")
	os.Exit(0)
}

func fetchWebTurnState(client *http.Client, baseURL, sessionID, turnID string) (string, string, error) {
	resp, err := client.Get(baseURL + "/api/sessions/" + sessionID + "/turns/" + turnID)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("turn status=%d body=%s", resp.StatusCode, body)
	}
	var parsed struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", "", err
	}
	return parsed.State, parsed.Error, nil
}
