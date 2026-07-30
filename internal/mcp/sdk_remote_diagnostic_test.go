package mcp

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/juex-ai/juex/internal/errorclass"
)

func TestRemoteDiagnosticRoundTripperResetsStatusBeforeNetworkError(t *testing.T) {
	var attempts atomic.Int32
	transport := &remoteDiagnosticRoundTripper{
		base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			if attempts.Add(1) == 1 {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("stale authentication body")),
				}, nil
			}
			return nil, errors.New("dial token.example.test: connection refused")
		}),
	}
	diagnostic := newRemoteDiagnostic()
	request, err := http.NewRequestWithContext(
		withRemoteDiagnostic(t.Context(), diagnostic),
		http.MethodPost,
		"https://token.example.test/mcp",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	_, networkErr := transport.RoundTrip(request)
	if networkErr == nil {
		t.Fatal("expected network error")
	}
	enriched := diagnostic.enrich(networkErr)
	if got := errorclass.Classify(enriched).Kind; got != errorclass.KindConnectivity {
		t.Fatalf("error kind = %q, want connectivity; error=%v", got, enriched)
	}
	if strings.Contains(enriched.Error(), "stale authentication body") {
		t.Fatalf("stale response diagnostics leaked into retry error: %v", enriched)
	}
}

func TestRemoteDiagnosticRoundTripperRedactsAcrossExcerptBoundary(t *testing.T) {
	const secret = "cross-boundary-secret"
	body := strings.Repeat("x", remoteDiagnosticBodyBytes-6) + secret + "tail"
	transport := &remoteDiagnosticRoundTripper{
		base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}
	diagnostic := newRemoteDiagnostic()
	request, err := http.NewRequestWithContext(
		withRemoteDiagnostic(t.Context(), diagnostic),
		http.MethodPost,
		"https://example.test/mcp",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	restoredBody, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	enriched := diagnostic.enrich(errors.New("unauthorized"))
	for _, text := range []string{string(restoredBody), enriched.Error()} {
		if strings.Contains(text, secret) || strings.Contains(text, "cross-") {
			t.Fatalf("credential fragment leaked across excerpt boundary: %q", text)
		}
	}
	if !strings.Contains(enriched.Error(), "[REDACTED]") {
		t.Fatalf("error = %v, want redaction marker", enriched)
	}
}

func TestRemoteDiagnosticRoundTripperDoesNotDispatchRejectedSSE(t *testing.T) {
	callback := make(chan Notification, 1)
	transport := &remoteDiagnosticRoundTripper{
		base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header: http.Header{
					"Content-Type": []string{"text/event-stream; charset=utf-8"},
				},
				Body: io.NopCloser(strings.NewReader("data: " + sdkChannelMessage + "\n\n")),
			}, nil
		}),
		serverName: "rejected",
		onNotification: func(notification Notification) {
			callback <- notification
		},
	}
	diagnostic := newRemoteDiagnostic()
	request, err := http.NewRequestWithContext(
		withRemoteDiagnostic(t.Context(), diagnostic),
		http.MethodPost,
		"https://example.test/mcp",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	select {
	case notification := <-callback:
		t.Fatalf("rejected SSE dispatched notification: %+v", notification)
	default:
	}
}

func TestRemoteDiagnosticPreservesExplicitTokenFailureKinds(t *testing.T) {
	tests := []struct {
		kind errorclass.Kind
		want string
	}{
		{kind: errorclass.KindAuth, want: "remote MCP authentication failed"},
		{kind: errorclass.KindPermission, want: "remote MCP token request permission denied"},
		{kind: errorclass.KindConnectivity, want: "remote MCP token endpoint connectivity failed"},
		{kind: errorclass.KindTimeout, want: "remote MCP token request timed out"},
		{kind: errorclass.KindRetryable, want: "retryable remote MCP token request failed"},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			cause := errorclass.WithKind(test.kind, errors.New("token source failed"))
			enriched := newRemoteDiagnostic().enrich(cause)
			classification := errorclass.Classify(enriched)
			if classification.Kind != test.kind {
				t.Fatalf("error kind = %q, want %q; error=%v", classification.Kind, test.kind, enriched)
			}
			if test.kind == errorclass.KindTimeout && !classification.TimedOut {
				t.Fatalf("timeout classification = %+v", classification)
			}
			if !strings.Contains(enriched.Error(), test.want) {
				t.Fatalf("error = %v, want %q", enriched, test.want)
			}
		})
	}
}

func TestRemoteDiagnosticExplicitTokenFailureOverridesPreviousHTTPStatus(t *testing.T) {
	tests := []struct {
		kind errorclass.Kind
	}{
		{kind: errorclass.KindConnectivity},
		{kind: errorclass.KindRetryable},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			diagnostic := newRemoteDiagnostic()
			diagnostic.record(http.StatusUnauthorized, "stale authentication response")

			cause := errorclass.WithKind(test.kind, errors.New("token source failed"))
			enriched := diagnostic.enrich(cause)
			if got := errorclass.Classify(enriched).Kind; got != test.kind {
				t.Fatalf("error kind = %q, want %q; error=%v", got, test.kind, enriched)
			}
			if strings.Contains(enriched.Error(), "stale authentication response") {
				t.Fatalf("stale HTTP diagnostic overrode token failure: %v", enriched)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
