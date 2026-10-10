//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/migration"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/migration/legacy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagedMCPHTTPFromSelectedExtensionThroughRuntimeAndRPC(t *testing.T) {
	ctx := context.Background()
	var effects atomic.Int64
	var lists atomic.Int64
	server := mcp.NewServer(&mcp.Implementation{Name: "managed-http-mcp", Version: "1"}, nil)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				lists.Add(1)
			}
			return next(ctx, method, request)
		}
	})
	mcp.AddTool(server, &mcp.Tool{Name: "acceptance", Description: "Return acceptance marker"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		effects.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "real-http-protocol-accepted"}}}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer selected-extension-secret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)
	binding := uuid.NewString()
	var calls atomic.Int64
	var original atomic.Value
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		encoded, _ := json.Marshal(body)
		if strings.Contains(string(encoded), "selected-extension-secret") {
			t.Error("configured credentials entered model context")
		}
		switch calls.Add(1) {
		case 1:
			if !strings.Contains(string(encoded), `\"kind\":\"mcp\",\"resource_id\":\"remote\"`) {
				t.Errorf("missing unambiguous resource identity in model catalog: %s", encoded)
			}
			streamManagedTool(w, "extension_mcp_connect", map[string]any{"binding_id": binding, "resource_id": "remote"})
		case 3:
			streamManagedTool(w, "mcp_list", map[string]any{"handle": original.Load()})
		case 5:
			streamManagedTool(w, "mcp_call", map[string]any{"handle": original.Load(), "name": "acceptance", "arguments": map[string]any{}})
		default:
			streamManagedReply(w, "Complete")
		}
	})
	device, token := f.pairDevice(t, execprotocol.MCP)
	directory := t.TempDir()
	sourceDir := filepath.Join(t.TempDir(), "http-mcp")
	if err := os.Mkdir(sourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	mcpSource, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"remote": map[string]any{"type": "streamable-http", "url": endpoint.URL, "headers": map[string]string{"Authorization": "Bearer ${TOKEN}"}}}})
	for name, data := range map[string][]byte{"juex.extension.json": []byte(`{"manifest_version":1,"name":"http-mcp","version":"1.0.0"}`), "mcp.json": mcpSource} {
		if err := os.WriteFile(filepath.Join(sourceDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := legacy.ReadExtension(sourceDir, legacy.ExtensionResources{MCP: true})
	if err != nil {
		t.Fatal(err)
	}
	secret := "selected-extension-secret"
	manifest, err := migration.ConvertMCPExtension(snapshot, nil, map[string]*string{"TOKEN": &secret})
	if err != nil {
		t.Fatal(err)
	}
	writeExtensionManifest(t, directory, manifest)
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	adapter, origin := extensionManagement(t, f)
	base := origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID
	receipt := inspectManagedExtension(t, f, adapter, device.ID, directory)
	f.agent, err = adapter.Configure(ctx, f.actor, f.tenant, f.agent.ID, binding, management.ExtensionChange{Version: f.agent.Version, Enabled: true, EnvironmentID: device.ID, InspectionID: receipt.OperationID, Resources: []string{"mcp/remote"}})
	if err != nil {
		t.Fatal(err)
	}
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	f.submit(t, "connect-http", f.main.ID, "Connect configured MCP")
	hooksEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	var connection string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM runtime.tools WHERE request->>'kind'='mcp_connect'`).Scan(&connection); err != nil {
		t.Fatal(err)
	}
	original.Store(map[string]string{"environment_id": device.ID, "operation_id": connection})
	operation, err := f.execution.Operation(ctx, f.actor, f.tenant, f.agent.ID, device.ID, connection, 0, 64<<10)
	if err != nil || operation.Snapshot.MCP == nil || operation.Snapshot.MCP.ServerName != "managed-http-mcp" {
		t.Fatal(operation, err)
	}
	for _, stage := range []struct {
		id    string
		count int64
	}{{"list-http", 4}, {"call-http", 6}} {
		f.submit(t, stage.id, f.main.ID, stage.id)
		hooksEventually(t, func() bool { return calls.Load() == stage.count && f.timeline(t, f.main.ID).Thread.State == "idle" })
	}
	if effects.Load() != 1 {
		t.Fatal("HTTP call did not execute exactly once", effects.Load())
	}
	var result string
	if err := f.pool.QueryRow(ctx, `SELECT result->>'content' FROM runtime.tools WHERE request->>'kind'='mcp_call'`).Scan(&result); err != nil || !strings.Contains(result, "real-http-protocol-accepted") {
		t.Fatal(result, err)
	}
	hooksEventually(t, func() bool {
		var acked bool
		return f.pool.QueryRow(ctx, `SELECT confirmed_cursor=cursor AND cursor>0 FROM runtime.observation_sources WHERE operation_id=$1`, connection).Scan(&acked) == nil && acked
	})
	var operationCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations`).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		page := managementCall[execution.MCPPage](t, f.client, "GET", base+"/mcp-connections", origin, nil, 200)
		if len(page.Items) != 1 || page.Items[0].ID != connection || page.Items[0].State != "running" || !page.Items[0].CanRefresh || page.Items[0].BindingID != binding || page.Items[0].LatestTools == nil || len(page.Items[0].LatestTools.Tools) != 1 || page.Items[0].LatestTools.Tools[0].Name != "acceptance" {
			t.Fatal(page)
		}
		encoded, _ := json.Marshal(page)
		if strings.Contains(string(encoded), "selected-extension-secret") || strings.Contains(string(encoded), endpoint.URL) {
			t.Fatal("MCP inspection exposed transport credentials")
		}
	}
	var countAfter int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations`).Scan(&countAfter); err != nil || countAfter != operationCount || lists.Load() != 1 {
		t.Fatal("read created an operation or issued tools/list", operationCount, countAfter, lists.Load(), err)
	}
	change := execution.MCPRefresh{ID: uuid.NewString()}
	refreshURL := base + "/environments/" + device.ID + "/mcp-connections/" + connection + "/tools"
	for range 2 {
		managementCall[execution.MCPToolList](t, f.client, "POST", refreshURL, origin, change, 200)
	}
	hooksEventually(t, func() bool {
		page := managementCall[execution.MCPPage](t, f.client, "GET", base+"/mcp-connections", origin, nil, 200)
		return len(page.Items) == 1 && page.Items[0].LatestTools != nil && page.Items[0].LatestTools.ID == change.ID && page.Items[0].LatestTools.State == "completed" && len(page.Items[0].LatestTools.Tools) == 1
	})
	if lists.Load() != 2 {
		t.Fatal("duplicate refresh reached MCP", lists.Load())
	}
	outsider, err := f.directory.CreateUser(ctx, "mcp-outsider@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.InspectMCP(ctx, outsider.ID, f.tenant, f.agent.ID, ""); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("cross-owner inspection", err)
	}
	if _, err := f.execution.RefreshMCPTools(ctx, outsider.ID, f.tenant, f.agent.ID, device.ID, connection, execution.MCPRefresh{ID: uuid.NewString()}); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("cross-owner refresh", err)
	}
	configuration := f.agent.Configuration
	configuration.Modules = map[agentpolicy.Capability]bool{agentpolicy.MCP: false}
	if _, err := f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Configuration: &configuration}); err != nil {
		t.Fatal(err)
	}
	executionEventually(t, f, device.ID, connection, func(operation execution.Operation) bool { return operation.State == "cancelled" })
	page := managementCall[execution.MCPPage](t, f.client, "GET", base+"/mcp-connections", origin, nil, 200)
	if len(page.Items) != 1 || page.Items[0].State != "cancelled" || page.Items[0].CanRefresh || page.Items[0].Handshake == nil {
		t.Fatal("closed connection lost history or permits refresh", page)
	}
	managementCall[execution.MCPToolList](t, f.client, "POST", refreshURL, origin, execution.MCPRefresh{ID: uuid.NewString()}, 409)
}
