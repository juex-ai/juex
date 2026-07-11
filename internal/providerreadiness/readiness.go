package providerreadiness

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
)

const InitSuggestion = "run `juex init` to get started"

type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

type Category string

const (
	CategorySelection    Category = "selection"
	CategoryConfig       Category = "config"
	CategoryCredentials  Category = "credentials"
	CategoryConnectivity Category = "connectivity"
)

type Result struct {
	Category   Category
	Status     Status
	Message    string
	Suggestion string
	Details    map[string]any
	Err        error
}

type Probe interface {
	Probe(ctx context.Context, profile llm.ProviderProfile) error
}

type ProbeFunc func(ctx context.Context, profile llm.ProviderProfile) error

func (f ProbeFunc) Probe(ctx context.Context, profile llm.ProviderProfile) error {
	return f(ctx, profile)
}

type LLMProbe struct{}

type ProviderConstructionError struct {
	Err error
}

func (e *ProviderConstructionError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ProviderConstructionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (LLMProbe) Probe(ctx context.Context, profile llm.ProviderProfile) error {
	provider, err := llm.NewProvider(profile)
	if err != nil {
		return &ProviderConstructionError{Err: err}
	}
	_, err = provider.Complete(ctx, "Reply with a short hello.", []llm.Message{
		llm.TextMessage(llm.RoleUser, "hello"),
	}, nil)
	return err
}

type ConnectivityOptions struct {
	Offline bool
	Timeout time.Duration
	Probe   Probe
}

func CheckSelection(cfg config.Config) Result {
	selection := cfg.ProviderSelection()
	if strings.TrimSpace(selection.ID) == "" && strings.TrimSpace(selection.Protocol) == "" && strings.TrimSpace(selection.Model) == "" {
		return selectionFailure("no Juex runtime config found")
	}
	if strings.TrimSpace(selection.Model) == "" {
		return selectionFailure("Juex runtime config has no selected model")
	}
	if strings.TrimSpace(selection.ID) == "" && strings.TrimSpace(selection.Protocol) == "" {
		return selectionFailure("Juex runtime config has no selected provider")
	}
	return Result{Category: CategorySelection, Status: StatusOK, Message: "selected runtime config available"}
}

func selectionFailure(message string) Result {
	err := fmt.Errorf("%s; %s", message, InitSuggestion)
	return Result{
		Category:   CategorySelection,
		Status:     StatusFail,
		Message:    message,
		Suggestion: InitSuggestion,
		Err:        err,
	}
}

func ResolveProfile(cfg config.Config) (llm.ProviderProfile, Result) {
	profile, err := cfg.ProviderSelection().ProviderProfile()
	if err != nil {
		return llm.ProviderProfile{}, Result{
			Category:   CategoryConfig,
			Status:     StatusFail,
			Message:    err.Error(),
			Suggestion: "fix provider config first",
			Err:        err,
		}
	}
	return profile, Result{
		Category: CategoryConfig,
		Status:   StatusOK,
		Message:  fmt.Sprintf("selected %s:%s using %s", profile.ID, profile.Model, profile.Protocol),
		Details: map[string]any{
			"provider": profile.ID,
			"protocol": string(profile.Protocol),
			"model":    profile.Model,
		},
	}
}

func CheckCredentials(selection config.ProviderSelection) Result {
	if strings.TrimSpace(selection.APIKey) != "" {
		return Result{Category: CategoryCredentials, Status: StatusOK, Message: "credentials available"}
	}
	status := StatusFail
	if AllowsMissingAPIKey(selection) {
		status = StatusWarn
	}
	suggestion := "set providers[].api_key or PROVIDER_API_KEY"
	if selection.ID == "openai-codex" || selection.Protocol == string(llm.ProtocolOpenAICodexResponses) {
		suggestion = "run Codex login or set providers[].api_key"
	}
	return Result{
		Category:   CategoryCredentials,
		Status:     status,
		Message:    "selected provider has no API key",
		Suggestion: suggestion,
	}
}

func AllowsMissingAPIKey(selection config.ProviderSelection) bool {
	if !knownProviderID(selection.ID) {
		return true
	}
	if raw := strings.TrimSpace(selection.BaseURL); raw != "" {
		u, err := url.Parse(raw)
		if err == nil {
			host := u.Hostname()
			if strings.EqualFold(host, "localhost") {
				return true
			}
			if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
				return true
			}
		}
	}
	return false
}

func knownProviderID(id string) bool {
	switch strings.TrimSpace(id) {
	case "anthropic", "openai", "openai-codex", "deepseek":
		return true
	default:
		return false
	}
}

func CheckConnectivity(ctx context.Context, cfg config.Config, opts ConnectivityOptions) Result {
	if opts.Offline {
		return Result{Category: CategoryConnectivity, Status: StatusOK, Message: "skipped because --offline was set"}
	}
	profile, profileResult := ResolveProfile(cfg)
	if profileResult.Status != StatusOK {
		return Result{
			Category:   CategoryConnectivity,
			Status:     StatusFail,
			Message:    profileResult.Message,
			Suggestion: profileResult.Suggestion,
			Err:        profileResult.Err,
		}
	}
	probe := opts.Probe
	if probe == nil {
		probe = LLMProbe{}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := probe.Probe(checkCtx, profile); err != nil {
		suggestion := "check network, base_url, API key, and model id"
		var constructionErr *ProviderConstructionError
		if errors.As(err, &constructionErr) {
			suggestion = "fix provider credentials and model config"
		}
		return Result{
			Category:   CategoryConnectivity,
			Status:     StatusFail,
			Message:    err.Error(),
			Suggestion: suggestion,
			Err:        err,
		}
	}
	return Result{Category: CategoryConnectivity, Status: StatusOK, Message: "provider hello check passed"}
}
