package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestManagedProviderSingleWireAttempt(t *testing.T) {
	for _, protocol := range []llm.Protocol{llm.ProtocolOpenAIChat, llm.ProtocolOpenAIResponses, llm.ProtocolAnthropicMessages} {
		t.Run(string(protocol), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprint(w, `{"error":{"message":"temporarily unavailable","type":"overloaded_error"}}`)
			}))
			defer server.Close()
			p, err := NewProvider(llm.ProviderProfile{ID: "fixture", Protocol: protocol, BaseURL: server.URL, APIKey: "test-key", Model: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			response, err := llm.CompleteWithOptions(ctx, p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil, llm.CompleteOptions{SingleAttempt: true})
			if err == nil || requests.Load() != 1 || response.UsageStatus == llm.UsageComplete {
				t.Fatalf("requests=%d response=%+v err=%v", requests.Load(), response, err)
			}
		})
	}
}

func TestManagedProviderReportedZeroAndMissingUsage(t *testing.T) {
	for _, fixture := range []struct {
		protocol    llm.Protocol
		body, usage string
	}{
		{llm.ProtocolOpenAIChat, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]%s}`, `,"usage":{"prompt_tokens":0,"completion_tokens":0}`},
		{llm.ProtocolOpenAIResponses, `{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]%s}`, `,"usage":{"input_tokens":0,"output_tokens":0}`},
		{llm.ProtocolAnthropicMessages, `{"id":"m","type":"message","role":"assistant","model":"fixture","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]%s}`, `,"usage":{"input_tokens":0,"output_tokens":0}`},
	} {
		for _, reported := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reported=%t", fixture.protocol, reported), func(t *testing.T) {
				usage := ""
				if reported {
					usage = fixture.usage
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, fixture.body, usage)
				}))
				defer server.Close()
				p, err := NewProvider(llm.ProviderProfile{ID: "fixture", Protocol: fixture.protocol, BaseURL: server.URL, APIKey: "test-key", Model: "fixture"})
				if err != nil {
					t.Fatal(err)
				}
				response, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil, llm.CompleteOptions{SingleAttempt: true})
				want := llm.UsageUnknown
				if reported {
					want = llm.UsageComplete
				}
				if err != nil || response.UsageStatus != want || response.Message.FirstText() != "ok" {
					t.Fatalf("response=%+v err=%v", response, err)
				}
			})
		}
	}
}

func TestManagedAnthropicInterruptedStreamRetainsPartialUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"fixture\",\"content\":[],\"usage\":{\"input_tokens\":12,\"output_tokens\":0}}}\n\n")
	}))
	defer server.Close()
	p, err := NewProvider(llm.ProviderProfile{ID: "fixture", Protocol: llm.ProtocolAnthropicMessages, BaseURL: server.URL, APIKey: "test-key", Model: "fixture", Capabilities: llm.ProviderCapabilities{Streaming: true}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil, llm.CompleteOptions{SingleAttempt: true})
	if err == nil || response.UsageStatus != llm.UsagePartial || response.Usage.InputTokens != 12 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
