package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Provider interface {
	Name() string
	Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error)
}

type CompleteOptions struct {
	Purpose         string
	MaxOutputTokens int
	CachePolicy     CachePolicy
}

type CachePolicy struct {
	StablePrefixKey string
	Retention       string
}

type ProviderWithOptions interface {
	CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error)
}

const providerMaxRetries = 10

type Config struct {
	ID             string
	Protocol       string
	BaseURL        string
	APIKey         string
	Model          string
	ThinkingEffort string // "low", "medium", "high", "xhigh", "max", or "" (provider default)
	Headers        map[string]string
	Query          map[string]string
	Capabilities   CapabilityOverrides
	Compat         CompatOptions
}

// New constructs the appropriate Provider for the resolved provider profile.
// Public custom protocol families are "anthropic/messages", "openai/chat",
// and "openai/responses"; "openai-codex/responses" is reserved for the
// openai-codex preset.
func New(cfg Config) (Provider, error) {
	profile, err := ResolveProfile(cfg)
	if err != nil {
		return nil, err
	}
	if profile.APIKey == "" {
		return nil, fmt.Errorf("llm: missing API key")
	}
	if profile.Model == "" {
		return nil, fmt.Errorf("llm: missing model")
	}
	resolved := profile.Config()
	switch profile.Protocol {
	case ProtocolAnthropicMessages:
		return NewAnthropic(resolved, &http.Client{Timeout: 120 * time.Second}), nil
	case ProtocolOpenAIChat:
		return NewOpenAI(resolved, &http.Client{Timeout: 120 * time.Second}), nil
	case ProtocolOpenAIResponses:
		return NewOpenAIResponses(resolved, &http.Client{Timeout: 120 * time.Second}), nil
	case ProtocolOpenAICodexResponses:
		return NewOpenAICodexResponses(resolved, &http.Client{Timeout: 120 * time.Second}), nil
	default:
		return nil, fmt.Errorf("llm: unsupported provider protocol %q", profile.Protocol)
	}
}

func CompleteWithOptions(ctx context.Context, p Provider, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error) {
	if withOpts, ok := p.(ProviderWithOptions); ok {
		return withOpts.CompleteWithOptions(ctx, sys, history, tools, opts)
	}
	return p.Complete(ctx, sys, history, tools)
}

func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"context_length_exceeded",
		"context window",
		"maximum context length",
		"prompt is too long",
		"input length",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
