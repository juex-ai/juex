package errorclass

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/juex-ai/juex/internal/cancellation"
)

type Kind string

const (
	KindError          Kind = "error"
	KindTimeout        Kind = "timeout"
	KindCancelled      Kind = "cancelled"
	KindInterrupted    Kind = "interrupted"
	KindTerminated     Kind = "terminated"
	KindPermission     Kind = "permission"
	KindAuth           Kind = "auth"
	KindConnectivity   Kind = "connectivity"
	KindWrongEndpoint  Kind = "wrong_endpoint"
	KindRetryable      Kind = "retryable"
	KindRuntimeRestart Kind = "runtime_restart"
)

type kindCarrier interface {
	ErrorKind() Kind
}

type kindError struct {
	kind Kind
	err  error
}

func (e *kindError) Error() string { return e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }
func (e *kindError) ErrorKind() Kind {
	return e.kind
}

type Classification struct {
	Kind     Kind
	TimedOut bool
	RawCause string
}

type MessageOptions struct {
	Subject        string
	TimeoutSeconds int
}

func Classify(err error) Classification {
	if err == nil {
		return Classification{Kind: KindError}
	}
	raw := err.Error()
	if signalErr, ok := cancellation.AsSignalError(err); ok {
		return Classification{Kind: Kind(signalErr.Kind), RawCause: raw}
	}
	if errors.Is(err, cancellation.ErrRuntimeRestart) {
		return Classification{Kind: KindRuntimeRestart, RawCause: raw}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Classification{Kind: KindTimeout, TimedOut: true, RawCause: raw}
	}
	if cancellation.IsUserCancelled(err) {
		return Classification{Kind: KindCancelled, RawCause: raw}
	}
	var carrier kindCarrier
	if errors.As(err, &carrier) && carrier.ErrorKind() != "" {
		kind := carrier.ErrorKind()
		return Classification{
			Kind:     kind,
			TimedOut: kind == KindTimeout,
			RawCause: raw,
		}
	}
	return ClassifyText(raw)
}

// WithKind attaches an explicit category without changing the public error
// text or breaking errors.Is/errors.As traversal.
func WithKind(kind Kind, err error) error {
	if err == nil {
		return nil
	}
	if kind == "" {
		return err
	}
	return &kindError{kind: kind, err: err}
}

// ExplicitKind returns a category attached with WithKind, if present.
func ExplicitKind(err error) (Kind, bool) {
	if err == nil {
		return "", false
	}
	var carrier kindCarrier
	if !errors.As(err, &carrier) || carrier.ErrorKind() == "" {
		return "", false
	}
	return carrier.ErrorKind(), true
}

func ClassifyText(raw string) Classification {
	lower := strings.ToLower(raw)
	switch {
	case isTimeoutText(lower):
		return Classification{Kind: KindTimeout, TimedOut: true, RawCause: raw}
	case strings.Contains(lower, "cancel"):
		return Classification{Kind: KindCancelled, RawCause: raw}
	case strings.Contains(lower, "permission") || strings.Contains(lower, "denied") || strings.Contains(lower, "forbidden") || hasStatusCode(lower, "403"):
		return Classification{Kind: KindPermission, RawCause: raw}
	case strings.Contains(lower, "auth") || strings.Contains(lower, "unauthorized") || hasStatusCode(lower, "401"):
		return Classification{Kind: KindAuth, RawCause: raw}
	default:
		return Classification{Kind: KindError, RawCause: raw}
	}
}

func hasStatusCode(lower, code string) bool {
	fields := strings.FieldsFunc(lower, func(r rune) bool {
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
		if next < len(fields) && fields[next] == code {
			return true
		}
	}
	return false
}

func IsTimeout(err error) bool {
	return Classify(err).TimedOut
}

func IsTimeoutText(raw string) bool {
	return ClassifyText(raw).TimedOut
}

func KindForError(err error) string {
	return string(Classify(err).Kind)
}

func KindForText(raw string) string {
	return string(ClassifyText(raw).Kind)
}

func PublicMessage(err error, opts MessageOptions) string {
	if err == nil {
		return ""
	}
	if signalErr, ok := cancellation.AsSignalError(err); ok {
		return signalErr.Error()
	}
	if errors.Is(err, cancellation.ErrRuntimeRestart) {
		return cancellation.ErrRuntimeRestart.Error()
	}
	if cancellation.IsUserCancelled(err) {
		return cancellation.ErrUserCancelled.Error()
	}
	return PublicText(err.Error(), opts)
}

func PublicText(raw string, opts MessageOptions) string {
	classification := ClassifyText(raw)
	if !classification.TimedOut {
		return raw
	}
	if opts.Subject == "" && opts.TimeoutSeconds <= 0 && isAlreadyPublicTimeout(raw) {
		return raw
	}
	return timeoutMessage(raw, opts)
}

func isTimeoutText(lower string) bool {
	return strings.Contains(lower, "deadline_exceeded") ||
		strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "read deadline") ||
		strings.Contains(lower, "write deadline") ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "timed out")
}

func isAlreadyPublicTimeout(raw string) bool {
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "deadline_exceeded") ||
		strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "read deadline") ||
		strings.Contains(lower, "write deadline") {
		return false
	}
	return strings.Contains(lower, "timed out")
}

func timeoutMessage(raw string, opts MessageOptions) string {
	subject := strings.TrimSpace(opts.Subject)
	if subject == "" {
		subject = timeoutPrefix(raw)
	}
	if subject == "" {
		subject = "operation"
	}
	if opts.TimeoutSeconds > 0 {
		return subject + " timed out after " + formatSeconds(opts.TimeoutSeconds)
	}
	return subject + " timed out"
}

func timeoutPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	markers := []string{
		"context deadline exceeded",
		"deadline_exceeded",
		"deadline exceeded",
		"read deadline",
		"write deadline",
		"timed out",
		"timeout",
	}
	cut := len(raw)
	for _, marker := range markers {
		if idx := strings.Index(lower, marker); idx >= 0 && idx < cut {
			cut = idx
		}
	}
	if cut == len(raw) {
		return ""
	}
	prefix := strings.TrimRight(strings.TrimSpace(raw[:cut]), ":;,- ")
	return prefix
}

func formatSeconds(seconds int) string {
	return strconv.Itoa(seconds) + "s"
}
