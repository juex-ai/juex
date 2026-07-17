package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/web"
)

type webProvider struct {
	steps     []llm.Response
	calls     int
	histories [][]llm.Message
	mu        sync.Mutex
}

func (p *webProvider) Name() string { return "web-test" }
func (p *webProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls >= len(p.steps) {
		return llm.Response{}, context.DeadlineExceeded
	}
	p.histories = append(p.histories, append([]llm.Message(nil), h...))
	r := p.steps[p.calls]
	p.calls++
	return r, nil
}

func (p *webProvider) history(idx int) []llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.histories) {
		return nil
	}
	return append([]llm.Message(nil), p.histories[idx]...)
}

func TestWeb_RuntimeToolCatalogIncludesMCPDescriptorsWithoutSession(t *testing.T) {
	work := t.TempDir()
	projectAgents := filepath.Join(work, ".agents")
	if err := os.MkdirAll(projectAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	mcpConfig := mcp.Config{MCPServers: map[string]mcp.ServerSpec{
		"local": {Command: os.Args[0], Env: map[string]string{"JUEX_E2E_MCP": "1"}},
	}}
	body, err := json.Marshal(mcpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectAgents, "mcp.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: &webProvider{},
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("runtime status = %d body=%s", resp.StatusCode, payload)
	}
	var got struct {
		Tools struct {
			Count  int `json:"count"`
			Groups []struct {
				Group string `json:"group"`
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"groups"`
		} `json:"tools"`
		MCP struct {
			Servers []struct {
				Name      string `json:"name"`
				ToolCount int    `json:"tool_count"`
				Tools     []struct {
					Name        string         `json:"name"`
					Description string         `json:"description"`
					Schema      map[string]any `json:"schema"`
					Timeout     struct {
						Mode    string `json:"mode"`
						Seconds int    `json:"seconds"`
					} `json:"timeout"`
				} `json:"tools"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Tools.Count != 28 || len(got.Tools.Groups) != 8 {
		t.Fatalf("builtin catalog = %+v", got.Tools)
	}
	var observableToolNames []string
	for _, group := range got.Tools.Groups {
		if group.Group != "observable" {
			continue
		}
		for _, tool := range group.Tools {
			observableToolNames = append(observableToolNames, tool.Name)
		}
	}
	if len(observableToolNames) != 7 || !slices.Contains(observableToolNames, "schedule_create") {
		t.Fatalf("observable tools = %v, want seven including schedule_create", observableToolNames)
	}
	if len(got.MCP.Servers) != 1 || got.MCP.Servers[0].Name != "local" || got.MCP.Servers[0].ToolCount != 1 || len(got.MCP.Servers[0].Tools) != 1 {
		t.Fatalf("mcp catalog = %+v", got.MCP)
	}
	echo := got.MCP.Servers[0].Tools[0]
	properties, _ := echo.Schema["properties"].(map[string]any)
	textSchema, _ := properties["text"].(map[string]any)
	if echo.Name != "echo" || echo.Description != "Echo input" || textSchema["type"] != "string" || echo.Timeout.Mode != "bounded" || echo.Timeout.Seconds != 60 {
		t.Fatalf("echo descriptor = %+v", echo)
	}
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

func TestWeb_ComposerImageUpload(t *testing.T) {
	work := t.TempDir()
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "image noted"), StopReason: llm.StopEndTurn},
	}}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sessionID := createWebSession(t, ts.URL)
	media := uploadWebSessionImage(t, ts.URL, sessionID)
	body, err := json.Marshal(struct {
		Prompt      string         `json:"prompt"`
		Attachments []llm.MediaRef `json:"attachments"`
	}{Prompt: "describe this", Attachments: []llm.MediaRef{media}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/api/sessions/"+sessionID+"/turns", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
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

	waitForWebTranscript(t, ts.URL, sessionID, turn.TurnID, 30*time.Second, "image upload turn", func(messages []webTranscriptMessage) bool {
		hasAssistant := false
		for _, msg := range messages {
			if msg.Role != "assistant" {
				continue
			}
			for _, block := range msg.Blocks {
				if block.Type == "text" && block.Text == "image noted" {
					hasAssistant = true
				}
			}
		}
		if !hasAssistant {
			return false
		}
		for _, msg := range messages {
			if msg.Role != "user" {
				continue
			}
			hasText := false
			hasImage := false
			for _, block := range msg.Blocks {
				if block.Type == "text" && block.Text == "describe this" {
					hasText = true
				}
				if block.Type == "image" && block.Media != nil && block.Media.ArtifactPath == media.ArtifactPath {
					hasImage = true
				}
			}
			if hasText && hasImage {
				return true
			}
		}
		return false
	})

	history := prov.history(0)
	if len(history) == 0 {
		t.Fatal("provider history missing")
	}
	user := history[len(history)-1]
	if len(user.Blocks) != 2 || user.Blocks[0].Type != llm.BlockText || user.Blocks[1].Type != llm.BlockImage ||
		user.Blocks[1].Media == nil || user.Blocks[1].Media.ArtifactPath != media.ArtifactPath {
		t.Fatalf("provider user message = %+v", user)
	}
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
	var eventArtifactPath string
	waitForCondition(t, 5*time.Second, func() bool {
		messages, err := fetchWebTranscript(&http.Client{Timeout: 2 * time.Second}, ts.URL, c.ID)
		if err != nil {
			return false
		}
		for _, msg := range messages {
			for _, block := range msg.Blocks {
				if block.Type == "image" && block.Media != nil && strings.HasPrefix(block.Media.ArtifactPath, ".juex/artifacts/event-media/") {
					eventArtifactPath = block.Media.ArtifactPath
					return true
				}
			}
		}
		return false
	})
	if err := os.Remove(filepath.Join(work, ".juex", "inbox", "observable-e2e.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, filepath.FromSlash(eventArtifactPath))); err != nil {
		t.Fatalf("stored observable event artifact unavailable after source removal: %v", err)
	}
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/observables/observable-e2e", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete command observable status=%d body=%s", resp.StatusCode, body)
	}
}

func TestWeb_CreateScheduleObservableAndSurfaceObservation(t *testing.T) {
	work := t.TempDir()
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "schedule handled"), StopReason: llm.StopEndTurn},
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

	scheduledAt := time.Now().UTC().Add(time.Second)
	body, err := json.Marshal(map[string]any{
		"id":   "schedule-e2e",
		"type": "schedule",
		"schedule_config": map[string]any{
			"once": map[string]any{
				"at": scheduledAt.Format(time.RFC3339Nano),
			},
			"observation": map[string]any{
				"kind":     "heartbeat",
				"severity": "info",
				"content":  "schedule e2e payload",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/api/observables", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create schedule status=%d body=%s", resp.StatusCode, respBody)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/api/observables/schedule-e2e/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var stopped observable.ObservableStatus
	if err := json.NewDecoder(resp.Body).Decode(&stopped); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || stopped.State != observable.RunStateStopped {
		t.Fatalf("stop schedule status=%d body=%+v", resp.StatusCode, stopped)
	}

	resp, err = http.Post(ts.URL+"/api/observables/schedule-e2e/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var restarted observable.ObservableStatus
	if err := json.NewDecoder(resp.Body).Decode(&restarted); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || restarted.State != observable.RunStateRunning {
		t.Fatalf("restart schedule status=%d body=%+v", resp.StatusCode, restarted)
	}

	var snapshot struct {
		Observables []observable.ObservableStatus `json:"observables"`
	}
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
		if len(next.Observables) != 1 || next.Observables[0].SourceType != observable.SourceTypeSchedule {
			return false
		}
		last := next.Observables[0].LastObservation
		return last.SourceEventID != "" && last.Content == "schedule e2e payload" && last.State == observable.ObservationStateDelivered
	})
	if got := snapshot.Observables[0]; got.Schedule == nil || got.Schedule.LastEmittedScheduledAt == nil {
		t.Fatalf("schedule status = %+v", got)
	}
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/observables/schedule-e2e", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete schedule status=%d body=%s", resp.StatusCode, respBody)
	}
}

func TestWeb_RunScheduleObservableOnce(t *testing.T) {
	work := t.TempDir()
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "manual schedule handled"), StopReason: llm.StopEndTurn},
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
	if created.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(created.Body)
		created.Body.Close()
		t.Fatalf("create session status=%d body=%s", created.StatusCode, body)
	}
	created.Body.Close()

	scheduledAt := time.Now().UTC().Add(time.Hour)
	body, err := json.Marshal(map[string]any{
		"id":   "manual-schedule-e2e",
		"type": "schedule",
		"schedule_config": map[string]any{
			"once": map[string]any{
				"at": scheduledAt.Format(time.RFC3339Nano),
			},
			"observation": map[string]any{
				"kind":     "heartbeat",
				"severity": "info",
				"content":  "manual schedule e2e payload",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/api/observables", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create schedule status=%d body=%s", resp.StatusCode, respBody)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/api/observables/manual-schedule-e2e/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("stop schedule status=%d body=%s", resp.StatusCode, respBody)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/api/observables/manual-schedule-e2e/run", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var record observable.ObservationRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("run schedule status=%d body=%+v", resp.StatusCode, record)
	}
	if !strings.HasPrefix(record.SourceEventID, "schedule:manual-schedule-e2e:manual:") {
		t.Fatalf("manual source event id = %q", record.SourceEventID)
	}
	if record.RunID != "" {
		t.Fatalf("manual run id = %q, want empty", record.RunID)
	}

	var delivered observable.ObservationRecord
	waitForCondition(t, 5*time.Second, func() bool {
		records, err := fetchObservableRecords(ts.URL, "manual-schedule-e2e")
		if err != nil {
			return false
		}
		for _, candidate := range records {
			if candidate.SourceEventID == record.SourceEventID {
				delivered = candidate
				return candidate.State == observable.ObservationStateDelivered
			}
		}
		return false
	})
	if delivered.Content != "manual schedule e2e payload" {
		t.Fatalf("manual content = %q", delivered.Content)
	}

	resp, err = http.Get(ts.URL + "/api/observables")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Observables []observable.ObservableStatus `json:"observables"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	status := observableStatusByID(list.Observables, "manual-schedule-e2e")
	if status == nil || status.State != observable.RunStateStopped {
		t.Fatalf("schedule status after run = %+v, want stopped", status)
	}
	if status.LastObservation.SourceEventID != record.SourceEventID {
		t.Fatalf("last observation = %+v, want %q", status.LastObservation, record.SourceEventID)
	}

	resp, err = http.Post(ts.URL+"/api/observables/missing/run", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("run missing status=%d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	commandBody, err := json.Marshal(map[string]any{
		"id":   "manual-command-e2e",
		"type": "command",
		"command_config": map[string]any{
			"command": os.Args[0],
			"args":    []string{"-test.run=TestE2EQuietObservableHelperProcess"},
			"env":     map[string]string{"JUEX_E2E_QUIET_OBSERVABLE": "1"},
			"streams": []string{"stdout"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.Post(ts.URL+"/api/observables", "application/json", bytes.NewReader(commandBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create command status=%d body=%s", resp.StatusCode, respBody)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/api/observables/manual-command-e2e/run", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("run command status=%d, want %d", resp.StatusCode, http.StatusConflict)
	}
}

func TestWeb_OldObservableShapeIsVisibleAndBlocksTaggedEdits(t *testing.T) {
	work := t.TempDir()
	configBody := `{"observables":[` +
		`{"id":"legacy-command","command":"echo"},` +
		`{"id":"valid-schedule","type":"schedule","schedule_config":{"interval":{"every_seconds":3600},"observation":{"content":"valid sibling"}}}` +
		`]}`
	configPath := filepath.Join(work, ".juex", "observables.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: &webProvider{},
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var statuses []observable.ObservableStatus
	waitForCondition(t, 5*time.Second, func() bool {
		resp, err := http.Get(ts.URL + "/api/observables")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var body struct {
			Observables []observable.ObservableStatus `json:"observables"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
			return false
		}
		statuses = body.Observables
		valid, legacy := observableStatusByID(statuses, "valid-schedule"), observableStatusByID(statuses, "legacy-command")
		return valid != nil && valid.State == observable.RunStateRunning && legacy != nil && legacy.State == observable.RunStateErrored
	})
	legacy := observableStatusByID(statuses, "legacy-command")
	if legacy == nil || !strings.Contains(legacy.LastError, "type plus command_config") {
		t.Fatalf("legacy config issue = %+v, want rewrite hint", legacy)
	}

	createBody := strings.NewReader(`{"id":"blocked-command","type":"command","command_config":{"command":"echo"}}`)
	resp, err := http.Post(ts.URL+"/api/observables", "application/json", createBody)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(responseBody), "fix invalid entries before editing") {
		t.Fatalf("blocked edit status=%d body=%s", resp.StatusCode, responseBody)
	}
}

func observableStatusByID(statuses []observable.ObservableStatus, id string) *observable.ObservableStatus {
	for i := range statuses {
		if statuses[i].ID == id {
			return &statuses[i]
		}
	}
	return nil
}

type webStartTurnResponse struct {
	TurnID string `json:"turn_id"`
}

type webTranscriptMessage struct {
	Kind   string `json:"kind"`
	Role   string `json:"role"`
	Blocks []struct {
		Type  string        `json:"type"`
		Text  string        `json:"text"`
		Media *llm.MediaRef `json:"media,omitempty"`
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

func createWebSession(t *testing.T, baseURL string) string {
	t.Helper()
	created, err := http.Post(baseURL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(created.Body)
		t.Fatalf("create session status = %d body=%s", created.StatusCode, body)
	}
	var c struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.ID == "" {
		t.Fatal("no session id")
	}
	return c.ID
}

func uploadWebSessionImage(t *testing.T, baseURL, sessionID string) llm.MediaRef {
	t.Helper()
	resp := postWebSessionAttachment(t, baseURL, sessionID, "screen.png", webUploadPNG(t))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload status = %d body=%s", resp.StatusCode, body)
	}
	var ref llm.MediaRef
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func postWebSessionAttachment(t *testing.T, baseURL, sessionID, filename string, data []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/sessions/"+sessionID+"/attachments", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func webUploadPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
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
	attachmentPath := ".juex/inbox/observable-e2e.png"
	writeE2ETestPNG(t, filepath.Join(work, filepath.FromSlash(attachmentPath)))
	cfg := map[string]any{
		"observables": []map[string]any{
			{
				"id":   "observable-e2e",
				"type": "command",
				"command_config": map[string]any{
					"command": os.Args[0],
					"args":    []string{"-test.run=TestE2EObservableHelperProcess"},
					"env": map[string]string{
						"JUEX_E2E_OBSERVABLE":            "1",
						"JUEX_E2E_OBSERVABLE_ATTACHMENT": attachmentPath,
					},
					"streams": []string{"stdout"},
					"parser": map[string]string{
						"type":              "jsonl",
						"content_field":     "content",
						"kind_field":        "type",
						"severity_field":    "level",
						"attachments_field": "attachments",
					},
					"batch": map[string]int{
						"interval_seconds": 5,
						"max_chars":        1000,
					},
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
	attachment := os.Getenv("JUEX_E2E_OBSERVABLE_ATTACHMENT")
	_, _ = os.Stdout.WriteString(`{"type":"e2e_event","level":"info","content":"observable e2e payload","attachments":[{"path":"` + attachment + `","media_type":"image/png"}]}` + "\n")
	os.Exit(0)
}

func TestE2EQuietObservableHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_E2E_QUIET_OBSERVABLE") != "1" {
		return
	}
	os.Exit(0)
}

func writeE2ETestPNG(t *testing.T, path string) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
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
