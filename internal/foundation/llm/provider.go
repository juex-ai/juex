package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Provider interface {
	Name() string
	Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error)
}

type CompleteOptions struct {
	Purpose           string
	MaxOutputTokens   int
	ThinkingEffort    string
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

const DefaultStreamIdleTimeout = 3 * time.Minute

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

// IsRetryableProviderError reports whether a failed provider request is safe to
// attempt again with newly accepted input. Provider SDKs already perform their
// own retries; this only distinguishes exhausted transient failures from
// deterministic request and authentication errors.
func IsRetryableProviderError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	lower := strings.ToLower(err.Error())
	if status, ok := providerHTTPStatusCode(err); ok {
		return status == http.StatusRequestTimeout ||
			status == http.StatusConflict ||
			status == http.StatusTooManyRequests ||
			status >= http.StatusInternalServerError
	}
	if IsContextOverflowError(err) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	if strings.Contains(lower, "stream error: stream id") && strings.Contains(lower, "received from peer") {
		return true
	}
	for _, marker := range []string{
		"connection reset",
		"connection refused",
		"connection aborted",
		"broken pipe",
		"network is unreachable",
		"no route to host",
		"server disconnected",
		"unexpected eof",
		"unexpected end of json input",
		": eof",
		"sse read",
		"stream idle timeout",
		"websocket: close 1006",
		"rate limit",
		"too many requests",
		"temporary server error",
		"temporarily unavailable",
		"internal server error",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
		"deadline exceeded",
		"timed out",
		"timeout",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

type FallbackFailureReason string

const (
	FallbackFailureTransient     FallbackFailureReason = "transient"
	FallbackFailureUnauthorized  FallbackFailureReason = "unauthorized"
	FallbackFailureForbidden     FallbackFailureReason = "forbidden"
	FallbackFailureModelNotFound FallbackFailureReason = "model_not_found"
)

// ClassifyFallbackError reports provider failures that are safe to resend to
// a different configured model. Whether streamed output was already emitted
// is request-local state and must be checked by the runtime before fallback.
func ClassifyFallbackError(err error) (FallbackFailureReason, bool) {
	if err == nil || errors.Is(err, context.Canceled) || IsContextOverflowError(err) {
		return "", false
	}
	lower := strings.ToLower(err.Error())
	if IsRetryableProviderError(err) {
		return FallbackFailureTransient, true
	}
	if status, ok := providerHTTPStatusCode(err); ok {
		switch status {
		case http.StatusUnauthorized:
			return FallbackFailureUnauthorized, true
		case http.StatusForbidden:
			return FallbackFailureForbidden, true
		}
	}
	if modelNotFoundError(lower) {
		return FallbackFailureModelNotFound, true
	}
	return "", false
}

func modelNotFoundError(lower string) bool {
	for _, marker := range []string{
		"model_not_found",
		"model not found",
		"unknown model",
		"model does not exist",
		"model doesn't exist",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func providerHTTPStatusCode(err error) (int, bool) {
	var statusError interface{ HTTPStatusCode() int }
	if errors.As(err, &statusError) && validHTTPStatusCode(statusError.HTTPStatusCode()) {
		return statusError.HTTPStatusCode(), true
	}

	fields := strings.FieldsFunc(strings.ToLower(err.Error()), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	for i, field := range fields {
		next := i + 1
		switch {
		case field == "status":
			if next < len(fields) && fields[next] == "code" {
				next++
			}
		case field == "http":
		case field == "code" && i > 0 && fields[i-1] == "error":
		default:
			continue
		}
		if next >= len(fields) {
			continue
		}
		status, parseErr := strconv.Atoi(fields[next])
		if parseErr == nil && validHTTPStatusCode(status) {
			return status, true
		}
	}
	return 0, false
}

func validHTTPStatusCode(status int) bool {
	return status >= 100 && status <= 599
}
