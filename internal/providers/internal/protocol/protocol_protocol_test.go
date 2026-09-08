package protocol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/juex-ai/juex/internal/foundation/llm"
	openaisdk "github.com/openai/openai-go"
)

func TestStreamIdleTimeout(t *testing.T) {
	tests := []struct {
		name string
		opts llm.CompleteOptions
		want time.Duration
	}{
		{name: "default", want: 3 * time.Minute},
		{name: "override", opts: llm.CompleteOptions{StreamIdleTimeout: 17 * time.Second}, want: 17 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StreamIdleTimeout(tt.opts); got != tt.want {
				t.Fatalf("streamIdleTimeout() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestStreamIdleContextCancelsAfterTimeout(t *testing.T) {
	ctx, _, stop, expired := NewStreamIdleContext(context.Background(), 10*time.Millisecond)
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream idle context did not cancel")
	}
	if !expired() {
		t.Fatal("expired = false, want true")
	}
}

func TestStreamIdleContextResetExtendsDeadline(t *testing.T) {
	const timeout = 40 * time.Millisecond
	ctx, reset, stop, expired := NewStreamIdleContext(context.Background(), timeout)
	defer stop()

	time.Sleep(25 * time.Millisecond)
	reset()
	select {
	case <-ctx.Done():
		t.Fatal("stream idle context canceled before the reset deadline")
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream idle context did not cancel after the reset deadline")
	}
	if !expired() {
		t.Fatal("expired = false, want true")
	}
}

func TestIsRetryableProviderError(t *testing.T) {
	openAIServerErr := &openaisdk.Error{
		StatusCode: http.StatusInternalServerError,
		Request:    httptest.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil),
		Response:   &http.Response{StatusCode: http.StatusInternalServerError},
	}
	anthropicPermissionErr := &anthropic.Error{
		StatusCode: http.StatusForbidden,
		Request:    httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil),
		Response:   &http.Response{StatusCode: http.StatusForbidden},
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "connection reset", err: errors.New("read: connection reset by peer"), want: true},
		{name: "sse read", err: errors.New("codex SSE read: stream error"), want: true},
		{name: "http2 stream read", err: errors.New("openai responses stream: stream error: stream ID 7; INTERNAL_ERROR; received from peer"), want: true},
		{name: "request timeout status", err: errors.New("provider request failed: status 408"), want: true},
		{name: "conflict status", err: errors.New("provider request failed: status code 409"), want: true},
		{name: "rate limit status", err: errors.New("provider request failed: HTTP 429"), want: true},
		{name: "server status", err: errors.New("codex websocket error: status 503: unavailable"), want: true},
		{name: "typed openai server status", err: openAIServerErr, want: true},
		{name: "bad request status", err: errors.New("codex websocket error: status 400: bad request"), want: false},
		{name: "payment status", err: errors.New("provider request failed: status 402"), want: false},
		{name: "not found status", err: errors.New("provider request failed: HTTP 404"), want: false},
		{name: "auth status", err: errors.New("provider request failed: status 401"), want: false},
		{name: "typed anthropic permission status", err: anthropicPermissionErr, want: false},
		{name: "context overflow", err: errors.New("context_length_exceeded"), want: false},
		{name: "semantic error", err: errors.New("invalid request: unsupported tool schema"), want: false},
		{name: "streamed semantic error", err: errors.New("openai responses stream error: invalid_request_error"), want: false},
		{name: "exit code", err: errors.New("provider helper exit code 500"), want: false},
		{name: "port number", err: errors.New("dial localhost port 500"), want: false},
		{name: "cancelled", err: context.Canceled, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := llm.IsRetryableProviderError(WrapProviderError(tt.err)); got != tt.want {
				t.Fatalf("IsRetryableProviderError() = %v, want %v", got, tt.want)
			}
		})
	}
}
