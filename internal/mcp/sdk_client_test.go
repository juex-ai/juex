package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/errorclass"
	"github.com/juex-ai/juex/internal/tools"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type remoteEchoInput struct {
	Text string `json:"text"`
}

func TestMCPClient_RemoteToolRoundTrip(t *testing.T) {
	server := newRemoteMCPTestServer(t, nil)
	cfg := Config{MCPServers: map[string]ServerSpec{
		"remote": {URL: server.URL},
	}}
	registry := tools.NewRegistry()
	clients, err := RegisterAll(t.Context(), cfg, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAll(clients) })

	tool, ok := registry.Get("mcp__remote__echo")
	if !ok {
		t.Fatalf("registered tools = %#v", registry.List())
	}
	if tool.Group != tools.ToolGroupMCP {
		t.Fatalf("tool group = %q, want %q", tool.Group, tools.ToolGroupMCP)
	}
	output, err := tool.Handler(t.Context(), map[string]any{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if output != "remote: hello" {
		t.Fatalf("output = %q", output)
	}
}

func TestMCPClient_RemoteStaticToken(t *testing.T) {
	const token = "static-secret"
	server := newRemoteMCPTestServer(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "missing bearer token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	client, err := Connect(t.Context(), "remote", ServerSpec{
		URL: server.URL,
		Auth: &AuthSpec{
			Token: &Credential{value: token},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	output, err := client.CallTool(t.Context(), "echo", map[string]any{"text": "authorized"})
	if err != nil {
		t.Fatal(err)
	}
	if output != "remote: authorized" {
		t.Fatalf("output = %q", output)
	}
}

func TestMCPClient_RemoteRefreshTokenRetriesAfterUnauthorized(t *testing.T) {
	const (
		clientID     = "client-id"
		clientSecret = "client-secret"
		refreshToken = "refresh-secret"
	)
	var tokenRequests atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotID, gotSecret, ok := r.BasicAuth()
		if !ok || gotID != clientID || gotSecret != clientSecret ||
			r.Form.Get("grant_type") != "refresh_token" {
			http.Error(w, "invalid refresh request", http.StatusBadRequest)
			return
		}
		attempt := tokenRequests.Add(1)
		accessToken := "stale-access"
		nextRefreshToken := "rotated-refresh"
		if attempt == 1 {
			if r.Form.Get("refresh_token") != refreshToken {
				http.Error(w, "expected original refresh token", http.StatusBadRequest)
				return
			}
		} else {
			if r.Form.Get("refresh_token") != nextRefreshToken {
				http.Error(w, "expected rotated refresh token", http.StatusBadRequest)
				return
			}
			accessToken = "fresh-access"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  accessToken,
			"refresh_token": nextRefreshToken,
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(tokenServer.Close)

	server := newRemoteMCPTestServer(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer fresh-access" {
				http.Error(w, "expired access token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	client, err := Connect(t.Context(), "remote", ServerSpec{
		URL: server.URL,
		Auth: &AuthSpec{Refresh: &RefreshAuthSpec{
			TokenURL:     tokenServer.URL,
			ClientID:     clientID,
			ClientSecret: &Credential{value: clientSecret},
			RefreshToken: Credential{value: refreshToken},
			Scopes:       []string{"mcp.tools"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if got := tokenRequests.Load(); got != 2 {
		t.Fatalf("token requests = %d, want 2", got)
	}
	output, err := client.CallTool(t.Context(), "echo", map[string]any{"text": "refreshed"})
	if err != nil {
		t.Fatal(err)
	}
	if output != "remote: refreshed" {
		t.Fatalf("output = %q", output)
	}
}

func TestMCPClient_RemoteLegacySessionLifecycleAndNotification(t *testing.T) {
	const (
		protocolVersion = "2025-11-25"
		sessionID       = "legacy-session"
	)
	notification := make(chan Notification, 1)
	getHeaders := make(chan http.Header, 2)
	deleteHeaders := make(chan http.Header, 1)
	var getAttempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if !hasLegacySessionHeaders(r.Header, protocolVersion, sessionID) {
				http.Error(w, "missing legacy GET headers", http.StatusBadRequest)
				return
			}
			getHeaders <- r.Header.Clone()
			attempt := getAttempts.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if attempt == 1 {
				_, _ = fmt.Fprintf(w, "id: event-1\nretry: 1\ndata: %s\n\n", sdkChannelMessage)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				return
			}
			<-r.Context().Done()
		case http.MethodDelete:
			deleteHeaders <- r.Header.Clone()
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPost:
			var request map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var method string
			_ = json.Unmarshal(request["method"], &method)
			switch method {
			case "server/discover":
				http.Error(w, "legacy server", http.StatusNotFound)
			case "initialize":
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Mcp-Session-Id", sessionID)
				writeSDKTestResult(t, w, request["id"], map[string]any{
					"protocolVersion": protocolVersion,
					"serverInfo":      map[string]any{"name": "legacy", "version": "1.0.0"},
					"capabilities":    map[string]any{"tools": map[string]any{}},
				})
			case "notifications/initialized":
				if !hasLegacySessionHeaders(r.Header, protocolVersion, sessionID) {
					http.Error(w, "missing initialized headers", http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusAccepted)
			case "tools/list":
				if !hasLegacySessionHeaders(r.Header, protocolVersion, sessionID) {
					http.Error(w, "missing tools/list headers", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				writeSDKTestResult(t, w, request["id"], map[string]any{"tools": []any{}})
			default:
				http.Error(w, "unexpected method "+method, http.StatusBadRequest)
			}
		default:
			http.Error(w, "unexpected HTTP method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	client, err := ConnectWithOptions(t.Context(), "legacy", ServerSpec{URL: server.URL}, ConnectOptions{
		OnNotification: func(got Notification) {
			notification <- got
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListTools(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-notification:
		if got.ServerName != "legacy" || got.EventType != "message" || got.Content != "hello" {
			t.Fatalf("notification = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for standalone SSE notification")
	}
	for attempt := 1; attempt <= 2; attempt++ {
		select {
		case headers := <-getHeaders:
			if !hasLegacySessionHeaders(headers, protocolVersion, sessionID) {
				t.Fatalf("GET %d headers = %v", attempt, headers)
			}
			if attempt == 1 && headers.Get("Last-Event-ID") != "" {
				t.Fatalf("initial GET Last-Event-ID = %q", headers.Get("Last-Event-ID"))
			}
			if attempt == 2 && headers.Get("Last-Event-ID") != "event-1" {
				t.Fatalf("resumed GET Last-Event-ID = %q, want event-1", headers.Get("Last-Event-ID"))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for standalone SSE GET %d", attempt)
		}
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case headers := <-deleteHeaders:
		if !hasLegacySessionHeaders(headers, protocolVersion, sessionID) {
			t.Fatalf("DELETE headers = %v", headers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session DELETE")
	}
}

func hasLegacySessionHeaders(headers http.Header, protocolVersion, sessionID string) bool {
	return headers.Get("Mcp-Protocol-Version") == protocolVersion &&
		headers.Get("Mcp-Session-Id") == sessionID
}

func writeSDKTestResult(
	t *testing.T,
	w http.ResponseWriter,
	id json.RawMessage,
	result any,
) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}); err != nil {
		t.Errorf("encode MCP response: %v", err)
	}
}

func TestMCPClient_RemoteDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       string
		wantKind   errorclass.Kind
	}{
		{
			name:       "permission denied",
			statusCode: http.StatusForbidden,
			want:       "remote MCP permission denied",
			wantKind:   errorclass.KindPermission,
		},
		{
			name:       "wrong endpoint",
			statusCode: http.StatusNotFound,
			want:       "remote MCP endpoint is incorrect",
			wantKind:   errorclass.KindWrongEndpoint,
		},
		{
			name:       "retryable rate limit",
			statusCode: http.StatusTooManyRequests,
			want:       "retryable remote MCP failure",
			wantKind:   errorclass.KindRetryable,
		},
		{
			name:       "retryable server failure",
			statusCode: http.StatusServiceUnavailable,
			want:       "retryable remote MCP failure",
			wantKind:   errorclass.KindRetryable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream detail", test.statusCode)
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := Connect(ctx, "remote", ServerSpec{URL: server.URL})
			if err == nil {
				t.Fatal("expected remote startup error")
			}
			for _, want := range []string{test.want, fmt.Sprintf("HTTP %d", test.statusCode), "upstream detail"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
			if got := errorclass.Classify(err).Kind; got != test.wantKind {
				t.Fatalf("error kind = %q, want %q", got, test.wantKind)
			}
		})
	}
}

func TestMCPClient_RemoteConnectivityClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err := Connect(ctx, "remote", ServerSpec{URL: url})
	if err == nil {
		t.Fatal("expected connectivity error")
	}
	if !strings.Contains(err.Error(), "remote MCP connectivity failed") {
		t.Fatalf("error = %v", err)
	}
	if got := errorclass.Classify(err).Kind; got != errorclass.KindConnectivity {
		t.Fatalf("error kind = %q, want connectivity", got)
	}
}

func TestMCPClient_RemoteDiagnosticsRedactCredentials(t *testing.T) {
	const secret = "static-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rejected "+secret, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := Connect(t.Context(), "remote", ServerSpec{
		URL:  server.URL,
		Auth: &AuthSpec{Token: &Credential{value: secret}},
	})
	if err == nil {
		t.Fatal("expected authentication error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("credential leaked in error: %v", err)
	}
	for _, want := range []string{"remote MCP authentication failed", "HTTP 401", "[REDACTED]"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
	if got := errorclass.Classify(err).Kind; got != errorclass.KindAuth {
		t.Fatalf("error kind = %q, want auth", got)
	}
}

func TestMCPClient_RefreshTokenErrorRedactsCredentials(t *testing.T) {
	const (
		clientSecret = "client-secret"
		refreshToken = "refresh-secret"
	)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad "+clientSecret+" "+refreshToken, http.StatusBadRequest)
	}))
	defer tokenServer.Close()
	remoteServer := newRemoteMCPTestServer(t, nil)

	_, err := Connect(t.Context(), "remote", ServerSpec{
		URL: remoteServer.URL,
		Auth: &AuthSpec{Refresh: &RefreshAuthSpec{
			TokenURL:     tokenServer.URL,
			ClientID:     "client-id",
			ClientSecret: &Credential{value: clientSecret},
			RefreshToken: Credential{value: refreshToken},
		}},
	})
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if strings.Contains(err.Error(), clientSecret) || strings.Contains(err.Error(), refreshToken) {
		t.Fatalf("credential leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") ||
		!strings.Contains(err.Error(), "remote MCP authentication failed") {
		t.Fatalf("error = %v", err)
	}
	if got := errorclass.Classify(err).Kind; got != errorclass.KindAuth {
		t.Fatalf("error kind = %q, want auth", got)
	}
}

func TestMCPClient_RefreshAccessTokenIsRedactedFromRemoteError(t *testing.T) {
	const (
		accessToken  = "dynamic-access-secret"
		refreshToken = "refresh-secret"
	)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": accessToken,
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer tokenServer.Close()
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected "+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), http.StatusUnauthorized)
	}))
	defer remoteServer.Close()

	_, err := Connect(t.Context(), "remote", ServerSpec{
		URL: remoteServer.URL,
		Auth: &AuthSpec{Refresh: &RefreshAuthSpec{
			TokenURL:     tokenServer.URL,
			ClientID:     "client-id",
			RefreshToken: Credential{value: refreshToken},
		}},
	})
	if err == nil {
		t.Fatal("expected authentication error")
	}
	if strings.Contains(err.Error(), accessToken) {
		t.Fatalf("dynamic access token leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error = %v, want redacted body excerpt", err)
	}
	if got := errorclass.Classify(err).Kind; got != errorclass.KindAuth {
		t.Fatalf("error kind = %q, want auth", got)
	}
}

func newRemoteMCPTestServer(
	t *testing.T,
	wrap func(http.Handler) http.Handler,
) *httptest.Server {
	t.Helper()
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "remote-test", Version: "1.0.0"},
		nil,
	)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "echo",
		Description: "Echo text from a remote MCP server",
	}, func(
		_ context.Context,
		_ *sdkmcp.CallToolRequest,
		input remoteEchoInput,
	) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "remote: " + input.Text},
			},
		}, nil, nil
	})
	var handler http.Handler = sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		&sdkmcp.StreamableHTTPOptions{Stateless: true},
	)
	if wrap != nil {
		handler = wrap(handler)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}
