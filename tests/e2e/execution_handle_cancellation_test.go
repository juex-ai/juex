//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestExecutionHandleAdmissionSerializesWithCancellation(t *testing.T) {
	for _, kind := range []string{"write_stdin", "mcp_call", "mcp_list", "write_chunk", "write_commit", "write_abort"} {
		for _, commit := range []bool{true, false} {
			name := kind + "/rollback"
			if commit {
				name = kind + "/commit"
			}
			t.Run(name, func(t *testing.T) {
				f := executionDatabase(t)
				if execprotocol.IsChunkedWrite(kind) {
					configureChunkedWrite(t, f, true)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				device, _ := f.pairDevice(t, execprotocol.MCP)
				original := execprotocol.Request{Version: execprotocol.Version, ID: "original", AgentID: f.agent.ID, Kind: "mcp_connect", Arguments: json.RawMessage(`{"command":"fixture"}`)}
				request := execprotocol.Request{Version: execprotocol.Version, ID: "dependent", AgentID: f.agent.ID, Kind: kind, Arguments: json.RawMessage(`{"connection_id":"original"}`)}
				if kind == "write_stdin" {
					original.Kind = "exec_command"
					request.Arguments = json.RawMessage(`{"operation_id":"original","chars":"input"}`)
				} else if execprotocol.IsChunkedWrite(kind) {
					original.Kind = "write_begin"
					original.Arguments = json.RawMessage(`{"path":"file","mode":"create"}`)
					original.WriteContext = &execprotocol.WriteContext{ThreadID: "thread", ResetID: "reset"}
					request.WriteContext = original.WriteContext
					request.Arguments = json.RawMessage(`{"write_id":"original"}`)
				}
				if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); !errors.Is(err, execprotocol.ErrNotFound) {
					t.Fatal("missing retained handle", err)
				}
				if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, original, 0); err != nil {
					t.Fatal(err)
				}
				tx, err := f.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				var writer int32
				if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM execution.environments WHERE id=$1 FOR UPDATE`, device.ID).Scan(&writer); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `UPDATE execution.operations SET cancel_requested=true WHERE environment_id=$1 AND id=$2`, device.ID, original.ID); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					_, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0)
					done <- err
				}()
				for {
					select {
					case err := <-done:
						t.Fatal("admission bypassed the uncommitted cancellation", err)
					default:
					}
					var waiting bool
					if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, writer).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if commit {
					err = tx.Commit(ctx)
				} else {
					err = tx.Rollback(ctx)
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					if commit && !errors.Is(err, execprotocol.ErrDenied) || !commit && err != nil {
						t.Fatal("admission ignored the committed handle state", err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				_, err = f.executionStore.Operation(ctx, device.ID, request.ID, 0, 1)
				if commit && !errors.Is(err, execprotocol.ErrNotFound) || !commit && err != nil {
					t.Fatal("unexpected admitted operation", err)
				}
				if !commit {
					if err := f.executionStore.CancelOperation(ctx, device.ID, original.ID); err != nil {
						t.Fatal(err)
					}
					if op, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil || op.ID != request.ID {
						t.Fatal("retry lost the previously admitted operation", op, err)
					}
					changed := request
					changed.Arguments = json.RawMessage(`{"changed":true}`)
					if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, changed, 0); !errors.Is(err, execprotocol.ErrConflict) {
						t.Fatal("retry replaced the previously admitted operation", err)
					}
					request.ID = "new-after-cancel"
					if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); !errors.Is(err, execprotocol.ErrDenied) {
						t.Fatal("new request used the canceled handle", err)
					}
				}
			})
		}
	}
}
