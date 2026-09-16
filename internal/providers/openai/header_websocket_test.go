package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/juex-ai/juex/internal/foundation/llm"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

func TestCodexWebsocketHeaderIdentityReconnect(t *testing.T) {
	type capture struct {
		identity, previous string
		connection         int32
	}
	captures := make(chan capture, 8)
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection := connections.Add(1)
		if r.Header.Get("Authorization") != "Bearer api-key" || r.Header.Get("originator") != "juex" || r.Header.Get("OpenAI-Beta") != codexResponsesWebsocketBeta {
			t.Errorf("native headers changed: %v", r.Header)
		}
		if r.Header.Get("ChatGPT-Account-ID") != "account-agent" {
			t.Error("account header not expanded")
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var frame map[string]any
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Error(err)
				return
			}
			previous, _ := frame["previous_response_id"].(string)
			captures <- capture{r.Header.Get("X-Identity"), previous, connection}
			if err := conn.Write(r.Context(), websocket.MessageText, openaiCodexCompletedWebsocketEvent(fmt.Sprintf("response-%d", connection), "ok")); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	p, err := newTestProvider(providerprofile.Config{ID: "openai-codex", Model: "m", APIKey: "api-key", BaseURL: server.URL, Compat: llm.CompatOptions{CodexTransport: providerprofile.CodexTransportWebSocketCached}, Headers: map[string]string{
		"X-Identity": "${juex_agent_id}/${juex_generation_id}", "ChatGPT-Account-ID": "account-${juex_agent_id}", "Authorization": "ignored", "originator": "ignored", "OpenAI-Beta": "ignored",
	}})
	if err != nil {
		t.Fatal(err)
	}
	concrete := p.(*openAICodexResponsesProvider)
	defer func() { concrete.ws.mu.Lock(); concrete.ws.closeLocked(); concrete.ws.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	history := []llm.Message{llm.TextMessage(llm.RoleUser, "first")}
	for i, generation := range []string{"g1", "g1", "g2"} {
		identity := llm.RequestIdentity{AgentID: "agent", GenerationID: generation}
		resp, err := llm.CompleteWithOptions(ctx, p, "", history, nil, llm.CompleteOptions{Identity: identity})
		if err != nil {
			t.Fatal(err)
		}
		got := <-captures
		if got.identity != "agent/"+generation {
			t.Fatalf("identity = %+v", got)
		}
		if i == 1 {
			if got.connection != 1 || got.previous != "response-1" {
				t.Fatalf("same identity lost reuse: %+v", got)
			}
		} else if got.previous != "" {
			t.Fatalf("new identity reused continuation: %+v", got)
		}
		if i == 2 && got.connection != 2 {
			t.Fatalf("changed identity did not reconnect: %+v", got)
		}
		history = append(history, resp.Message, llm.TextMessage(llm.RoleUser, "next"))
	}
	if _, err := p.Complete(ctx, "", history, nil); err == nil {
		t.Fatal("missing identity accepted")
	}
	if connections.Load() != 2 {
		t.Fatal("missing identity opened a connection")
	}
}
