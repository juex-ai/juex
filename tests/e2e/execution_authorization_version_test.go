//go:build postgres

package e2e

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestExecutionFrozenDeviceGrantVersionCannotRevive(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	request := nativeRequest(t, "frozen-admission", "exec_command", native.CommandArguments{Command: "printf forbidden"})
	request.AgentID, request.AuthorizationVersion = f.agent.ID, device.Version
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	changed, err := f.executionStore.SetGrants(ctx, device.ID, device.Version, map[string][]execprotocol.Capability{}, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := f.executionStore.SetGrants(ctx, device.ID, changed.Version, device.Grants, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	request.ID = "delayed-original-submit"
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("old device authority revived", err)
	}
	request.ID = "frozen-admission"
	connected, err := f.executionStore.Connect(ctx, restored.ID, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("dispatch crossed authorization change", err)
	}
	request.ID, request.AuthorizationVersion = "fresh-authorized-submit", restored.Version
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	if op, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil || op.State != "dispatched" {
		t.Fatal(op.State, err)
	}
	// The version is part of operation identity, not a replaceable retry hint.
	request.AuthorizationVersion = 0
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("retry replaced frozen authority", err)
	}
}
