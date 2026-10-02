package managementhttp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/management"
)

type proxyTestAuth struct{ Auth }

func (proxyTestAuth) Login(context.Context, string, string) (management.Session, error) {
	return management.Session{}, management.ErrCredentials
}

type proxyTestDirectory struct{ Directory }

func TestAuthenticationProxyRateLimit(t *testing.T) {
	for _, trusted := range []string{"", "172.30.0.11"} {
		t.Run("trusted="+trusted, func(t *testing.T) {
			handler, err := New(Options{Auth: proxyTestAuth{}, Directory: proxyTestDirectory{}, PublicURL: "https://example.test", TrustedProxies: trusted})
			if err != nil {
				t.Fatal(err)
			}
			call := func(client string) *httptest.ResponseRecorder {
				request := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"email":"example@example.test","password":"bad password"}`))
				request.RemoteAddr = "172.30.0.11:1234"
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Real-IP", client)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
			}
			for range 120 {
				if response := call("192.0.2.1"); response.Code != 401 {
					t.Fatal(response.Code)
				}
			}
			if response := call("192.0.2.1"); response.Code != 429 || response.Header().Get("Retry-After") != "60" {
				t.Fatal(response.Code, response.Header())
			}
			want := 401
			if trusted == "" {
				want = 429
			}
			if response := call("192.0.2.2"); response.Code != want {
				t.Fatalf("second client: got %d, want %d", response.Code, want)
			}
		})
	}
}

func TestInvalidAuthenticationProxyConfiguration(t *testing.T) {
	if _, err := New(Options{Auth: proxyTestAuth{}, Directory: proxyTestDirectory{}, PublicURL: "https://example.test", TrustedProxies: "gateway:80"}); err == nil {
		t.Fatal("invalid proxy configuration accepted")
	}
}
