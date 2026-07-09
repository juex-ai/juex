package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Provider interface {
	Name() string
	Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error)
}

type CompleteOptions struct {
	Purpose           string
	MaxOutputTokens   int
	CachePolicy       CachePolicy
	RetryObserver     func(ProviderRetryDiagnostic)
	OnDelta           func(StreamDelta)
	StreamIdleTimeout time.Duration
}

type StreamDelta struct {
	Kind  string
	Index int
	Text  string
}

type CachePolicy struct {
	StablePrefixKey string
	Retention       string
}

type ProviderRetryDiagnostic struct {
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	Protocol    Protocol `json:"protocol,omitempty"`
	Transport   string   `json:"transport,omitempty"`
	Operation   string   `json:"operation"`
	Attempt     int      `json:"attempt"`
	MaxAttempts int      `json:"max_attempts"`
	DelayMS     int64    `json:"delay_ms,omitempty"`
	RetryReason string   `json:"retry_reason"`
	RawError    string   `json:"raw_error,omitempty"`
	WillRetry   bool     `json:"will_retry"`
	Exhausted   bool     `json:"exhausted,omitempty"`
}

type ProviderWithOptions interface {
	CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error)
}

const (
	providerMaxRetries       = 10
	DefaultStreamIdleTimeout = 90 * time.Second
)

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
	WorkDir        string
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
	return NewProvider(profile)
}

// NewProvider constructs the concrete provider for a resolved profile.
func NewProvider(profile ProviderProfile) (Provider, error) {
	if profile.APIKey == "" {
		return nil, fmt.Errorf("llm: missing API key")
	}
	if profile.Model == "" {
		return nil, fmt.Errorf("llm: missing model")
	}
	profile = cloneProviderProfile(profile)
	switch profile.Protocol {
	case ProtocolAnthropicMessages:
		return NewAnthropic(profile, &http.Client{Timeout: 120 * time.Second}), nil
	case ProtocolOpenAIChat:
		return NewOpenAI(profile, &http.Client{Timeout: 120 * time.Second}), nil
	case ProtocolOpenAIResponses:
		return NewOpenAIResponses(profile, &http.Client{Timeout: 120 * time.Second}), nil
	case ProtocolOpenAICodexResponses:
		transport, err := NormalizeCodexTransport(profile.Compat.CodexTransport)
		if err != nil {
			return nil, err
		}
		if transport == "" {
			transport = CodexTransportSSE
		}
		profile.Compat.CodexTransport = transport
		return NewOpenAICodexResponses(profile, nil), nil
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

func streamIdleTimeout(opts CompleteOptions) time.Duration {
	if opts.StreamIdleTimeout != 0 {
		return opts.StreamIdleTimeout
	}
	return DefaultStreamIdleTimeout
}

func newStreamIdleContext(ctx context.Context, timeout time.Duration) (context.Context, func(), func(), func() bool) {
	if timeout <= 0 {
		return ctx, func() {}, func() {}, func() bool { return false }
	}
	streamCtx, cancel := context.WithCancel(ctx)
	var (
		mu       sync.Mutex
		once     sync.Once
		expired  bool
		stopped  bool
		deadline = time.Now().Add(timeout)
	)
	timer := time.NewTimer(timeout)
	stopCh := make(chan struct{})
	go func() {
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				mu.Lock()
				if stopped || expired {
					mu.Unlock()
					return
				}
				if remaining := time.Until(deadline); remaining > 0 {
					timer.Reset(remaining)
					mu.Unlock()
					continue
				}
				expired = true
				mu.Unlock()
				cancel()
				return
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			}
		}
	}()
	reset := func() {
		mu.Lock()
		defer mu.Unlock()
		if stopped || expired {
			return
		}
		deadline = time.Now().Add(timeout)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(timeout)
	}
	stop := func() {
		once.Do(func() {
			mu.Lock()
			stopped = true
			mu.Unlock()
			close(stopCh)
			cancel()
		})
	}
	isExpired := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return expired
	}
	return streamCtx, reset, stop, isExpired
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
