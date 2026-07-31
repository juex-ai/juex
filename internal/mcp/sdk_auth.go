package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/errorclass"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"
)

const oauthRefreshTimeout = 15 * time.Second

func newOAuthHandler(spec *AuthSpec) (auth.OAuthHandler, error) {
	if spec == nil {
		return nil, nil
	}
	switch {
	case spec.Token != nil:
		return &staticOAuthHandler{
			source: oauth2.StaticTokenSource(&oauth2.Token{
				AccessToken: spec.Token.Value(),
				TokenType:   "Bearer",
			}),
		}, nil
	case spec.Refresh != nil:
		refresh := spec.Refresh
		return &refreshOAuthHandler{
			config: oauth2.Config{
				ClientID:     refresh.ClientID,
				ClientSecret: credentialValue(refresh.ClientSecret),
				Endpoint: oauth2.Endpoint{
					TokenURL: refresh.TokenURL,
				},
				Scopes: append([]string(nil), refresh.Scopes...),
			},
			refreshToken: refresh.RefreshToken.Value(),
		}, nil
	default:
		return nil, fmt.Errorf("exactly one of token or refresh is required")
	}
}

func credentialValue(credential *Credential) string {
	if credential == nil {
		return ""
	}
	return credential.Value()
}

func credentialValues(spec *AuthSpec) []string {
	if spec == nil {
		return nil
	}
	var values []string
	if spec.Token != nil {
		values = appendSecret(values, spec.Token.Value())
	}
	if spec.Refresh != nil {
		values = appendSecret(values, credentialValue(spec.Refresh.ClientSecret))
		values = appendSecret(values, spec.Refresh.RefreshToken.Value())
	}
	return values
}

func appendSecret(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func redactSecrets(text string, values []string) string {
	return redactSecretValues(text, secretRedactionValues(values))
}

func secretRedactionValues(values []string) []string {
	secrets := make([]string, 0, len(values)*2)
	for _, value := range values {
		secrets = appendSecret(secrets, value)
		encoded, err := json.Marshal(value)
		if err == nil && len(encoded) >= 2 {
			secrets = appendSecret(secrets, string(encoded[1:len(encoded)-1]))
		}
		secrets = appendSecret(secrets, url.QueryEscape(value))
	}
	sort.Slice(secrets, func(i, j int) bool {
		if len(secrets[i]) == len(secrets[j]) {
			return secrets[i] < secrets[j]
		}
		return len(secrets[i]) > len(secrets[j])
	})
	return secrets
}

func redactSecretValues(text string, secrets []string) string {
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}

type staticOAuthHandler struct {
	source oauth2.TokenSource
}

func (h *staticOAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return h.source, nil
}

func (h *staticOAuthHandler) Authorize(_ context.Context, _ *http.Request, response *http.Response) error {
	closeAuthResponse(response)
	return fmt.Errorf("static token was rejected; interactive authorization is not configured")
}

type refreshOAuthHandler struct {
	mu             sync.Mutex
	config         oauth2.Config
	refreshToken   string
	token          *oauth2.Token
	refreshTimeout time.Duration
	httpClient     *http.Client
}

type oauthRefreshDeadlineContextKey struct{}

func withOAuthRefreshDeadline(ctx context.Context) context.Context {
	deadline, ok := ctx.Deadline()
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, oauthRefreshDeadlineContextKey{}, deadline)
}

func (h *refreshOAuthHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return h.redactingTokenSource(oauthTokenSourceFunc(func() (*oauth2.Token, error) {
		return h.refresh(ctx)
	})), nil
}

func (h *refreshOAuthHandler) refresh(parent context.Context) (*oauth2.Token, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.token != nil && h.token.Valid() {
		return cloneOAuthToken(h.token), nil
	}
	seed := cloneOAuthToken(h.token)
	if seed == nil {
		seed = &oauth2.Token{
			RefreshToken: h.refreshToken,
			Expiry:       time.Unix(0, 0),
		}
	}
	timeout := h.refreshTimeout
	if timeout <= 0 {
		timeout = oauthRefreshTimeout
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if deadline, ok := parent.Value(oauthRefreshDeadlineContextKey{}).(time.Time); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline)
		defer deadlineCancel()
	}
	httpClient := h.httpClient
	if httpClient == nil {
		httpClient = newSecureEndpointHTTPClient(nil)
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	token, err := h.config.TokenSource(ctx, seed).Token()
	if err != nil {
		return nil, err
	}
	if token.RefreshToken == "" {
		token.RefreshToken = h.refreshToken
	}
	h.refreshToken = token.RefreshToken
	h.token = cloneOAuthToken(token)
	return cloneOAuthToken(token), nil
}

func (h *refreshOAuthHandler) redactingTokenSource(source oauth2.TokenSource) oauth2.TokenSource {
	h.mu.Lock()
	secrets := []string{h.refreshToken, h.config.ClientSecret}
	if h.config.ClientSecret != "" {
		secrets = appendSecret(secrets, basicAuthCredential(h.config.ClientID, h.config.ClientSecret))
		secrets = appendSecret(secrets, basicAuthCredential(
			url.QueryEscape(h.config.ClientID),
			url.QueryEscape(h.config.ClientSecret),
		))
	}
	h.mu.Unlock()
	return &redactingTokenSource{source: source, secrets: secrets}
}

func basicAuthCredential(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

func cloneOAuthToken(token *oauth2.Token) *oauth2.Token {
	if token == nil {
		return nil
	}
	clone := *token
	return &clone
}

type oauthTokenSourceFunc func() (*oauth2.Token, error)

func (f oauthTokenSourceFunc) Token() (*oauth2.Token, error) {
	return f()
}

func (h *refreshOAuthHandler) Authorize(_ context.Context, _ *http.Request, response *http.Response) error {
	closeAuthResponse(response)
	h.mu.Lock()
	h.token = nil
	h.mu.Unlock()
	return nil
}

func closeAuthResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}

type redactingTokenSource struct {
	source    oauth2.TokenSource
	secretsMu sync.RWMutex
	secrets   []string
	onToken   func(*oauth2.Token)
}

func (s *redactingTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.source.Token()
	if err == nil {
		if token != nil && token.RefreshToken != "" {
			s.secretsMu.Lock()
			s.secrets = appendSecret(s.secrets, token.RefreshToken)
			s.secretsMu.Unlock()
		}
		if s.onToken != nil {
			s.onToken(token)
		}
		return token, nil
	}
	message := err.Error()
	s.secretsMu.RLock()
	secrets := append([]string(nil), s.secrets...)
	s.secretsMu.RUnlock()
	message = redactSecrets(message, secrets)
	redacted := &redactedCauseError{message: message, cause: err}
	return nil, errorclass.WithKind(tokenSourceErrorKind(err), redacted)
}

type redactedCauseError struct {
	message string
	cause   error
}

func (e *redactedCauseError) Error() string { return e.message }
func (e *redactedCauseError) Unwrap() error { return e.cause }

func tokenSourceErrorKind(err error) errorclass.Kind {
	if errors.Is(err, context.DeadlineExceeded) {
		return errorclass.KindTimeout
	}
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		statusCode := 0
		if retrieveErr.Response != nil {
			statusCode = retrieveErr.Response.StatusCode
		}
		switch {
		case statusCode == http.StatusRequestTimeout,
			statusCode == http.StatusTooManyRequests,
			statusCode >= http.StatusInternalServerError:
			return errorclass.KindRetryable
		case statusCode == http.StatusForbidden:
			return errorclass.KindPermission
		default:
			return errorclass.KindAuth
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errorclass.KindTimeout
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return errorclass.KindConnectivity
	}
	if netErr != nil {
		return errorclass.KindConnectivity
	}
	return errorclass.KindAuth
}
