package executionhttp

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestDeviceProxyRateLimit(t *testing.T) {
	for _, trusted := range []string{"", "172.30.0.11"} {
		t.Run("trusted="+trusted, func(t *testing.T) {
			handler, err := New(context.Background(), nil, Options{TrustedProxies: trusted})
			if err != nil {
				t.Fatal(err)
			}
			call := func(client string) *httptest.ResponseRecorder {
				request := httptest.NewRequest("POST", "/device/missing", nil)
				request.RemoteAddr = "172.30.0.11:1234"
				request.Header.Set("X-Real-IP", client)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
			}
			for range 240 {
				if response := call("192.0.2.1"); response.Code != 404 {
					t.Fatal(response.Code)
				}
			}
			if response := call("192.0.2.1"); response.Code != 429 || response.Header().Get("Retry-After") != "60" {
				t.Fatal(response.Code, response.Header())
			}
			want := 404
			if trusted == "" {
				want = 429
			}
			if response := call("192.0.2.2"); response.Code != want {
				t.Fatalf("second client: got %d, want %d", response.Code, want)
			}
		})
	}
}

func TestInvalidDeviceProxyConfiguration(t *testing.T) {
	if _, err := New(context.Background(), nil, Options{TrustedProxies: "gateway:80"}); err == nil {
		t.Fatal("invalid proxy configuration accepted")
	}
}
