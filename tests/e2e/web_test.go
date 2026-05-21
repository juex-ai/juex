package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
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
	resp.Body.Close()

	// 3. Wait until turn shows in transcript.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		show, _ := http.Get(ts.URL + "/api/sessions/" + c.ID)
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
		for _, m := range parsed.Messages {
			if m.Role == "assistant" {
				for _, b := range m.Blocks {
					if b.Type == "text" && b.Text == "noted" {
						return
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("turn never appeared in transcript")
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
