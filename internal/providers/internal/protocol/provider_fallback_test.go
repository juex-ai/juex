package protocol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/juex-ai/juex/internal/foundation/llm"
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
		wantReason llm.FallbackFailureReason
		wantOK     bool
	}{
		{name: "rate limit", err: errors.New("provider failed: HTTP 429"), wantReason: llm.FallbackFailureTransient, wantOK: true},
		{name: "server", err: errors.New("provider failed: status 503"), wantReason: llm.FallbackFailureTransient, wantOK: true},
		{name: "truncated json", err: errors.New("openai responses stream: unexpected end of JSON input"), wantReason: llm.FallbackFailureTransient, wantOK: true},
		{name: "timeout", err: context.DeadlineExceeded, wantReason: llm.FallbackFailureTransient, wantOK: true},
		{name: "unauthorized", err: errors.New("provider failed: status 401"), wantReason: llm.FallbackFailureUnauthorized, wantOK: true},
		{name: "typed forbidden", err: typedForbidden, wantReason: llm.FallbackFailureForbidden, wantOK: true},
		{name: "model not found code", err: errors.New("status 404: model_not_found"), wantReason: llm.FallbackFailureModelNotFound, wantOK: true},
		{name: "unknown model", err: errors.New("invalid request: unknown model gpt-missing"), wantReason: llm.FallbackFailureModelNotFound, wantOK: true},
		{name: "generic route not found", err: errors.New("status 404: route not found"), wantOK: false},
		{name: "context overflow", err: errors.New("status 400: context_length_exceeded"), wantOK: false},
		{name: "cancelled", err: context.Canceled, wantOK: false},
		{name: "semantic", err: errors.New("invalid tool schema"), wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := llm.ClassifyFallbackError(WrapProviderError(tt.err))
			if ok != tt.wantOK || got != tt.wantReason {
				t.Fatalf("ClassifyFallbackError() = %q, %v, want %q, %v", got, ok, tt.wantReason, tt.wantOK)
			}
		})
	}
}
