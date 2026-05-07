package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
		Cfg:      config.Config{ProviderType: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Create session.
	created, _ := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	var c struct{ ID string }
	json.NewDecoder(created.Body).Decode(&c)
	created.Body.Close()
	if c.ID == "" {
		t.Fatal("no session id")
	}

	// 2. Submit a turn.
	resp, _ := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
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
		json.NewDecoder(show.Body).Decode(&parsed)
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
