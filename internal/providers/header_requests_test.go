package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

func TestProviderRequestHeaders(t *testing.T) {
	for _, protocol := range []llm.Protocol{llm.ProtocolOpenAIChat, llm.ProtocolOpenAIResponses, llm.ProtocolAnthropicMessages, llm.ProtocolOpenAICodexResponses} {
		for _, streaming := range []bool{false, true} {
			if protocol == llm.ProtocolOpenAICodexResponses && !streaming {
				continue
			}
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, streaming), func(t *testing.T) {
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempt := calls.Add(1)
					identity := r.Header.Get("X-Identity")
					if identity != "a-0-g1-s1" && identity != "b-worker-g2-s2" {
						t.Errorf("identity = %q", identity)
					}
					if r.Header.Get("X-Literal") != "${juex_agent_id}/${HOME}" {
						t.Errorf("literal = %q", r.Header.Get("X-Literal"))
					}
					if r.Header.Get("X-Static") != "static" {
						t.Error("lost static header")
					}
					if attempt == 1 {
						w.Header().Set("retry-after-ms", "0")
						w.WriteHeader(500)
						fmt.Fprint(w, `{"error":{"type":"api_error","message":"temporary server error"}}`)
						return
					}
					writeHeaderTestResponse(w, protocol, streaming)
				}))
				defer srv.Close()
				id := "custom"
				if protocol == llm.ProtocolOpenAICodexResponses {
					id = "openai-codex"
				}
				profile, err := providerprofile.ResolveProfile(providerprofile.Config{ID: id, Protocol: string(protocol), Model: "model", APIKey: "test", BaseURL: srv.URL,
					Headers:      map[string]string{"X-Identity": "${juex_agent_id}-${juex_thread_id}-${juex_generation_id}-${juex_context_scope_id}", "X-Literal": "$${juex_agent_id}/${HOME}", "X-Static": "static"},
					Capabilities: llm.CapabilityOverrides{Streaming: &streaming},
				})
				if err != nil {
					t.Fatal(err)
				}
				p, err := NewProvider(profile)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.Complete(context.Background(), "", nil, nil); err == nil || !strings.Contains(err.Error(), "juex_agent_id") {
					t.Fatalf("missing identity: %v", err)
				}
				if calls.Load() != 0 {
					t.Fatal("missing identity reached network")
				}
				identities := []llm.RequestIdentity{{AgentID: "a", ThreadID: "0", GenerationID: "g1", ContextScopeID: "s1"}, {AgentID: "b", ThreadID: "worker", GenerationID: "g2", ContextScopeID: "s2"}}
				var wg sync.WaitGroup
				for _, identity := range identities {
					wg.Add(1)
					go func() {
						defer wg.Done()
						resp, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{Identity: identity})
						if err != nil || resp.Message.FirstText() != "ok" {
							t.Errorf("response = %v, err = %v", resp, err)
						}
					}()
				}
				wg.Wait()
				if calls.Load() != 3 {
					t.Fatalf("calls = %d, want 2 requests plus retry", calls.Load())
				}
			})
		}
	}
}

func writeHeaderTestResponse(w http.ResponseWriter, protocol llm.Protocol, streaming bool) {
	w.Header().Set("Content-Type", "application/json")
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	switch protocol {
	case llm.ProtocolOpenAIChat:
		if streaming {
			fmt.Fprint(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		} else {
			fmt.Fprint(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}
	case llm.ProtocolAnthropicMessages:
		if streaming {
			fmt.Fprint(w, providersAnthropicTextStreamData("model", "ok", "end_turn", 1, 1))
		} else {
			fmt.Fprint(w, `{"id":"m","type":"message","role":"assistant","model":"model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		}
	default:
		response := `{"id":"r","object":"response","model":"model","status":"completed","output":[{"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`
		if streaming {
			fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
		} else {
			fmt.Fprint(w, response)
		}
	}
}
