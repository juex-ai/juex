//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedProcessEnvironmentLayersPrivateDispatchAndRevocation(t *testing.T) {
	ctx := context.Background()
	f := executionDatabase(t)
	f.execution.ProcessEnvironments = f.execution.Authority.(execution.ProcessEnvironmentResolver)
	device, token := f.pairDevice(t)
	_, origin := extensionManagement(t, f)
	base := origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID + "/process-environment"
	read := func() management.ProcessEnvironmentView {
		return managementCall[management.ProcessEnvironmentView](t, f.client, "GET", base, origin, nil, 200)
	}
	set := func(layer string, values map[string]string, remove ...string) management.ProcessEnvironmentView {
		t.Helper()
		v := read()
		c := management.ProcessEnvironmentChange{Version: v.Layers[layer].Version, Set: values, Remove: remove}
		if layer == "workspace" {
			c.EnvironmentID = device.ID
			c.WorkingDirectory = "/Users/test"
		}
		return managementCall[management.ProcessEnvironmentView](t, f.client, "PUT", base+"/"+layer, origin, c, 200)
	}
	set("tenant", map[string]string{"TOKEN": "tenant-private-value", "TENANT_ONLY": "shared"})
	set("fleet", map[string]string{"TOKEN": "fleet-private-value"})
	set("workspace", map[string]string{"TOKEN": "workspace-private-value"})
	set("agent", map[string]string{"TOKEN": "agent-private-value"})
	scope := func() management.ModelCallScope {
		a, err := f.directory.AuthorizeAgent(ctx, f.actor, f.tenant, f.agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		return modelCallScope(a)
	}
	resolve := func(s management.ModelCallScope, env, dir string) map[string]string {
		t.Helper()
		v, err := f.directory.ResolveProcessEnvironment(ctx, management.ProcessEnvironmentAccess{Scope: s, EnvironmentID: env, WorkingDirectory: dir})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	old := scope()
	if resolve(old, device.ID, "/Users/test/sub")["TOKEN"] != "agent-private-value" {
		t.Fatal("Agent override lost")
	}
	before := old.AgentExecutionEpoch
	set("fleet", map[string]string{"TOKEN": "changed-hidden-value"})
	if scope().AgentExecutionEpoch != before {
		t.Fatal("fully shadowed lower value revoked Agent")
	}
	set("agent", nil, "TOKEN")
	fresh := scope()
	if fresh.AgentExecutionEpoch <= before {
		t.Fatal("restored inherited secret did not revoke")
	}
	if resolve(fresh, device.ID, "/Users/test/sub")["TOKEN"] != "workspace-private-value" || resolve(fresh, device.ID, "/Users/testing")["TOKEN"] != "changed-hidden-value" || resolve(fresh, "other-device", "/Users/test")["TOKEN"] != "changed-hidden-value" {
		t.Fatal("Workspace credential crossed its binding")
	}
	if _, err := f.directory.ResolveProcessEnvironment(ctx, management.ProcessEnvironmentAccess{Scope: old}); !errors.Is(err, management.ErrDenied) {
		t.Fatal("old scope received new values", err)
	}
	view := read()
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "private-value") || strings.Contains(string(raw), "changed-hidden-value") {
		t.Fatal("public metadata exposed private values")
	}
	managementCall[any](t, f.client, "PUT", base+"/agent", origin, management.ProcessEnvironmentChange{Version: 0, Set: map[string]string{"TOKEN": "stale"}}, http.StatusConflict)
	// Freeze an offline operation, then change defaults. It must never receive
	// the replacement value when its device connects later.
	request := nativeRequest(t, "stale-env-command", "exec_command", map[string]any{"command": "printf should-not-run > stale-marker"})
	request.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	set("agent", map[string]string{"TOKEN": "dispatch-private-value", "OVERRIDE": "default"})
	dir, state := t.TempDir(), filepath.Join(t.TempDir(), "state")
	engine := openNative(t, native.Config{StateDirectory: state, EnvironmentID: device.ID, WorkingDirectory: dir, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	hooksEventually(t, func() bool {
		op, err := f.execution.Operation(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID, 0, 4096)
		return err == nil && op.State == "cancelled"
	})
	if _, err := os.Stat(filepath.Join(dir, "stale-marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("revoked command executed")
	}
	live := nativeRequest(t, "private-env-command", "exec_command", map[string]any{"command": "test -n \"$TOKEN\" && printf '%s' \"$OVERRIDE\"", "environment": map[string]string{"OVERRIDE": "explicit-operation"}})
	live.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, live, 0); err != nil {
		t.Fatal(err)
	}
	var op execution.Operation
	hooksEventually(t, func() bool {
		var err error
		op, err = f.execution.Operation(ctx, f.actor, f.tenant, f.agent.ID, device.ID, live.ID, 0, 4096)
		return err == nil && op.State == "completed"
	})
	if string(op.Snapshot.Output) != "explicit-operation" {
		t.Fatal("defaults or explicit override unavailable", op.State)
	}
	encoded, _ := json.Marshal(op)
	if strings.Contains(string(encoded), "dispatch-private-value") {
		t.Fatal("public operation contains secret")
	}
	err := filepath.WalkDir(state, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "dispatch-private-value") {
				t.Fatal("native journal persisted secret")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var dbPlain string
	if err := f.pool.QueryRow(ctx, `SELECT request::text FROM execution.operations WHERE id=$1`, live.ID).Scan(&dbPlain); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dbPlain, "dispatch-private-value") {
		t.Fatal("Execution journal persisted secret")
	}
}

func TestWorkspacePrivateEnvironmentCanBeRemovedAfterDeviceRevocation(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	_, origin := extensionManagement(t, f)
	base := origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID + "/process-environment/workspace"
	view := managementCall[management.ProcessEnvironmentView](t, f.client, "PUT", base, origin, management.ProcessEnvironmentChange{Set: map[string]string{"ONE": "private-one", "TWO": "private-two"}, EnvironmentID: device.ID, WorkingDirectory: "/Users/test"}, 200)
	if err := f.execution.Revoke(ctx, f.actor, f.tenant, device.ID); err != nil {
		t.Fatal(err)
	}
	change := management.ProcessEnvironmentChange{Version: view.Layers["workspace"].Version, Remove: []string{"ONE"}, EnvironmentID: uuid.NewString(), WorkingDirectory: "/another-root"}
	managementCall[any](t, f.client, "PUT", base, origin, change, 400)
	change.EnvironmentID, change.WorkingDirectory = device.ID, "/Users/test"
	view = managementCall[management.ProcessEnvironmentView](t, f.client, "PUT", base, origin, change, 200)
	layer := view.Layers["workspace"]
	if len(layer.Keys) != 1 || layer.Keys[0] != "TWO" || layer.EnvironmentID != device.ID || layer.WorkingDirectory != "/Users/test" {
		t.Fatal("remove rebound remaining values", layer)
	}
	view = managementCall[management.ProcessEnvironmentView](t, f.client, "PUT", base, origin, management.ProcessEnvironmentChange{Version: layer.Version, Remove: []string{"TWO"}}, 200)
	layer = view.Layers["workspace"]
	if len(layer.Keys) != 0 || layer.EnvironmentID != "" || layer.WorkingDirectory != "" {
		t.Fatal("private binding survived removal", layer)
	}
	managementCall[any](t, f.client, "PUT", base, origin, management.ProcessEnvironmentChange{Version: layer.Version, Set: map[string]string{"ONE": "replacement"}, EnvironmentID: device.ID, WorkingDirectory: "/Users/test"}, 403)
}
