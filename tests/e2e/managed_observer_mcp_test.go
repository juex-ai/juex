//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManualMCPObserverConnectsWithoutModelAndDeliversRealProtocolNotification(t *testing.T) {
	ctx := context.Background()
	var initialized atomic.Int64
	server := mcp.NewServer(&mcp.Implementation{Name: "manual-mcp", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "example"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "initialize" {
				initialized.Add(1)
			}
			result, err := next(ctx, method, request)
			if err == nil && method == "tools/list" {
				err = request.GetSession().(*mcp.ServerSession).NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: "manual", Progress: 1, Message: "manual-protocol-notification"})
			}
			return result, err
		}
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer manual-private" {
			http.Error(w, "unauthorized", 401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)
	f := executionDatabase(t)
	device, token := f.pairDevice(t, execprotocol.MCP)
	directory := t.TempDir()
	writeExtensionManifest(t, directory, extensionpolicy.Manifest{ManifestVersion: 2, Name: "manual-mcp", Version: "1", MCP: []extensionpolicy.MCPResource{{CommandResource: extensionpolicy.CommandResource{ID: "remote"}, MCPRemote: execprotocol.MCPRemote{Transport: "http", URL: endpoint.URL, Headers: map[string]string{"Authorization": "Bearer manual-private"}}}}})
	connectExecutionDevice(t, f, device, token, openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling}))
	adapter, _ := extensionManagement(t, f)
	receipt := inspectManagedExtension(t, f, adapter, device.ID, directory)
	binding := uuid.NewString()
	var err error
	f.agent, err = adapter.Configure(ctx, f.actor, f.tenant, f.agent.ID, binding, management.ExtensionChange{Version: f.agent.Version, Enabled: true, EnvironmentID: device.ID, InspectionID: receipt.OperationID, Resources: []string{"mcp/remote"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE management.models SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	gateway := runtimeExecutionGateway(t, f)
	f.service.Tools = gateway
	request := managedruntime.ObserverStart{RequestID: uuid.NewString(), ThreadID: f.main.ID, BindingID: binding, ResourceID: "remote", Kind: "mcp", AgentVersion: f.agent.Version, Revision: receipt.Catalog.Revision, Mode: "continuous", Subscribe: true}
	control := managementCall[managedruntime.ObserverControl](t, f.client, "POST", f.base+"/observers", f.origin, request, 200)
	runRuntimeTools(t, f, gateway)
	hooksEventually(t, func() bool {
		page, err := f.execution.InspectMCP(ctx, f.actor, f.tenant, f.agent.ID, "")
		return err == nil && len(page.Items) == 1 && page.Items[0].State == "running" && page.Items[0].Handshake != nil
	})
	if _, err = f.execution.RefreshMCPTools(ctx, f.actor, f.tenant, f.agent.ID, device.ID, control.SourceID, execution.MCPRefresh{ID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	hooksEventually(t, func() bool {
		events, err := f.service.ObservedEvents(ctx, f.actor, f.tenant, f.agent.ID, control.SourceID, "")
		if err != nil {
			return false
		}
		for _, event := range events.Items {
			if strings.Contains(string(event.Data), "manual-protocol-notification") && event.Delivered == 1 {
				return true
			}
		}
		return false
	})
	page := managementCall[managedruntime.ObservationPage](t, f.client, "GET", f.base+"/observation-sources", f.origin, nil, 200)
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "manual-private") || strings.Contains(string(encoded), endpoint.URL) {
		t.Fatal("private transport exposed in inspection")
	}
	var attempts int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.attempts`).Scan(&attempts); err != nil || attempts != 0 || initialized.Load() != 1 {
		t.Fatal("manual connection required provider or duplicated", attempts, initialized.Load(), err)
	}
	if err = f.service.StopObserver(ctx, f.actor, f.tenant, f.agent.ID, control.SourceID); err != nil {
		t.Fatal(err)
	}
	executionEventually(t, f, device.ID, control.SourceID, func(operation execution.Operation) bool { return operation.State == "cancelled" })
}
