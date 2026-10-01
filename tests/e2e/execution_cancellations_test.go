//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

type delayedAdmissionAuthority struct {
	execution.Authority
	entered chan struct{}
	release chan struct{}
	delayed atomic.Bool
}

func (a *delayedAdmissionAuthority) Agent(ctx context.Context, actor, tenant, agent string, execute bool) (execution.Scope, error) {
	scope, err := a.Authority.Agent(ctx, actor, tenant, agent, execute)
	if err == nil && execute && a.delayed.CompareAndSwap(false, true) {
		close(a.entered)
		<-a.release
	}
	return scope, err
}

func TestExecutionPreparedCancellationWinsAgainstTimedOutRPCAdmission(t *testing.T) {
	for _, file := range []bool{false, true} {
		name := "operation"
		if file {
			name = "transfer"
		}
		t.Run(name, func(t *testing.T) {
			f := transferFixture(t)
			ctx := context.Background()
			device, _ := f.pairDevice(t)
			call := llm.Block{Type: llm.BlockToolUse, ToolUseID: "prepared-call", ToolName: "write", Input: map[string]any{"environment_id": device.ID, "path": "must-not-exist", "content": "late write"}}
			if file {
				call.ToolName, call.Input = "publish_file", map[string]any{"source": map[string]any{"environment_id": device.ID, "path": "source.bin"}}
			}
			scope, job := prepareRuntimeCall(t, f.managedRuntimeFixture, f.main.ID, "prepared-input", call)
			request := execprotocol.Request{Version: execprotocol.Version, ID: job.ID, AgentID: f.agent.ID, Kind: "write", Arguments: json.RawMessage(`{"path":"must-not-exist","content":"late write"}`)}
			if file {
				request.Kind, request.Arguments = "publish_file", json.RawMessage(`{}`)
			}
			if err := f.store.PrepareTool(ctx, job, device.ID, request); err != nil {
				t.Fatal(err)
			}
			authority := &delayedAdmissionAuthority{Authority: f.execution.Authority, entered: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			defer release.Do(func() { close(authority.release) })
			f.execution.Authority = authority
			gateway := runtimeExecutionGateway(t, f)
			admission := make(chan error, 1)
			rpcCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			defer cancel()
			go func() {
				if file {
					_, err := gateway.StartFileTransfer(rpcCtx, scope, job.ID, managedruntime.FileTransferSpec{Source: &managedruntime.FileLocation{EnvironmentID: device.ID, AuthorizationVersion: device.Version, Path: "source.bin"}, Name: "source.bin", MediaType: "application/octet-stream", Visibility: "agent"})
					admission <- err
				} else {
					_, err := gateway.Submit(rpcCtx, scope, device.ID, request)
					admission <- err
				}
			}()
			select {
			case <-authority.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("RPC admission did not reach the barrier")
			}
			select {
			case err := <-admission:
				if err == nil {
					t.Fatal("blocked admission did not time out")
				}
			case <-time.After(12 * time.Second):
				t.Fatal("RPC timeout did not fire")
			}
			// The client has timed out, but the server is still inside admission.
			if err := f.service.Cancel(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE runtime.tools SET lease_until='-infinity' WHERE id=$1`, job.ID); err != nil {
				t.Fatal(err)
			}
			runRuntimeTools(t, f, gateway)
			runtimeEventually(t, func() bool {
				var settled bool
				return f.pool.QueryRow(ctx, `SELECT state='cancelled' AND NOT operation_live FROM runtime.tools WHERE id=$1`, job.ID).Scan(&settled) == nil && settled
			})
			release.Do(func() { close(authority.release) })
			// Reconstruct the repository as a restarted process would. The late
			// request must leave only a terminal fact, never dispatchable work.
			restarted := executionpg.New(f.pool)
			runtimeEventually(t, func() bool {
				if file {
					transfer, err := restarted.Transfer(ctx, execution.TransferID(f.agent.ID, job.ID))
					return err == nil && transfer.State == execprotocol.Cancelled && transfer.CancelRequested
				}
				op, err := restarted.Operation(ctx, device.ID, job.ID, 0, 1)
				return err == nil && op.State == "cancelled" && op.Acknowledged && op.CancelRequested
			})
			pending, err := restarted.Pending(ctx, device.ID, 100)
			if err != nil || len(pending) != 0 {
				t.Fatal("late operation can reach Native", pending, err)
			}
			var count int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.cancellations`).Scan(&count); err != nil || count != 1 {
				t.Fatal("cancellation responsibility not durable", count, err)
			}
		})
	}
}

func TestExecutionPreparedCancellationIsScopedAndIdempotent(t *testing.T) {
	f := transferFixture(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	request := execprotocol.Request{Version: execprotocol.Version, ID: uuid.NewString(), AgentID: f.agent.ID, Kind: "read", Arguments: json.RawMessage(`{"path":"safe"}`)}
	other := scope
	other.AgentID = uuid.NewString()
	if _, err := f.executionStore.CancelPreparedOperation(ctx, other, device.ID, request.ID); err != nil {
		t.Fatal(err)
	}
	operation, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, time.Hour)
	if err != nil || operation.State != "waiting" {
		t.Fatal("another Agent pre-cancelled this operation", operation, err)
	}
	if _, err := f.executionStore.CancelPreparedOperation(ctx, other, device.ID, request.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("another Agent cancelled admitted operation", err)
	}
	for range 2 {
		if _, err := f.execution.CancelPreparedOperation(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID); err != nil {
			t.Fatal(err)
		}
	}
	operation, err = f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, time.Hour)
	if err != nil || operation.State != "cancelled" {
		t.Fatal("cancel retry revived the operation", operation, err)
	}
	other.TenantID = uuid.NewString()
	if _, err := f.executionStore.CancelPreparedOperation(ctx, other, device.ID, uuid.NewString()); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("cross-tenant cancellation admitted", err)
	}
	transferRequest := execution.TransferRequest{RequestID: uuid.NewString(), Source: &execution.FileLocation{EnvironmentID: device.ID, AuthorizationVersion: device.Version, Path: "source.bin"}, Name: "source.bin", MediaType: "application/octet-stream", Visibility: "agent"}
	for range 2 {
		if _, err := f.execution.CancelPreparedTransfer(ctx, f.actor, f.tenant, f.agent.ID, transferRequest.RequestID); err != nil {
			t.Fatal(err)
		}
	}
	transfer, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, transferRequest)
	if err != nil || transfer.State != execprotocol.Cancelled {
		t.Fatal(transfer, err)
	}
	transferRequest.Name = "different.bin"
	if _, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, transferRequest); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("cancelled identity accepted a different request", err)
	}
	if _, err := f.execution.CancelPreparedTransfer(ctx, f.actor, f.tenant, f.agent.ID, transferRequest.RequestID); err != nil {
		t.Fatal("cannot retry cancellation after terminal admission", err)
	}
}

func TestExecutionPreparedCancellationReturnsActualUnsettledState(t *testing.T) {
	f := transferFixture(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	request := execprotocol.Request{Version: execprotocol.Version, ID: uuid.NewString(), AgentID: f.agent.ID, Kind: "read", Arguments: json.RawMessage(`{"path":"file"}`)}
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, time.Hour); err != nil {
		t.Fatal(err)
	}
	connected, err := f.executionStore.Connect(ctx, device.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	gateway := runtimeExecutionGateway(t, f)
	if state, err := gateway.CancelPrepared(ctx, scope, device.ID, request.ID); err != nil || state != "dispatched" {
		t.Fatal("RPC cancellation acknowledgment fabricated settlement", state, err)
	}
	operation, err := f.executionStore.Operation(ctx, device.ID, request.ID, 0, 1)
	if err != nil || !operation.CancelRequested || operation.State != "dispatched" {
		t.Fatal(operation, err)
	}
	if err := f.executionStore.Settle(ctx, device.ID, connected.ConnectionEpoch, request.ID, execprotocol.Unknown, "device lost original outcome"); err != nil {
		t.Fatal(err)
	}
	if state, err := gateway.CancelPrepared(ctx, scope, device.ID, request.ID); err != nil || state != execprotocol.Unknown {
		t.Fatal("unknown cancellation claimed success", state, err)
	}
}
