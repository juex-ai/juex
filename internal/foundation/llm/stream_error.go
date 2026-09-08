package llm

import (
	"errors"
	"fmt"
	"strings"
)

const StreamParseErrorKindAnthropic = "anthropic_stream_parse"

// StreamParseError preserves provider stream parse context without requiring
// callers to know the provider SDK's internal error types.
type StreamParseError struct {
	Kind       string
	Provider   string
	EventType  string
	Index      int64
	HasIndex   bool
	RawPreview string
	Cause      error
}

func (e *StreamParseError) Error() string {
	if e == nil {
		return "<nil>"
	}
	kind := e.Kind
	if kind == "" {
		kind = "provider_stream_parse"
	}
	parts := []string{"kind=" + kind}
	if e.Provider != "" {
		parts = append(parts, "provider="+e.Provider)
	}
	if e.EventType != "" {
		parts = append(parts, "event_type="+e.EventType)
	}
	if e.HasIndex {
		parts = append(parts, fmt.Sprintf("index=%d", e.Index))
	}
	if e.RawPreview != "" {
		parts = append(parts, fmt.Sprintf("raw_preview=%q", e.RawPreview))
	}
	if e.Cause != nil {
		return fmt.Sprintf("provider stream parse error: %s: %v", strings.Join(parts, " "), e.Cause)
	}
	return "provider stream parse error: " + strings.Join(parts, " ")
}

func (e *StreamParseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func AsStreamParseError(err error) (*StreamParseError, bool) {
	var streamErr *StreamParseError
	if errors.As(err, &streamErr) {
		return streamErr, true
	}
	return nil, false
}
