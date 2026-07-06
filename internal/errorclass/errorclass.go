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
	KindError       Kind = "error"
	KindTimeout     Kind = "timeout"
	KindCancelled   Kind = "cancelled"
	KindInterrupted Kind = "interrupted"
	KindTerminated  Kind = "terminated"
	KindPermission  Kind = "permission"
	KindAuth        Kind = "auth"
)

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
	if errors.Is(err, context.DeadlineExceeded) {
		return Classification{Kind: KindTimeout, TimedOut: true, RawCause: raw}
	}
	if cancellation.IsUserCancelled(err) {
		return Classification{Kind: KindCancelled, RawCause: raw}
	}
	return ClassifyText(raw)
}

func ClassifyText(raw string) Classification {
	lower := strings.ToLower(raw)
	switch {
	case isTimeoutText(lower):
		return Classification{Kind: KindTimeout, TimedOut: true, RawCause: raw}
	case strings.Contains(lower, "cancel"):
		return Classification{Kind: KindCancelled, RawCause: raw}
	case strings.Contains(lower, "permission") || strings.Contains(lower, "denied"):
		return Classification{Kind: KindPermission, RawCause: raw}
	case strings.Contains(lower, "auth") || strings.Contains(lower, "unauthorized"):
		return Classification{Kind: KindAuth, RawCause: raw}
	default:
		return Classification{Kind: KindError, RawCause: raw}
	}
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
