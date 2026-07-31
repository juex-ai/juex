package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/errorclass"
)

const remoteDiagnosticBodyBytes = 4 * 1024

type remoteDiagnosticContextKey struct{}

type remoteDiagnostic struct {
	mu          sync.Mutex
	statusCode  int
	bodyExcerpt string
}

func newRemoteDiagnostic() *remoteDiagnostic {
	return &remoteDiagnostic{}
}

func withRemoteDiagnostic(ctx context.Context, diagnostic *remoteDiagnostic) context.Context {
	if diagnostic == nil {
		return ctx
	}
	return context.WithValue(ctx, remoteDiagnosticContextKey{}, diagnostic)
}

func remoteDiagnosticFromContext(ctx context.Context) *remoteDiagnostic {
	diagnostic, _ := ctx.Value(remoteDiagnosticContextKey{}).(*remoteDiagnostic)
	return diagnostic
}

func (d *remoteDiagnostic) record(statusCode int, bodyExcerpt string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.statusCode = statusCode
	d.bodyExcerpt = strings.TrimSpace(bodyExcerpt)
	d.mu.Unlock()
}

func (d *remoteDiagnostic) enrich(err error) error {
	if err == nil || d == nil {
		return err
	}
	d.mu.Lock()
	statusCode := d.statusCode
	bodyExcerpt := d.bodyExcerpt
	d.mu.Unlock()

	if kind, ok := errorclass.ExplicitKind(err); ok {
		message := "remote MCP token request failed"
		switch kind {
		case errorclass.KindAuth:
			message = "remote MCP authentication failed"
		case errorclass.KindPermission:
			message = "remote MCP token request permission denied"
		case errorclass.KindConnectivity:
			message = "remote MCP token endpoint connectivity failed"
		case errorclass.KindTimeout:
			message = "remote MCP token request timed out"
		case errorclass.KindRetryable:
			message = "retryable remote MCP token request failed"
		}
		return errorclass.WithKind(kind, fmt.Errorf("%s: %w", message, err))
	}
	if statusCode > 0 && statusCode < http.StatusBadRequest {
		return err
	}
	if statusCode == 0 {
		return errorclass.WithKind(
			errorclass.KindConnectivity,
			fmt.Errorf("remote MCP connectivity failed: %w", err),
		)
	}
	status := fmt.Sprintf("HTTP %d %s", statusCode, http.StatusText(statusCode))
	detail := ""
	if bodyExcerpt != "" {
		detail = ": " + bodyExcerpt
	}
	switch {
	case statusCode == http.StatusUnauthorized:
		return errorclass.WithKind(
			errorclass.KindAuth,
			fmt.Errorf("remote MCP authentication failed (%s%s): %w", status, detail, err),
		)
	case statusCode == http.StatusForbidden:
		return errorclass.WithKind(
			errorclass.KindPermission,
			fmt.Errorf("remote MCP permission denied (%s%s): %w", status, detail, err),
		)
	case statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed:
		return errorclass.WithKind(
			errorclass.KindWrongEndpoint,
			fmt.Errorf("remote MCP endpoint is incorrect (%s%s): %w", status, detail, err),
		)
	case statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError:
		return errorclass.WithKind(
			errorclass.KindRetryable,
			fmt.Errorf("retryable remote MCP failure (%s%s): %w", status, detail, err),
		)
	default:
		return fmt.Errorf("remote MCP request failed (%s%s): %w", status, detail, err)
	}
}

type remoteDiagnosticRoundTripper struct {
	base           http.RoundTripper
	redactions     []string
	serverName     string
	onNotification func(Notification)
}

func (t *remoteDiagnosticRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	diagnostic := remoteDiagnosticFromContext(request.Context())
	if diagnostic != nil {
		diagnostic.record(0, "")
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode >= http.StatusOK &&
		response.StatusCode < http.StatusMultipleChoices &&
		response.Body != nil &&
		strings.EqualFold(mediaType, "text/event-stream") {
		response.Body = newSSEChannelFilter(
			response.Body,
			t.serverName,
			t.onNotification,
		)
	}
	if diagnostic == nil {
		return response, nil
	}
	if response.StatusCode < http.StatusBadRequest || response.Body == nil {
		diagnostic.record(response.StatusCode, "")
		return response, nil
	}

	redactions := append([]string(nil), t.redactions...)
	if authorization := request.Header.Get("Authorization"); strings.HasPrefix(authorization, "Bearer ") {
		redactions = append(redactions, strings.TrimPrefix(authorization, "Bearer "))
	}
	redactionValues := secretRedactionValues(redactions)
	readLimit := remoteDiagnosticBodyBytes + longestString(redactionValues)
	data, readErr := io.ReadAll(io.LimitReader(response.Body, int64(readLimit+1)))
	_ = response.Body.Close()
	bodyExcerpt := redactSecretValues(string(data), redactionValues)
	bodyExcerpt = truncateDiagnosticExcerpt(bodyExcerpt, remoteDiagnosticBodyBytes)
	response.Body = io.NopCloser(bytes.NewReader([]byte(bodyExcerpt)))
	diagnostic.record(response.StatusCode, bodyExcerpt)
	if readErr != nil {
		return nil, readErr
	}
	return response, nil
}

func longestString(values []string) int {
	longest := 0
	for _, value := range values {
		if len(value) > longest {
			longest = len(value)
		}
	}
	return longest
}

func truncateDiagnosticExcerpt(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	const redactionMarker = "[REDACTED]"
	for index := max(0, limit-len(redactionMarker)+1); index < limit; index++ {
		if strings.HasPrefix(text[index:], redactionMarker) {
			return text[:limit-len(redactionMarker)] + redactionMarker
		}
	}
	return text[:limit]
}
