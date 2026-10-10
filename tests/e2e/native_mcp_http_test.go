//go:build linux || darwin

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNativeExecutorHTTPMCPUsesRealProtocolAndOriginalIdentity(t *testing.T) {
	for _, kind := range []string{"http", "sse"} {
		t.Run(kind, func(t *testing.T) {
			var calls, missingAuth atomic.Int64
			server := mcp.NewServer(&mcp.Implementation{Name: "parity-mcp", Version: "1.0"}, nil)
			type input struct {
				Text string `json:"text"`
			}
			mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo test text"}, func(ctx context.Context, request *mcp.CallToolRequest, args input) (*mcp.CallToolResult, any, error) {
				calls.Add(1)
				if err := request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: "parity", Progress: 1, Total: 1, Message: "after-http-call"}); err != nil {
					return nil, nil, err
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + args.Text}}}, nil, nil
			})
			var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
			if kind == "sse" {
				handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
			}
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer isolated-test" {
					missingAuth.Add(1)
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(endpoint.Close)
			config := nativeConfig(t)
			engine := openNative(t, config)
			request := nativeRequest(t, "http-connection", "mcp_connect", native.MCPArguments{MCPRemote: execprotocol.MCPRemote{Transport: kind, URL: endpoint.URL, Headers: map[string]string{"Authorization": "Bearer isolated-test"}}})
			if _, err := engine.Submit(request); err != nil {
				t.Fatal(err)
			}
			connected := nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.MCP != nil || snapshot.State.Terminal() })
			if connected.State != execprotocol.Running || connected.MCP == nil || connected.MCP.Transport != kind || connected.MCP.ServerName != "parity-mcp" || connected.MCP.ConnectedAt.IsZero() {
				t.Fatal(connected)
			}
			list := nativeRun(t, engine, nativeRequest(t, "http-list", "mcp_list", native.MCPArguments{ConnectionID: request.ID}))
			if list.State != execprotocol.Completed || !strings.Contains(list.Text(), `"name":"echo"`) {
				t.Fatal(list)
			}
			call := nativeRequest(t, "http-call", "mcp_call", native.MCPArguments{ConnectionID: request.ID, Name: "echo", Arguments: map[string]any{"text": "中文验收"}})
			for range 2 {
				result := nativeRun(t, engine, call)
				if result.State != execprotocol.Completed || !strings.Contains(result.Text(), "echo:中文验收") {
					t.Fatal(result)
				}
			}
			if calls.Load() != 1 || missingAuth.Load() != 0 {
				t.Fatal("repeated effect or lost authorization", calls.Load(), missingAuth.Load())
			}
			nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return strings.Contains(snapshot.Text(), "after-http-call") })
			if err := engine.Cancel("agent-one", request.ID); err != nil {
				t.Fatal(err)
			}
			cancelled := nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.State == execprotocol.Cancelled })
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			engine = openNative(t, config)
			retained, err := engine.Snapshot("agent-one", request.ID, 0, 64<<10)
			if err != nil || retained.State != execprotocol.Cancelled || retained.MCP == nil || !retained.MCP.ConnectedAt.Equal(cancelled.MCP.ConnectedAt) {
				t.Fatal("handshake lost across restart", retained, err)
			}
		})
	}
}

func TestNativeExecutorHTTPMCPDoesNotFollowCredentialRedirects(t *testing.T) {
	var requests atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer foreign.Close()
	for _, kind := range []string{"http", "sse"} {
		t.Run(kind, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, foreign.URL+"?secret=private-url-token", http.StatusTemporaryRedirect)
			}))
			t.Cleanup(endpoint.Close)
			engine := openNative(t, nativeConfig(t))
			result := nativeRun(t, engine, nativeRequest(t, "redirect", "mcp_connect", native.MCPArguments{MCPRemote: execprotocol.MCPRemote{Transport: kind, URL: endpoint.URL + "?key=private-url-token", Headers: map[string]string{"Authorization": "Bearer private-header"}}}))
			if result.State != execprotocol.Failed || result.MCP != nil || strings.Contains(result.Error, "private") || requests.Load() != 0 {
				t.Fatal(result, requests.Load())
			}
		})
	}
}

func TestNativeExecutorSSEMCPRejectsForeignMessageEndpoint(t *testing.T) {
	var requests atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer foreign.Close()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", foreign.URL)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(endpoint.Close)
	engine := openNative(t, nativeConfig(t))
	result := nativeRun(t, engine, nativeRequest(t, "sse-foreign", "mcp_connect", native.MCPArguments{MCPRemote: execprotocol.MCPRemote{Transport: "sse", URL: endpoint.URL, Headers: map[string]string{"Authorization": "Bearer isolated-secret"}}}))
	if result.State != execprotocol.Failed || result.MCP != nil || requests.Load() != 0 || strings.Contains(result.Error, "isolated-secret") {
		t.Fatal(result, requests.Load())
	}
}

func TestNativeExecutorHTTPMCPOldProtocolIdleNotifications(t *testing.T) {
	const protocol = "2025-11-25"
	var invalidHeaders atomic.Int64
	push := make(chan struct{})
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet {
			if r.Header.Get("Mcp-Protocol-Version") != protocol || r.Header.Get("Mcp-Session-Id") != "old-session" {
				invalidHeaders.Add(1)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-push:
			}
			fmt.Fprint(w, "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\r\ndata: \"params\":{\"progressToken\":\"idle\",\"progress\":1,\"message\":\"idle-standard-marker\"}}\r\n\r\n")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/claude/channel\",\"params\":{\"content\":\"idle-channel-marker\"}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Method == "server/discover" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			return
		}
		if request.Method != "initialize" && r.Header.Get("Mcp-Protocol-Version") != protocol {
			invalidHeaders.Add(1)
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any = map[string]any{"tools": []any{}}
		if request.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "old-session")
			result = map[string]any{"protocolVersion": protocol, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "old-wire-server", "version": "1"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(endpoint.Close)
	engine := openNative(t, nativeConfig(t))
	request := nativeRequest(t, "old-http-idle", "mcp_connect", native.MCPArguments{MCPRemote: execprotocol.MCPRemote{Transport: "http", URL: endpoint.URL}})
	if _, err := engine.Submit(request); err != nil {
		t.Fatal(err)
	}
	connected := nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.MCP != nil || snapshot.State.Terminal() })
	if connected.MCP == nil || connected.MCP.ProtocolVersion != protocol {
		t.Fatal(connected)
	}
	close(push)
	notified := nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool {
		return strings.Contains(snapshot.Text(), "idle-standard-marker") && strings.Contains(snapshot.Text(), "idle-channel-marker")
	})
	if notified.State != execprotocol.Running {
		t.Fatal(notified)
	}
	list := nativeRun(t, engine, nativeRequest(t, "old-http-list", "mcp_list", native.MCPArguments{ConnectionID: request.ID}))
	if list.State != execprotocol.Completed || invalidHeaders.Load() != 0 {
		t.Fatal(list, invalidHeaders.Load())
	}
}

func TestNativeExecutorHTTPMCPCancellationDoesNotExposeEndpoint(t *testing.T) {
	for _, kind := range []string{"http", "sse"} {
		t.Run(kind, func(t *testing.T) {
			started := make(chan struct{}, 1)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case started <- struct{}{}:
				default:
				}
				<-r.Context().Done()
			}))
			t.Cleanup(endpoint.Close)
			engine := openNative(t, nativeConfig(t))
			request := nativeRequest(t, "cancel-http", "mcp_connect", native.MCPArguments{MCPRemote: execprotocol.MCPRemote{Transport: kind, URL: endpoint.URL + "?secret=private-query-token"}})
			if _, err := engine.Submit(request); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("HTTP initialization did not start")
			}
			if err := engine.Cancel("agent-one", request.ID); err != nil {
				t.Fatal(err)
			}
			result := nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.State.Terminal() })
			if result.State != execprotocol.Cancelled || result.Error != context.Canceled.Error() || strings.Contains(result.Text(), "private-query-token") {
				t.Fatal(result)
			}
		})
	}
}

func TestNativeExecutorSSEMCPNotificationCaptureFailureIsVisible(t *testing.T) {
	for _, test := range []struct {
		name      string
		limit     int64
		size      int
		errorText string
	}{
		{"notification-limit", 8 << 20, 1 << 20, "MCP notification record exceeds 1 MiB"},
		{"output-quota", 1024, 2048, execprotocol.ErrQuota.Error()},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "capture-limit", Version: "1"}, nil)
			mcp.AddTool(server, &mcp.Tool{Name: "emit", Description: "Emit notifications"}, func(ctx context.Context, request *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
				for _, message := range []string{"first-complete-notification", strings.Repeat("x", test.size)} {
					if err := request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: "limit", Progress: 1, Message: message}); err != nil {
						return nil, nil, err
					}
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "sent"}}}, nil, nil
			})
			endpoint := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil))
			t.Cleanup(endpoint.Close)
			config := nativeConfig(t)
			config.OutputLimit = test.limit
			engine := openNative(t, config)
			request := nativeRequest(t, "limited-sse", "mcp_connect", native.MCPArguments{MCPRemote: execprotocol.MCPRemote{Transport: "sse", URL: endpoint.URL}})
			if _, err := engine.Submit(request); err != nil {
				t.Fatal(err)
			}
			nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.MCP != nil })
			if _, err := engine.Submit(nativeRequest(t, "emit", "mcp_call", native.MCPArguments{ConnectionID: request.ID, Name: "emit"})); err != nil {
				t.Fatal(err)
			}
			result := nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.State.Terminal() })
			if result.State != execprotocol.Failed || result.Error != test.errorText || !strings.Contains(result.Text(), "first-complete-notification") {
				t.Fatal(result.State, result.Error, result.Text())
			}
			for _, line := range strings.Split(strings.TrimSpace(result.Text()), "\n") {
				if !json.Valid([]byte(line)) {
					t.Fatal("partial notification journal", line)
				}
			}
		})
	}
}
