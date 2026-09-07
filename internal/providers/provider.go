package providers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	anthropicadapter "github.com/juex-ai/juex/internal/providers/anthropic"
	openaiadapter "github.com/juex-ai/juex/internal/providers/openai"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

// New constructs the appropriate Provider for the resolved provider profile.
// Public custom protocol families are "anthropic/messages", "openai/chat",
// and "openai/responses"; "openai-codex/responses" is reserved for the
// openai-codex preset.
func New(cfg providerprofile.Config) (llm.Provider, error) {
	profile, err := providerprofile.ResolveProfile(cfg)
	if err != nil {
		return nil, err
	}
	return NewProvider(profile)
}

// NewProvider constructs the concrete provider for a resolved profile.
func NewProvider(profile llm.ProviderProfile) (llm.Provider, error) {
	if profile.APIKey == "" {
		return nil, fmt.Errorf("llm: missing API key")
	}
	if profile.Model == "" {
		return nil, fmt.Errorf("llm: missing model")
	}
	profile = providerprofile.CloneProviderProfile(profile)
	switch profile.Protocol {
	case llm.ProtocolAnthropicMessages:
		return anthropicadapter.NewAnthropic(profile, &http.Client{Timeout: 120 * time.Second}), nil
	case llm.ProtocolOpenAIChat:
		return openaiadapter.NewOpenAI(profile, &http.Client{Timeout: 120 * time.Second}), nil
	case llm.ProtocolOpenAIResponses:
		return openaiadapter.NewOpenAIResponses(profile, &http.Client{Timeout: 120 * time.Second}), nil
	case llm.ProtocolOpenAICodexResponses:
		transport, err := providerprofile.NormalizeCodexTransport(profile.Compat.CodexTransport)
		if err != nil {
			return nil, err
		}
		if transport == "" {
			transport = providerprofile.CodexTransportSSE
		}
		profile.Compat.CodexTransport = transport
		return openaiadapter.NewOpenAICodexResponses(profile, nil), nil
	default:
		return nil, fmt.Errorf("llm: unsupported provider protocol %q", profile.Protocol)
	}
}
