package llm

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type Provider interface {
	Name() string
	Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error)
}

const providerMaxRetries = 10

type Config struct {
	Type           string
	BaseURL        string
	APIKey         string
	Model          string
	ThinkingEffort string // "low", "medium", "high", or "" (provider default)
}

// New constructs the appropriate Provider for cfg.Type.
// Supported types: "anthropic", "openai".
func New(cfg Config) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("llm: missing API key")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm: missing model")
	}
	switch cfg.Type {
	case "anthropic":
		return NewAnthropic(cfg, &http.Client{Timeout: 120 * time.Second}), nil
	case "openai":
		return NewOpenAI(cfg, &http.Client{Timeout: 120 * time.Second}), nil
	default:
		return nil, fmt.Errorf("llm: unknown provider type %q", cfg.Type)
	}
}
