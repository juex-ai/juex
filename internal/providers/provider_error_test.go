package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/juex-ai/juex/internal/foundation/llm"
	openaisdk "github.com/openai/openai-go"
)

func TestProviderPublishesNeutralHTTPStatusAndPreservesSDKCause(t *testing.T) {
	for _, protocol := range []llm.Protocol{llm.ProtocolAnthropicMessages, llm.ProtocolOpenAIChat, llm.ProtocolOpenAIResponses} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/streaming=%v", protocol, streaming), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"type":"error","error":{"type":"permission_error","message":"access denied"}}`)
				}))
				t.Cleanup(server.Close)
				provider, err := NewProvider(llm.ProviderProfile{ID: "test", Protocol: protocol, BaseURL: server.URL, APIKey: "test-key", Model: "test-model", Capabilities: llm.ProviderCapabilities{Streaming: streaming}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = provider.Complete(context.Background(), "system", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil)
				reason, eligible := llm.ClassifyFallbackError(err)
				if reason != llm.FallbackFailureForbidden || !eligible {
					t.Fatalf("fallback = %q, %v; error %v", reason, eligible, err)
				}
				var status interface{ HTTPStatusCode() int }
				if !errors.As(err, &status) || status.HTTPStatusCode() != http.StatusForbidden {
					t.Fatalf("neutral HTTP status missing: %v", err)
				}
				if protocol == llm.ProtocolAnthropicMessages {
					var cause *anthropicsdk.Error
					if !errors.As(err, &cause) {
						t.Fatalf("Anthropic cause lost: %v", err)
					}
				} else {
					var cause *openaisdk.Error
					if !errors.As(err, &cause) {
						t.Fatalf("OpenAI cause lost: %v", err)
					}
				}
			})
		}
	}
}
