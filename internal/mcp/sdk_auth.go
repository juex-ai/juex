package mcp

import (
	"context"
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
	mu           sync.Mutex
	config       oauth2.Config
	refreshToken string
	source       oauth2.TokenSource
}

func (h *refreshOAuthHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.source == nil {
		h.source = &redactingTokenSource{
			source: h.config.TokenSource(ctx, &oauth2.Token{
				RefreshToken: h.refreshToken,
				Expiry:       time.Unix(0, 0),
			}),
			secrets: []string{h.refreshToken, h.config.ClientSecret},
			onToken: func(token *oauth2.Token) {
				if token == nil || token.RefreshToken == "" {
					return
				}
				h.mu.Lock()
				h.refreshToken = token.RefreshToken
				h.mu.Unlock()
			},
		}
	}
	return h.source, nil
}

func (h *refreshOAuthHandler) Authorize(_ context.Context, _ *http.Request, response *http.Response) error {
	closeAuthResponse(response)
	h.mu.Lock()
	h.source = nil
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
