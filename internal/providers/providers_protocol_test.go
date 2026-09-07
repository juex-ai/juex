package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	anthropicadapter "github.com/juex-ai/juex/internal/providers/anthropic"
	protocolsupport "github.com/juex-ai/juex/internal/providers/internal/protocol"
	openaiadapter "github.com/juex-ai/juex/internal/providers/openai"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

func TestProviders_RetryPolicy(t *testing.T) {
	cases := []struct {
		name               string
		provider           func(baseURL string) llm.Provider
		serverErr          string
		badReqErr          string
		successContentType string
		successRes         string
	}{
		{
			name: "anthropic",
			provider: func(baseURL string) llm.Provider {
				return anthropicadapter.NewAnthropic(providersTestProfile(t, providerprofile.Config{ID: "anthropic", BaseURL: baseURL, APIKey: "test-key", Model: "claude-test"}), nil)
			},
			serverErr:          `{"type":"error","error":{"type":"api_error","message":"temporary server error"}}`,
			badReqErr:          `{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`,
			successContentType: "text/event-stream",
			successRes:         providersAnthropicTextStreamData("claude-test", "ok", "end_turn", 1, 1),
		},
		{
			name: "openai",
			provider: func(baseURL string) llm.Provider {
				return openaiadapter.NewOpenAI(providersTestBlockingProfile(t, providerprofile.Config{Protocol: string(llm.ProtocolOpenAIChat), BaseURL: baseURL, APIKey: "k", Model: "m"}), nil)
			},
			serverErr:          `{"error":{"message":"temporary server error","type":"server_error"}}`,
			badReqErr:          `{"error":{"message":"bad request","type":"invalid_request_error"}}`,
			successContentType: "application/json",
			successRes: `{
				"id":"cmpl_1","object":"chat.completion","model":"gpt-test",
				"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`,
		},
		{
			name: "openai-codex",
			provider: func(baseURL string) llm.Provider {
				return openaiadapter.NewOpenAICodexResponses(providersTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: baseURL, APIKey: "k", Model: "m"}), nil)
			},
			serverErr:          `{"error":{"message":"temporary server error","type":"server_error"}}`,
			badReqErr:          `{"error":{"message":"bad request","type":"invalid_request_error"}}`,
			successContentType: "text/event-stream",
			successRes:         `data: {"type":"response.completed","response":{"id":"resp_1","model":"m","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}}` + "\n\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/recoverable", func(t *testing.T) {
			attempts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.Header().Set("retry-after-ms", "0")
				if attempts <= protocolsupport.ProviderMaxRetries {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(tc.serverErr))
					return
				}
				w.Header().Set("Content-Type", tc.successContentType)
				w.Write([]byte(tc.successRes))
			}))
			defer srv.Close()

			resp, err := tc.provider(srv.URL).Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if attempts != protocolsupport.ProviderMaxRetries+1 {
				t.Fatalf("attempts = %d, want %d", attempts, protocolsupport.ProviderMaxRetries+1)
			}
			if resp.Message.FirstText() != "ok" {
				t.Fatalf("text = %q, want ok", resp.Message.FirstText())
			}
		})

		t.Run(tc.name+"/bad_request", func(t *testing.T) {
			attempts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(tc.badReqErr))
			}))
			defer srv.Close()

			if _, err := tc.provider(srv.URL).Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil); err == nil {
				t.Fatal("expected error")
			}
			if attempts != 1 {
				t.Fatalf("attempts = %d, want 1", attempts)
			}
		})
	}
}

func TestNewProvider_Errors(t *testing.T) {
	if _, err := New(providerprofile.Config{ID: "anthropic", APIKey: "", Model: "m"}); err == nil {
		t.Error("missing key should error")
	}
	if _, err := New(providerprofile.Config{ID: "anthropic", APIKey: "k"}); err == nil {
		t.Error("missing model should error")
	}
	if _, err := New(providerprofile.Config{ID: "bogus", APIKey: "k", Model: "m"}); err == nil {
		t.Error("unknown provider selector should error")
	}
	if _, err := NewProvider(llm.ProviderProfile{
		ID:       "openai-codex",
		Protocol: llm.ProtocolOpenAICodexResponses,
		APIKey:   "k",
		Model:    "m",
		Compat:   llm.CompatOptions{CodexTransport: "bogus"},
	}); err == nil {
		t.Error("unsupported codex transport should error")
	}
}

func TestNewProvider_FromResolvedProfile(t *testing.T) {
	profile, err := providerprofile.ResolveProfile(providerprofile.Config{
		ID:       "openai",
		Protocol: string(llm.ProtocolOpenAIResponses),
		APIKey:   "sk-test",
		Model:    "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewProvider(profile)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "openai:gpt-test" {
		t.Fatalf("provider name = %q", provider.Name())
	}
}

func TestNewProvider_DoesNotReResolveProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("X-Provider") != "canonical" || r.URL.Query().Get("debug") != "1" {
			t.Errorf("resolved request changed: %s headers=%v", r.URL, r.Header)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if !strings.Contains(fmt.Sprint(request["include"]), "reasoning.encrypted_content") {
			t.Errorf("resolved reasoning replay disabled: %v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","object":"response","status":"completed","model":"responses-test","output":[]}`)
	}))
	t.Cleanup(srv.Close)
	profile := llm.ProviderProfile{
		ID:       "deepseek",
		Protocol: llm.ProtocolOpenAIResponses,
		BaseURL:  srv.URL,
		APIKey:   "sk-test",
		Model:    "responses-test",
		Headers:  map[string]string{"X-Provider": "canonical"},
		Query:    map[string]string{"debug": "1"},
		Capabilities: llm.ProviderCapabilities{
			Tools:           true,
			ReasoningReplay: true,
		},
		Compat: llm.CompatOptions{ReasoningReplayFields: []string{"canonical_reasoning"}},
	}
	provider, err := NewProvider(profile)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "deepseek:responses-test" {
		t.Fatalf("provider name = %q", provider.Name())
	}
	_, err = provider.Complete(context.Background(), "system", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil)
	if err != nil {
		t.Fatal(err)
	}

}

func providersAnthropicTextStreamData(model, text, stopReason string, inputTokens, outputTokens int) string {
	var sb strings.Builder
	rr := httptest.NewRecorder()
	providersWriteAnthropicTextStream(rr, model, text, stopReason, inputTokens, outputTokens)
	sb.WriteString(rr.Body.String())
	return sb.String()
}

func providersBlockingConfig(cfg providerprofile.Config) providerprofile.Config {
	cfg.Capabilities.Streaming = providersBoolPtr(false)
	return cfg
}

func providersTestBlockingProfile(t testing.TB, cfg providerprofile.Config) llm.ProviderProfile {
	t.Helper()
	return providersTestProfile(t, providersBlockingConfig(cfg))
}

func providersTestProfile(t testing.TB, cfg providerprofile.Config) llm.ProviderProfile {
	t.Helper()
	profile, err := providerprofile.ResolveProfile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func providersWriteAnthropicTextStream(w http.ResponseWriter, model, text, stopReason string, inputTokens, outputTokens int) {
	providersWriteAnthropicTextStreamWithCache(w, model, text, stopReason, inputTokens, outputTokens, 0)
}

func providersWriteAnthropicTextStreamWithCache(w http.ResponseWriter, model, text, stopReason string, inputTokens, outputTokens, cacheReadTokens int) {
	if stopReason == "" {
		stopReason = "end_turn"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: message_start\n")
	fmt.Fprintf(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":%d,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":%d}}}`+"\n\n", model, inputTokens, cacheReadTokens)
	fmt.Fprint(w, "event: content_block_start\n")
	fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprintf(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`+"\n\n", text)
	fmt.Fprint(w, "event: content_block_stop\n")
	fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
	fmt.Fprint(w, "event: message_delta\n")
	fmt.Fprintf(w, `data: {"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":%d}}`+"\n\n", stopReason, outputTokens)
	fmt.Fprint(w, "event: message_stop\n")
	fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
}

func providersBoolPtr(v bool) *bool {
	return &v
}
