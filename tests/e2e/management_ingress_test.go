//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/postgres"
)

func TestManagementPublicIngressSessionAndCSRF(t *testing.T) {
	for _, mode := range []string{"http", "proxy"} {
		t.Run(mode, func(t *testing.T) {
			pool, directory := managementDatabase(t)
			auth, err := postgres.NewAuth(directory)
			if err != nil {
				t.Fatal(err)
			}
			public := httptest.NewUnstartedServer(nil)
			scheme := "https"
			if mode == "http" {
				scheme = "http"
			}
			origin := scheme + "://" + public.Listener.Addr().String()
			handler, err := managementhttp.New(managementhttp.Options{
				Auth: auth, Directory: directory, PublicURL: origin,
				InsecureHTTP: mode == "http", Health: pool.Ping,
			})
			if err != nil {
				t.Fatal(err)
			}
			backend := httptest.NewServer(handler)
			t.Cleanup(backend.Close)
			target, err := url.Parse(backend.URL)
			if err != nil {
				t.Fatal(err)
			}
			// Both ingress modes use an HTTP service hop. Only the public origin
			// determines the session policy, including when TLS ends at the edge.
			public.Config.Handler = httputil.NewSingleHostReverseProxy(target)
			if mode == "proxy" {
				public.StartTLS()
			} else {
				public.Start()
			}
			t.Cleanup(public.Close)
			client := public.Client()
			client.Jar, err = cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			link, err := auth.BeginBootstrap(context.Background(), "Ingress", "admin@example.test")
			if err != nil {
				t.Fatal(err)
			}
			managementCall[any](t, client, "POST", origin+"/api/auth/set-password", origin,
				map[string]string{"token": linkToken(t, link), "password": "long ingress password"}, 200)
			request, err := http.NewRequest("POST", origin+"/api/auth/login",
				strings.NewReader(`{"email":"admin@example.test","password":"long ingress password"}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", origin)
			// A caller-supplied transport header cannot weaken the configured policy.
			request.Header.Set("X-Forwarded-Proto", "http")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var session management.Session
			if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
				t.Fatal(err)
			}
			cookies := response.Cookies()
			if response.StatusCode != http.StatusOK || len(cookies) != 1 {
				t.Fatalf("login: status=%d cookies=%v", response.StatusCode, cookies)
			}
			cookie := cookies[0]
			if cookie.Secure != (mode == "proxy") || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("incorrect public session policy: %+v", cookie)
			}
			managementCall[any](t, client, "GET", origin+"/api/auth/session", origin, nil, 200)
			managementCall[any](t, client, "POST", origin+"/api/auth/logout", "http://attacker.invalid", map[string]any{}, 403)
			managementCall[any](t, client, "GET", origin+"/api/auth/session", origin, nil, 200)
			managementCall[any](t, client, "POST", origin+"/api/auth/logout", origin, map[string]any{}, 200)
			managementCall[any](t, client, "GET", origin+"/api/auth/session", origin, nil, 401)
		})
	}
}
