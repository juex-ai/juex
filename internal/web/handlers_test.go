package web

import (
	"context"
	"encoding/json"
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
	"github.com/juex-ai/juex/internal/session"
)

// seedSession writes a minimal conversation.jsonl under
// <work>/.juex/sessions/<id>/ so session.List can find it.
func seedSession(t *testing.T, work, id, body string) {
	t.Helper()
	dir := filepath.Join(work, ".juex", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGetSessionsList_ReturnsSeededSession(t *testing.T) {
	srv := newTestServer(t)
	seedSession(t, srv.opts.Cfg.WorkDir, "20260507T101010-aaaa11",
		`{"role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed struct {
		Sessions []struct {
			ID      string `json:"id"`
			Preview string `json:"preview"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 1 || parsed.Sessions[0].ID != "20260507T101010-aaaa11" {
		t.Errorf("got %+v", parsed.Sessions)
	}
	if parsed.Sessions[0].Preview != "hi" {
		t.Errorf("preview = %q", parsed.Sessions[0].Preview)
	}
}

func TestGetSessionsList_ReturnsKindAndActive(t *testing.T) {
	srv := newTestServer(t)
	primaryID := "20260507T101010-primary1"
	sideID := "20260507T111010-side0001"
	seedSession(t, srv.opts.Cfg.WorkDir, primaryID,
		`{"role":"user","blocks":[{"type":"text","text":"primary"}]}`+"\n")
	seedSession(t, srv.opts.Cfg.WorkDir, sideID,
		`{"role":"user","blocks":[{"type":"text","text":"side"}]}`+"\n")
	sideDir := filepath.Join(srv.opts.Cfg.SessionsDir(), sideID)
	if err := session.SetKind(sideDir, session.KindSide); err != nil {
		t.Fatal(err)
	}
	primary, _, err := session.LoadInfo(filepath.Join(srv.opts.Cfg.SessionsDir(), primaryID))
	if err != nil {
		t.Fatal(err)
	}
	side, _, err := session.LoadInfo(sideDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), primary); err != nil {
		t.Fatal(err)
	}
	if err := session.RecordSession(srv.opts.Cfg.HistoryPath(), side); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed struct {
		Sessions []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Active bool   `json:"active"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct {
		Kind   string
		Active bool
	}{}
	for _, info := range parsed.Sessions {
		byID[info.ID] = struct {
			Kind   string
			Active bool
		}{Kind: info.Kind, Active: info.Active}
	}
	if byID[primaryID].Kind != session.KindPrimary || !byID[primaryID].Active {
		t.Fatalf("primary info = %+v", byID[primaryID])
	}
	if byID[sideID].Kind != session.KindSide || byID[sideID].Active {
		t.Fatalf("side info = %+v", byID[sideID])
	}
}

func TestGetSessionShow_ReturnsTranscript(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-show01"
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, id, body)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed struct {
		ID       string `json:"id"`
		Model    string `json:"model"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ID != id || len(parsed.Messages) != 2 {
		t.Errorf("got %+v", parsed)
	}
	if parsed.Model != "m" {
		t.Errorf("model = %q", parsed.Model)
	}
}

func TestGetSessionShow_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestPostSessionCompact(t *testing.T) {
	srv := newTestServer(t)
	id := "20260515T010203-webcompact"
	seedSession(t, srv.opts.Cfg.WorkDir, id,
		`{"id":"m1","role":"user","blocks":[{"type":"text","text":"`+strings.Repeat("old ", 200)+`"}]}`+"\n")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/compact", "application/json", strings.NewReader(`{"reason":"manual"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"message_id"`) || !strings.Contains(string(body), `"manual"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestPostTurn_StatusSlashReturnsCommand(t *testing.T) {
	prov := newPendingProvider()
	work := t.TempDir()
	srv := NewServer(Options{
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

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"/status"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var parsed struct {
		Command struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"command"`
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Command.Name != "/status" || !strings.Contains(parsed.Command.Text, "Juex status") || parsed.TurnID != "" {
		t.Fatalf("parsed = %+v", parsed)
	}
	prov.mu.Lock()
	calls := prov.calls
	prov.mu.Unlock()
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
}

func TestPostTurn_NewSlashCreatesActivePrimary(t *testing.T) {
	srv := newTestServer(t)
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

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"/new"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var parsed struct {
		Command struct {
			Name   string `json:"name"`
			Text   string `json:"text"`
			Status struct {
				SessionID   string `json:"session_id"`
				SessionKind string `json:"session_kind"`
				Active      bool   `json:"active"`
			} `json:"status"`
		} `json:"command"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Command.Name != "/new" || parsed.Command.Status.SessionID == "" || parsed.Command.Status.SessionID == c.ID {
		t.Fatalf("command = %+v, old id = %s", parsed.Command, c.ID)
	}
	if parsed.Command.Status.SessionKind != session.KindPrimary || !parsed.Command.Status.Active {
		t.Fatalf("status = %+v, want active primary", parsed.Command.Status)
	}
	if _, ok := srv.sessions.Load(c.ID); ok {
		t.Fatalf("old session %s still registered", c.ID)
	}
	if _, ok := srv.sessions.Load(parsed.Command.Status.SessionID); !ok {
		t.Fatalf("new session %s not registered", parsed.Command.Status.SessionID)
	}
	h, err := session.LoadHistory(srv.opts.Cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != parsed.Command.Status.SessionID {
		t.Fatalf("history active = %+v, want %s", h.Active, parsed.Command.Status.SessionID)
	}
}

func TestPostTurn_UnknownSlashStartsAgentTurn(t *testing.T) {
	srv := newTestServer(t)
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

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"/bogus"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var turn struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&turn); err != nil {
		t.Fatal(err)
	}
	if turn.TurnID == "" {
		t.Fatal("missing turn id")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		show, err := http.Get(ts.URL + "/api/sessions/" + c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if show.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(show.Body)
			show.Body.Close()
			t.Fatalf("status = %d body = %s", show.StatusCode, body)
		}
		var parsed struct {
			Messages []struct {
				Role   string `json:"role"`
				Blocks []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"blocks"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(show.Body).Decode(&parsed); err != nil {
			show.Body.Close()
			t.Fatal(err)
		}
		show.Body.Close()
		var sawUser, sawAssistant bool
		for _, msg := range parsed.Messages {
			for _, block := range msg.Blocks {
				if msg.Role == "user" && block.Type == "text" && block.Text == "/bogus" {
					sawUser = true
				}
				if msg.Role == "assistant" && block.Type == "text" && block.Text == "ack" {
					sawAssistant = true
				}
			}
		}
		if sawUser && sawAssistant {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for unknown slash prompt to be handled as a normal turn")
}

func TestPostTurn_CompactSlashConflictsWhileRunning(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
	)
	work := t.TempDir()
	srv := NewServer(Options{
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

	first, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}

	compact, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"/compact"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer compact.Body.Close()
	if compact.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(compact.Body)
		t.Fatalf("status = %d body = %s", compact.StatusCode, body)
	}
	close(prov.release)
}

func TestPostTurn_ConflictsWhileCompacting(t *testing.T) {
	srv := newTestServer(t)
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

	v, ok := srv.sessions.Load(c.ID)
	if !ok {
		t.Fatal("created session not active")
	}
	as := v.(*activeSession)
	as.cancelMu.Lock()
	as.compacting = true
	as.cancelMu.Unlock()

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

type pendingProvider struct {
	started   chan struct{}
	release   chan struct{}
	responses []llm.Response

	mu        sync.Mutex
	calls     int
	histories [][]llm.Message
}

func newPendingProvider(responses ...llm.Response) *pendingProvider {
	return &pendingProvider{
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		responses: responses,
	}
}

func (p *pendingProvider) Name() string { return "pending-test" }

func (p *pendingProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
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
	}
	if idx >= len(p.responses) {
		return llm.Response{}, context.DeadlineExceeded
	}
	return p.responses[idx], nil
}

func (p *pendingProvider) history(idx int) []llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.histories) {
		return nil
	}
	return append([]llm.Message(nil), p.histories[idx]...)
}

func TestPostTurn_QueuesWhileRunning(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
	)
	work := t.TempDir()
	srv := NewServer(Options{
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

	first, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(first.Body)
		first.Body.Close()
		t.Fatalf("first status = %d body = %s", first.StatusCode, body)
	}
	var firstTurn struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstTurn); err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}

	second, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"follow up"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(second.Body)
		t.Fatalf("second status = %d body = %s", second.StatusCode, body)
	}
	var queued struct {
		TurnID       string `json:"turn_id"`
		Queued       bool   `json:"queued"`
		PendingCount int    `json:"pending_count"`
	}
	if err := json.NewDecoder(second.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if !queued.Queued || queued.TurnID != firstTurn.TurnID || queued.PendingCount != 1 {
		t.Fatalf("queued response = %+v, first turn = %+v", queued, firstTurn)
	}

	statusResp, err := http.Get(ts.URL + "/api/sessions/" + c.ID + "/turns/" + firstTurn.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		State        string `json:"state"`
		PendingCount int    `json:"pending_count"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	statusResp.Body.Close()
	if status.State != "running" || status.PendingCount != 1 {
		t.Fatalf("turn status = %+v", status)
	}

	close(prov.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		show, err := http.Get(ts.URL + "/api/sessions/" + c.ID)
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Messages []struct {
				Role   string `json:"role"`
				Blocks []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"blocks"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(show.Body).Decode(&parsed); err != nil {
			show.Body.Close()
			t.Fatal(err)
		}
		show.Body.Close()
		if transcriptContains(parsed.Messages, "follow up") && transcriptContains(parsed.Messages, "second") {
			secondHistory := prov.history(1)
			if len(secondHistory) == 0 || secondHistory[len(secondHistory)-1].FirstText() != "follow up" {
				t.Fatalf("second provider history = %+v", secondHistory)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for queued input transcript")
}

func TestPostTurn_QueuesBeforeEngineGoroutineStarts(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
	)
	work := t.TempDir()
	srv := NewServer(Options{
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

	first, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	var firstTurn struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstTurn); err != nil {
		t.Fatal(err)
	}
	first.Body.Close()

	second, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"follow up"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(second.Body)
		t.Fatalf("second status = %d body = %s", second.StatusCode, body)
	}
	var queued struct {
		TurnID       string `json:"turn_id"`
		Queued       bool   `json:"queued"`
		PendingCount int    `json:"pending_count"`
	}
	if err := json.NewDecoder(second.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if !queued.Queued || queued.TurnID != firstTurn.TurnID {
		t.Fatalf("queued response = %+v, first turn = %+v", queued, firstTurn)
	}
	close(prov.release)
}

func transcriptContains(messages []struct {
	Role   string `json:"role"`
	Blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"blocks"`
}, text string) bool {
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if block.Type == "text" && block.Text == text {
				return true
			}
		}
	}
	return false
}

func TestGetSessionContext(t *testing.T) {
	srv := newTestServer(t)
	id := "20260515T010203-webctx"
	seedSession(t, srv.opts.Cfg.WorkDir, id, `{"id":"m1","role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/context")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"messages"`) || !strings.Contains(string(body), `"hi"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestPostCreateSession_ReturnsIDAndDir(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed struct {
		ID           string `json:"id"`
		Dir          string `json:"dir"`
		Kind         string `json:"kind"`
		Active       bool   `json:"active"`
		LastActiveAt string `json:"last_active_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ID == "" || parsed.Dir == "" || parsed.LastActiveAt == "" {
		t.Errorf("got %+v", parsed)
	}
	if parsed.Kind != session.KindPrimary || !parsed.Active {
		t.Fatalf("created session kind/active = %q/%v, want primary active", parsed.Kind, parsed.Active)
	}
	h, err := session.LoadHistory(srv.opts.Cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != parsed.ID {
		t.Fatalf("history active = %+v, want created session", h.Active)
	}
	if _, err := os.Stat(filepath.Join(parsed.Dir, "conversation.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("conversation stat err = %v, want not exist before first message", err)
	}
	// The created session must show up in subsequent List call.
	resp2, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), parsed.ID) {
		t.Errorf("created id %q not found in list:\n%s", parsed.ID, body)
	}

	show, err := http.Get(ts.URL + "/api/sessions/" + parsed.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer show.Body.Close()
	if show.StatusCode != http.StatusOK {
		t.Fatalf("show status = %d", show.StatusCode)
	}
	var shown struct {
		ID       string `json:"id"`
		Messages []any  `json:"messages"`
	}
	if err := json.NewDecoder(show.Body).Decode(&shown); err != nil {
		t.Fatal(err)
	}
	if shown.ID != parsed.ID || len(shown.Messages) != 0 {
		t.Fatalf("show = %+v", shown)
	}
}

func TestPostSessionActivate_PrimaryOnly(t *testing.T) {
	srv := newTestServer(t)
	firstID := "20260507T101010-first01"
	secondID := "20260507T111010-second1"
	sideID := "20260507T121010-side001"
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, firstID, body)
	seedSession(t, srv.opts.Cfg.WorkDir, secondID, body)
	seedSession(t, srv.opts.Cfg.WorkDir, sideID, body)
	sideDir := filepath.Join(srv.opts.Cfg.SessionsDir(), sideID)
	if err := session.SetKind(sideDir, session.KindSide); err != nil {
		t.Fatal(err)
	}
	first, _, err := session.LoadInfo(filepath.Join(srv.opts.Cfg.SessionsDir(), firstID))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), first); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sessions/"+secondID+"/activate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var activated struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
		Kind   string `json:"kind"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&activated); err != nil {
		t.Fatal(err)
	}
	if activated.ID != secondID || activated.Kind != session.KindPrimary || !activated.Active {
		t.Fatalf("activated = %+v", activated)
	}

	sideResp, err := http.Post(ts.URL+"/api/sessions/"+sideID+"/activate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sideResp.Body.Close()
	if sideResp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(sideResp.Body)
		t.Fatalf("side status = %d body = %s", sideResp.StatusCode, body)
	}
}

func TestPostTurn_StartsTurnAndPersists(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Create a session first.
	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	if _, err := os.Stat(filepath.Join(srv.opts.Cfg.SessionsDir(), c.ID, "conversation.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("conversation stat before turn err = %v, want not exist", err)
	}

	// Submit a turn.
	body := strings.NewReader(`{"prompt":"hi"}`)
	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var got struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.TurnID == "" {
		t.Errorf("missing turn_id")
	}

	// Wait briefly for the goroutine to finish (stub provider returns immediately).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		show, err := http.Get(ts.URL + "/api/sessions/" + c.ID)
		if err == nil {
			var parsed struct {
				TokenUsage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"token_usage"`
				ContextUsage struct {
					Model         string `json:"model"`
					ContextWindow int    `json:"context_window"`
					InputTokens   int    `json:"input_tokens"`
					OutputTokens  int    `json:"output_tokens"`
					TotalTokens   int    `json:"total_tokens"`
					Breakdown     []struct {
						Key    string `json:"key"`
						Tokens int    `json:"tokens"`
					} `json:"breakdown"`
				} `json:"context_usage"`
				Messages []struct {
					Role         string    `json:"role"`
					Usage        *struct{} `json:"usage,omitempty"`
					ContextUsage *struct{} `json:"context_usage,omitempty"`
					Blocks       []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"blocks"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(show.Body).Decode(&parsed); err != nil {
				show.Body.Close()
				t.Fatal(err)
			}
			show.Body.Close()
			for _, m := range parsed.Messages {
				if m.Role == "assistant" {
					for _, b := range m.Blocks {
						if b.Type == "text" && b.Text == "ack" {
							if parsed.TokenUsage.InputTokens != 4 || parsed.TokenUsage.OutputTokens != 2 {
								t.Fatalf("token_usage = %+v", parsed.TokenUsage)
							}
							if m.Usage != nil {
								t.Fatalf("message usage should be omitted: %+v", m.Usage)
							}
							if m.ContextUsage != nil {
								t.Fatalf("message context_usage should be omitted: %+v", m.ContextUsage)
							}
							if parsed.ContextUsage.Model != "stub" ||
								parsed.ContextUsage.ContextWindow != 256000 ||
								parsed.ContextUsage.InputTokens != 4 ||
								parsed.ContextUsage.OutputTokens != 2 ||
								parsed.ContextUsage.TotalTokens != 6 {
								t.Fatalf("context_usage = %+v", parsed.ContextUsage)
							}
							if len(parsed.ContextUsage.Breakdown) == 0 {
								t.Fatal("context_usage missing breakdown")
							}
							var hasResponse bool
							for _, part := range parsed.ContextUsage.Breakdown {
								if part.Key == "response" && part.Tokens == 2 {
									hasResponse = true
									break
								}
							}
							if !hasResponse {
								t.Fatalf("context_usage missing response breakdown: %+v", parsed.ContextUsage.Breakdown)
							}
							if _, err := os.Stat(filepath.Join(srv.opts.Cfg.SessionsDir(), c.ID, "conversation.jsonl")); err != nil {
								t.Fatalf("conversation stat after turn err = %v", err)
							}
							return
						}
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for ack to be persisted")
}

func TestPostTurn_RequiresActivePrimary(t *testing.T) {
	srv := newTestServer(t)
	activeID := "20260507T101010-active1"
	inactiveID := "20260507T111010-inactive"
	sideID := "20260507T121010-side001"
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, activeID, body)
	seedSession(t, srv.opts.Cfg.WorkDir, inactiveID, body)
	seedSession(t, srv.opts.Cfg.WorkDir, sideID, body)
	sideDir := filepath.Join(srv.opts.Cfg.SessionsDir(), sideID)
	if err := session.SetKind(sideDir, session.KindSide); err != nil {
		t.Fatal(err)
	}
	activeInfo, _, err := session.LoadInfo(filepath.Join(srv.opts.Cfg.SessionsDir(), activeID))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), activeInfo); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, id := range []string{inactiveID, sideID} {
		resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/turns", "application/json",
			strings.NewReader(`{"prompt":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s status = %d body = %s", id, resp.StatusCode, body)
		}
	}
}

func TestGetTurnStatus_DoneAfterCompletion(t *testing.T) {
	srv := newTestServer(t)
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

	turnResp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	var t1 struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(turnResp.Body).Decode(&t1); err != nil {
		t.Fatal(err)
	}
	turnResp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, err := http.Get(ts.URL + "/api/sessions/" + c.ID + "/turns/" + t1.TurnID)
		if err != nil {
			t.Fatal(err)
		}
		var st struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			r.Body.Close()
			t.Fatal(err)
		}
		r.Body.Close()
		if st.State == "done" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("turn never reached done state")
}

func TestPostInterrupt_IdempotentWhenIdle(t *testing.T) {
	srv := newTestServer(t)
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

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var got struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Cancelled {
		t.Errorf("expected cancelled=false when nothing running")
	}
}

func TestDeleteSession_RemovesSessionAndListEntry(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-delete1"
	seedSession(t, srv.opts.Cfg.WorkDir, id,
		`{"role":"user","blocks":[{"type":"text","text":"delete me"}]}`+"\n")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+id, nil)
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
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var got struct {
		Deleted bool   `json:"deleted"`
		ID      string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Deleted || got.ID != id {
		t.Fatalf("response = %+v", got)
	}

	resp2, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if strings.Contains(string(body), id) {
		t.Fatalf("deleted session still listed:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(srv.opts.Cfg.SessionsDir(), id)); !os.IsNotExist(err) {
		t.Fatalf("deleted dir stat err = %v, want not exist", err)
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/missing", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDeleteSession_RemovesEmptyActiveSession(t *testing.T) {
	srv := newTestServer(t)
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

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+c.ID, nil)
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
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}

	resp2, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if strings.Contains(string(body), c.ID) {
		t.Fatalf("empty active session still listed:\n%s", body)
	}
}

func TestSSEEvents_ReceivesPublished(t *testing.T) {
	srv := newTestServer(t)
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

	// Connect to the SSE stream first.
	req, _ := http.NewRequest("GET", ts.URL+"/api/sessions/"+c.ID+"/events", nil)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type = %q", resp.Header.Get("Content-Type"))
	}

	// Submit a turn — at minimum, a turn.started/turn.completed pair fires.
	go func() {
		resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
			strings.NewReader(`{"prompt":"hi"}`))
		if err == nil {
			resp.Body.Close()
		}
	}()

	// Read until we see one full SSE frame containing turn.started.
	buf := make([]byte, 4096)
	deadline := time.Now().Add(2 * time.Second)
	collected := ""
	for time.Now().Before(deadline) {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			collected += string(buf[:n])
			if strings.Contains(collected, "turn.started") {
				return
			}
		}
		if err != nil {
			break
		}
	}
	t.Fatalf("did not receive turn.started; collected:\n%s", collected)
}

func TestSPAFallback_ServesIndexForUnknownRoute(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, path := range []string{"/", "/sessions/some-arbitrary-id", "/runtime", "/anything/at/all"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: status = %d, body=%s", path, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "<!doctype html") &&
			!strings.Contains(string(body), "<!DOCTYPE html") {
			t.Errorf("GET %s: body does not look like HTML:\n%s", path, body)
		}
	}
}
