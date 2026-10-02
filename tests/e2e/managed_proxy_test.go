//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/juex-ai/juex/internal/entrypoints/executionhttp"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func TestManagedProxySeparatesAuthenticationAndDeviceClients(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	managementHTTP, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, PublicURL: f.origin, InsecureHTTP: true, TrustedProxies: "127.0.0.1,::1"})
	if err != nil {
		t.Fatal(err)
	}
	deviceHTTP, err := executionhttp.New(ctx, f.execution, executionhttp.Options{TrustedProxies: "127.0.0.1,::1"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", managementHTTP)
	mux.Handle("/device/", deviceHTTP)
	backend := httptest.NewServer(mux)
	defer backend.Close()
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(request *httputil.ProxyRequest) {
		request.SetURL(target)
		host, _, err := net.SplitHostPort(request.In.RemoteAddr)
		if err != nil {
			t.Error(err)
		}
		// The gateway overwrites a supplied header from its own TCP peer, for
		// both /api/ and /device/. The backend observes one shared proxy peer.
		request.Out.Header.Set("X-Real-IP", host)
	}}
	call := func(client, path string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "https://example.test"+path, bytes.NewReader(data))
		request.RemoteAddr = net.JoinHostPort(client, "12345")
		request.Header.Set("Content-Type", "application/json")
		// A caller cannot choose its bucket by supplying forwarded headers.
		request.Header.Set("X-Real-IP", "198.51.100.99")
		request.Header.Set("X-Forwarded-For", "198.51.100.100")
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", client, path, response.Code, status, response.Body.String())
		}
		return response
	}
	for range 120 {
		call("192.0.2.1", "/api/auth/missing", nil, 404)
	}
	call("192.0.2.1", "/api/auth/login", map[string]string{}, 429)
	response := call("192.0.2.2", "/api/auth/login", map[string]string{"email": "runtime@example.test", "password": "runtime test password"}, 200)
	var session management.Session
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil || session.User.ID != f.actor {
		t.Fatal("proxy login failed", err)
	}
	if len(response.Result().Cookies()) != 1 {
		t.Fatal("proxy login did not set a session cookie")
	}

	for range 240 {
		call("192.0.2.1", "/device/missing", nil, 404)
	}
	call("192.0.2.1", "/device/pair", map[string]string{}, 429)
	proof := execution.PairConfirmation{ID: rand.Text(), Secret: rand.Text() + rand.Text(), Credential: rand.Text() + rand.Text()}
	pairRequest := execution.PairRequest{ID: proof.ID, Name: "Proxy laptop", OS: "darwin", WorkingDirectory: "/Users/test", PairSecretHash: execution.Digest(proof.Secret), CredentialHash: execution.Digest(proof.Credential), Capabilities: []execprotocol.Capability{execprotocol.Files}}
	call("192.0.2.2", "/device/pair", pairRequest, 200)
	if _, err := f.execution.ApprovePair(ctx, f.actor, f.tenant, proof.ID, map[string][]execprotocol.Capability{f.agent.ID: pairRequest.Capabilities}); err != nil {
		t.Fatal(err)
	}
	response = call("192.0.2.2", "/device/pair/poll", map[string]string{"id": proof.ID, "secret": proof.Secret}, 200)
	var pairing execution.Pairing
	if err := json.Unmarshal(response.Body.Bytes(), &pairing); err != nil || pairing.ApprovalNonce == "" {
		t.Fatal("proxy pairing challenge failed", err)
	}
	proof.ApprovalNonce = pairing.ApprovalNonce
	response = call("192.0.2.2", "/device/pair/confirm", proof, 200)
	var device execution.Device
	if err := json.Unmarshal(response.Body.Bytes(), &device); err != nil || device.UserID != f.actor {
		t.Fatal("proxy device confirmation failed", err)
	}
}
