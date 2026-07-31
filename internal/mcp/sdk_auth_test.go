package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/errorclass"
	"golang.org/x/oauth2"
)

func TestRedactingTokenSourceTracksRotatedRefreshToken(t *testing.T) {
	const rotated = "rotated-refresh-secret"
	var calls atomic.Int32
	source := &redactingTokenSource{
		source: tokenSourceFunc(func() (*oauth2.Token, error) {
			if calls.Add(1) == 1 {
				return &oauth2.Token{
					AccessToken:  "access",
					RefreshToken: rotated,
				}, nil
			}
			return nil, errors.New("refresh rejected " + rotated)
		}),
	}
	if _, err := source.Token(); err != nil {
		t.Fatal(err)
	}
	_, err := source.Token()
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if strings.Contains(err.Error(), rotated) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error = %v", err)
	}
	if got := errorclass.Classify(err).Kind; got != errorclass.KindAuth {
		t.Fatalf("error kind = %q, want auth", got)
	}
}

func TestRedactingTokenSourceRedactsOverlappingSecrets(t *testing.T) {
	const (
		shortSecret = "token-prefix"
		longSecret  = shortSecret + "-sensitive-suffix"
	)
	source := &redactingTokenSource{
		source: tokenSourceFunc(func() (*oauth2.Token, error) {
			return nil, errors.New("refresh rejected " + longSecret + " and " + shortSecret)
		}),
		secrets: []string{shortSecret, longSecret},
	}
	_, err := source.Token()
	if err == nil {
		t.Fatal("expected refresh error")
	}
	for _, leaked := range []string{shortSecret, "sensitive-suffix"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("credential fragment %q leaked in error: %v", leaked, err)
		}
	}
}

func TestRedactingTokenSourceRedactsJSONEscapedSecret(t *testing.T) {
	const secret = "token\"with\\slashes\nand-newline"
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	escaped := string(encoded[1 : len(encoded)-1])
	source := &redactingTokenSource{
		source: tokenSourceFunc(func() (*oauth2.Token, error) {
			return nil, errors.New("refresh rejected " + escaped)
		}),
		secrets: []string{secret},
	}
	_, err = source.Token()
	if err == nil {
		t.Fatal("expected refresh error")
	}
	for _, leaked := range []string{escaped, "with", "slashes", "and-newline"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("escaped credential fragment %q leaked in error: %v", leaked, err)
		}
	}
}

func TestRedactingTokenSourceRedactsFormEncodedSecret(t *testing.T) {
	const secret = "a+b/c% token"
	encoded := url.QueryEscape(secret)
	cause := &oauth2.RetrieveError{
		Response: &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
		},
		Body: []byte("request=" + encoded),
	}
	source := &redactingTokenSource{
		source: tokenSourceFunc(func() (*oauth2.Token, error) {
			return nil, cause
		}),
		secrets: []string{secret},
	}
	_, err := source.Token()
	if err == nil {
		t.Fatal("expected refresh error")
	}
	for _, leaked := range []string{encoded, "%2B", "%2F", "%25"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("form-encoded credential fragment %q leaked in error: %v", leaked, err)
		}
	}
	if !errors.Is(err, cause) {
		t.Fatal("redaction broke the typed error chain")
	}
}

func TestRefreshOAuthHandlerRedactsBasicAuthCredential(t *testing.T) {
	const (
		clientID     = "client+id"
		clientSecret = "a+b/c% token"
	)
	encoded := base64.StdEncoding.EncodeToString([]byte(
		url.QueryEscape(clientID) + ":" + url.QueryEscape(clientSecret),
	))
	handler := &refreshOAuthHandler{
		config: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
		},
		refreshToken: "refresh-token",
	}
	source := handler.redactingTokenSource(tokenSourceFunc(func() (*oauth2.Token, error) {
		return nil, &oauth2.RetrieveError{
			Response: &http.Response{
				StatusCode: http.StatusBadRequest,
				Status:     "400 Bad Request",
			},
			Body: []byte("Authorization: Basic " + encoded),
		}
	}))
	_, err := source.Token()
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if strings.Contains(err.Error(), encoded) || strings.Contains(err.Error(), clientSecret) {
		t.Fatalf("Basic-auth credential leaked in error: %v", err)
	}
}

func TestRefreshOAuthHandlerBoundsRefreshRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer tokenServer.Close()
	defer close(release)

	handler := &refreshOAuthHandler{
		config: oauth2.Config{
			ClientID: "client",
			Endpoint: oauth2.Endpoint{TokenURL: tokenServer.URL},
		},
		refreshToken:   "refresh-token",
		refreshTimeout: 50 * time.Millisecond,
	}
	source, err := handler.TokenSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = source.Token()
	if err == nil {
		t.Fatal("expected bounded token refresh to time out")
	}
	select {
	case <-started:
	default:
		t.Fatal("token refresh did not start")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("token refresh took %v, want a bounded failure", elapsed)
	}
}

func TestRefreshOAuthHandlerRejectsInsecureRedirect(t *testing.T) {
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://auth.example.test/token", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	transport := &insecureCountingTransport{base: redirect.Client().Transport}
	handler := &refreshOAuthHandler{
		config: oauth2.Config{
			ClientID:     "client",
			ClientSecret: "client-secret",
			Endpoint: oauth2.Endpoint{
				TokenURL:  redirect.URL,
				AuthStyle: oauth2.AuthStyleInHeader,
			},
		},
		refreshToken: "refresh-token",
		httpClient:   newSecureEndpointHTTPClient(transport),
	}
	source, err := handler.TokenSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Token()
	if err == nil || !strings.Contains(err.Error(), "insecure redirect") {
		t.Fatalf("Token error = %v, want insecure redirect rejection", err)
	}
	if got := transport.insecureRequests.Load(); got != 0 {
		t.Fatalf("downgrade target received %d token requests", got)
	}
}

func TestRedactingTokenSourceClassifiesTypedFailures(t *testing.T) {
	const secret = "token-secret"
	tests := []struct {
		name     string
		cause    error
		wantKind errorclass.Kind
		timedOut bool
	}{
		{
			name: "connectivity",
			cause: &url.Error{
				Op:  "Post",
				URL: "https://token.example.test/" + secret,
				Err: syscall.ECONNREFUSED,
			},
			wantKind: errorclass.KindConnectivity,
		},
		{
			name:     "timeout",
			cause:    context.DeadlineExceeded,
			wantKind: errorclass.KindTimeout,
			timedOut: true,
		},
		{
			name: "URL timeout",
			cause: &url.Error{
				Op:  "Post",
				URL: "https://token.example.test/",
				Err: timeoutNetError{},
			},
			wantKind: errorclass.KindTimeout,
			timedOut: true,
		},
		{
			name: "rate limit",
			cause: &oauth2.RetrieveError{
				Response: &http.Response{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests"},
				Body:     []byte("retry " + secret),
			},
			wantKind: errorclass.KindRetryable,
		},
		{
			name: "server failure",
			cause: &oauth2.RetrieveError{
				Response: &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable"},
				Body:     []byte("failed " + secret),
			},
			wantKind: errorclass.KindRetryable,
		},
		{
			name: "permission",
			cause: &oauth2.RetrieveError{
				Response: &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden"},
				Body:     []byte("denied " + secret),
			},
			wantKind: errorclass.KindPermission,
		},
		{
			name: "invalid grant",
			cause: &oauth2.RetrieveError{
				Response:         &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request"},
				ErrorCode:        "invalid_grant",
				ErrorDescription: "rejected " + secret,
			},
			wantKind: errorclass.KindAuth,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &redactingTokenSource{
				source: tokenSourceFunc(func() (*oauth2.Token, error) {
					return nil, test.cause
				}),
				secrets: []string{secret},
			}
			_, err := source.Token()
			if err == nil {
				t.Fatal("expected token-source error")
			}
			classification := errorclass.Classify(err)
			if classification.Kind != test.wantKind || classification.TimedOut != test.timedOut {
				t.Fatalf("classification = %+v, want kind=%q timedOut=%v", classification, test.wantKind, test.timedOut)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("credential leaked in error: %v", err)
			}
			if !errors.Is(err, test.cause) {
				t.Fatalf("error chain does not preserve cause %T", test.cause)
			}
		})
	}
}

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) {
	return f()
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "network timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }
