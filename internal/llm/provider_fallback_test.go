package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
)

func TestClassifyFallbackError(t *testing.T) {
	typedForbidden := &anthropicsdk.Error{
		StatusCode: http.StatusForbidden,
		Request:    httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil),
		Response:   &http.Response{StatusCode: http.StatusForbidden},
	}
	tests := []struct {
		name       string
		err        error
		wantReason FallbackFailureReason
		wantOK     bool
	}{
		{name: "rate limit", err: errors.New("provider failed: HTTP 429"), wantReason: FallbackFailureTransient, wantOK: true},
		{name: "server", err: errors.New("provider failed: status 503"), wantReason: FallbackFailureTransient, wantOK: true},
		{name: "truncated json", err: errors.New("openai responses stream: unexpected end of JSON input"), wantReason: FallbackFailureTransient, wantOK: true},
		{name: "timeout", err: context.DeadlineExceeded, wantReason: FallbackFailureTransient, wantOK: true},
		{name: "unauthorized", err: errors.New("provider failed: status 401"), wantReason: FallbackFailureUnauthorized, wantOK: true},
		{name: "typed forbidden", err: typedForbidden, wantReason: FallbackFailureForbidden, wantOK: true},
		{name: "model not found code", err: errors.New("status 404: model_not_found"), wantReason: FallbackFailureModelNotFound, wantOK: true},
		{name: "unknown model", err: errors.New("invalid request: unknown model gpt-missing"), wantReason: FallbackFailureModelNotFound, wantOK: true},
		{name: "generic route not found", err: errors.New("status 404: route not found"), wantOK: false},
		{name: "context overflow", err: errors.New("status 400: context_length_exceeded"), wantOK: false},
		{name: "cancelled", err: context.Canceled, wantOK: false},
		{name: "retry suppressed", err: fmt.Errorf("retry suppressed after output: %w", errors.New("status 503")), wantOK: false},
		{name: "semantic", err: errors.New("invalid tool schema"), wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ClassifyFallbackError(tt.err)
			if ok != tt.wantOK || got != tt.wantReason {
				t.Fatalf("ClassifyFallbackError() = %q, %v, want %q, %v", got, ok, tt.wantReason, tt.wantOK)
			}
		})
	}
}
