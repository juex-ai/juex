package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

func TestOpenAIResponsesStreamRequiresTerminal(t *testing.T) {
	for _, protocol := range []llm.Protocol{llm.ProtocolOpenAIResponses, llm.ProtocolOpenAICodexResponses} {
		for name, item := range map[string]string{
			"text": `{"type":"message","id":"msg","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer","annotations":[]}]}`,
			"tool": `{"type":"function_call","id":"fc","call_id":"call","name":"read","arguments":"{\"path\":\"file\"}","status":"completed"}`,
		} {
			for _, terminal := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/terminal=%t", protocol, name, terminal), func(t *testing.T) {
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						calls.Add(1)
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
						if terminal {
							fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"status\":\"completed\",\"output\":[]}}\n\n")
						}
					}))
					defer server.Close()
					providerID := "openai"
					if protocol == llm.ProtocolOpenAICodexResponses {
						providerID = "openai-codex"
					}
					profile := openaiTestProfile(t, providerprofile.Config{ID: providerID, Protocol: string(protocol), BaseURL: server.URL, APIKey: "test", Model: "m"})
					provider := NewOpenAIResponses(profile, nil)
					if protocol == llm.ProtocolOpenAICodexResponses {
						provider = NewOpenAICodexResponses(profile, nil)
					}
					response, err := llm.CompleteWithOptions(context.Background(), provider, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil, llm.CompleteOptions{SingleAttempt: true})
					if calls.Load() != 1 {
						t.Fatalf("durable attempt replayed: calls=%d err=%v", calls.Load(), err)
					}
					if terminal {
						if err != nil || len(response.Message.Blocks) != 1 {
							t.Fatalf("terminal item backfill lost: response=%+v err=%v", response, err)
						}
					} else if !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatalf("item completion accepted as response completion: response=%+v err=%v", response, err)
					}
				})
			}
		}
	}
}

func TestOpenAIChatStreamRequiresFinishReason(t *testing.T) {
	for name, delta := range map[string]string{
		"reasoning": `{"role":"assistant","reasoning_content":"unfinished thought"}`,
		"text":      `{"role":"assistant","content":"unfinished answer"}`,
		"tool":      `{"role":"assistant","tool_calls":[{"index":0,"id":"partial","type":"function","function":{"name":"read","arguments":"{\"path\":\"file\"}"}}]}`,
	} {
		for _, done := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/done=%t", name, done), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"id\":\"partial\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":null}]}\n\n", delta)
					if done {
						fmt.Fprint(w, "data: [DONE]\n\n")
					}
				}))
				defer server.Close()
				provider := NewOpenAI(openaiTestProfile(t, providerprofile.Config{Protocol: string(llm.ProtocolOpenAIChat), BaseURL: server.URL, APIKey: "test", Model: "m"}), nil)
				response, err := provider.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil)
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("unterminated choice accepted: response=%+v err=%v", response, err)
				}
				if len(response.Message.Blocks) != 1 || response.UsageStatus != llm.UsageUnknown {
					t.Fatalf("partial diagnostics lost: %+v", response)
				}
			})
		}
	}
}

func TestOpenAIChatStreamTerminalWithoutUsage(t *testing.T) {
	for finish, want := range map[string]llm.StopReason{"stop": llm.StopEndTurn, "length": llm.StopMaxTokens, "content_filter": llm.StopOther} {
		t.Run(finish, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"id\":\"complete\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":%q}]}\n\n", finish)
			}))
			defer server.Close()
			provider := NewOpenAI(openaiTestProfile(t, providerprofile.Config{Protocol: string(llm.ProtocolOpenAIChat), BaseURL: server.URL, APIKey: "test", Model: "m"}), nil)
			response, err := provider.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil)
			if err != nil || response.StopReason != want || response.Message.FirstText() != "answer" || response.UsageStatus != llm.UsageUnknown {
				t.Fatalf("terminal choice depends on optional usage: response=%+v err=%v", response, err)
			}
		})
	}
}
