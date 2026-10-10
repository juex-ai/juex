//go:build postgres

package e2e

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestExecutionMCPHandshakeRemainsImmutableAcrossObservations(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t, execprotocol.MCP)
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	request := nativeRequest(t, "mcp-metadata", "mcp_connect", native.MCPArguments{Command: "fixture"})
	request.AgentID = f.agent.ID
	if _, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Minute, false); err != nil {
		t.Fatal(err)
	}
	connected, err := f.executionStore.Connect(ctx, device.ID, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	metadata := execprotocol.MCPConnection{Transport: "stdio", ConnectedAt: time.Now().UTC(), ServerName: "first-handshake", ServerVersion: "1", ProtocolVersion: "2025-11-25"}
	snapshot := execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: request.ID, AgentID: f.agent.ID, Kind: request.Kind, State: execprotocol.Running, MCP: &metadata}
	if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal(err)
	}
	changed := metadata
	changed.ServerName = "replacement-server"
	snapshot.MCP = &changed
	if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("a different handshake overwrote the original operation", err)
	}
	snapshot.MCP = nil
	snapshot.State = execprotocol.Cancelled
	if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal("terminal observation without metadata", err)
	}
	operation, err := f.executionStore.Operation(ctx, device.ID, request.ID, 0, 100)
	if err != nil || operation.Snapshot.MCP == nil || operation.Snapshot.MCP.ServerName != metadata.ServerName || !operation.Snapshot.MCP.ConnectedAt.Equal(metadata.ConnectedAt) || operation.Snapshot.State != execprotocol.Cancelled {
		t.Fatal("lost original handshake", operation, err)
	}
}

func TestExecutionMCPInspectionPagesWithoutMutatingPresenceOrOperations(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t, execprotocol.MCP)
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	connected, err := f.executionStore.Connect(ctx, device.ID, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	for index := range 51 {
		request := nativeRequest(t, fmt.Sprintf("connection-%02d", index), "mcp_connect", native.MCPArguments{Command: "test", Environment: map[string]string{"PRIVATE": "not-in-inspection"}})
		request.AgentID = f.agent.ID
		if _, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Minute, false); err != nil {
			t.Fatal(err)
		}
		if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: request.ID, AgentID: f.agent.ID, Kind: request.Kind, State: execprotocol.Running, MCP: &execprotocol.MCPConnection{Transport: "stdio", ConnectedAt: time.Now().UTC(), ServerName: "original"}}); err != nil {
			t.Fatal(err)
		}
	}
	tables := []string{"execution.operations", "execution.environments", "execution.audit"}
	before := statusRows(t, f.pool, tables...)
	first, err := f.execution.InspectMCP(ctx, f.actor, f.tenant, f.agent.ID, "")
	if err != nil || len(first.Items) != 50 || first.Next == "" || !first.Items[0].CanRefresh {
		t.Fatal(first, err)
	}
	second, err := f.execution.InspectMCP(ctx, f.actor, f.tenant, f.agent.ID, first.Next)
	if err != nil || len(second.Items) != 1 || second.Next != "" || second.Items[0].ID == first.Items[49].ID {
		t.Fatal(second, err)
	}
	if !reflect.DeepEqual(before, statusRows(t, f.pool, tables...)) {
		t.Fatal("MCP inspection changed persistence")
	}
	if _, err := f.execution.InspectMCP(ctx, f.actor, f.tenant, f.agent.ID, "invalid"); !errors.Is(err, execprotocol.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.environments SET online_until=NULL WHERE id=$1`, device.ID); err != nil {
		t.Fatal(err)
	}
	page, err := f.execution.InspectMCP(ctx, f.actor, f.tenant, f.agent.ID, "")
	if err != nil || page.Items[0].State != "unconfirmed" || page.Items[0].CanRefresh || page.Items[0].Handshake == nil {
		t.Fatal(page, err)
	}
	if _, err := f.execution.RefreshMCPTools(ctx, f.actor, f.tenant, f.agent.ID, device.ID, page.Items[0].ID, execution.MCPRefresh{ID: "offline-refresh"}); !errors.Is(err, execprotocol.ErrUnavailable) {
		t.Fatal("offline refresh admitted", err)
	}
}

func TestExecutionMCPManagementRequiresHandshakeAndCurrentAuthorization(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t, execprotocol.MCP)
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	connected, err := f.executionStore.Connect(ctx, device.ID, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	request := nativeRequest(t, "handshake-guard", "mcp_connect", native.MCPArguments{Command: "test"})
	request.AgentID = f.agent.ID
	request.AuthorizationVersion = device.Version
	if _, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Minute, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: request.ID, AgentID: f.agent.ID, Kind: request.Kind, State: execprotocol.Running}
	if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal(err)
	}
	page, err := f.execution.InspectMCP(ctx, f.actor, f.tenant, f.agent.ID, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "connecting" || page.Items[0].CanRefresh {
		t.Fatal(page, err)
	}
	if _, err := f.execution.RefreshMCPTools(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID, execution.MCPRefresh{ID: "before-handshake"}); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal(err)
	}
	snapshot.MCP = &execprotocol.MCPConnection{Transport: "stdio", ConnectedAt: time.Now().UTC(), ServerName: "ready"}
	if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal(err)
	}
	refresh, err := f.execution.RefreshMCPTools(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID, execution.MCPRefresh{ID: "authorized-refresh"})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := f.executionStore.Operation(ctx, device.ID, refresh.ID, 0, 1)
	if err != nil || admitted.Request.AuthorizationVersion != device.Version {
		t.Fatal("refresh lost authorization fence", admitted.Request, err)
	}
	changed, err := f.executionStore.SetGrants(ctx, device.ID, device.Version, device.Grants, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	page, err = f.execution.InspectMCP(ctx, f.actor, f.tenant, f.agent.ID, "")
	if err != nil || page.Items[0].State != "unconfirmed" || page.Items[0].CanRefresh || page.Items[0].Handshake == nil {
		t.Fatal(page, err)
	}
	if _, err := f.execution.RefreshMCPTools(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID, execution.MCPRefresh{ID: "stale-refresh"}); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("stale connection admitted refresh", err)
	}
	// The request retains its checked version even if grants change between the service check and admission.
	raced := admitted.Request
	raced.ID = "raced-refresh"
	if _, err := f.executionStore.Enqueue(ctx, changed, scope, raced, time.Minute, false); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("refresh crossed authorization version", err)
	}
}
