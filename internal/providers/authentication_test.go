package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

func TestExplicitUnauthenticatedProviderSendsNoCredential(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "unrelated-host-secret")
	for _, fixture := range []struct {
		protocol llm.Protocol
		body     string
	}{
		{llm.ProtocolOpenAIChat, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`},
		{llm.ProtocolOpenAIResponses, `{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`},
	} {
		t.Run(string(fixture.protocol), func(t *testing.T) {
			observed := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed <- r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(fixture.body))
			}))
			defer server.Close()
			p, err := NewProvider(llm.ProviderProfile{ID: "local", Protocol: fixture.protocol, Model: "test", BaseURL: server.URL, Authentication: "none"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil, llm.CompleteOptions{SingleAttempt: true})
			if err != nil || response.Message.FirstText() != "ok" {
				t.Fatal("unauthenticated request failed", err)
			}
			if <-observed != "" {
				t.Fatal("none authentication sent a host or empty credential")
			}
		})
	}
}

func TestProviderAuthenticationMustBeExplicitAndConsistent(t *testing.T) {
	for _, profile := range []llm.ProviderProfile{
		{ID: "local", Protocol: llm.ProtocolOpenAIChat, Model: "test"},
		{ID: "local", Protocol: llm.ProtocolOpenAIChat, Model: "test", Authentication: "typo", APIKey: "secret"},
		{ID: "local", Protocol: llm.ProtocolOpenAIChat, Model: "test", Authentication: "none", APIKey: "secret"},
		{ID: "local", Protocol: llm.ProtocolOpenAIChat, Model: "test", Authentication: "none", Headers: map[string]string{"authorization": "secret"}},
		{ID: "openai-codex", Protocol: llm.ProtocolOpenAICodexResponses, Model: "test", Authentication: "none"},
	} {
		if _, err := NewProvider(profile); err == nil {
			t.Fatal("ambiguous or unsupported authentication accepted")
		}
	}
	if _, err := providerprofile.ResolveProfile(providerprofile.Config{ID: "local", Protocol: string(llm.ProtocolOpenAIChat), Authentication: "none", Headers: map[string]string{"Authorization": "secret"}}); err == nil {
		t.Fatal("profile resolver accepted contradictory authentication")
	}
}
