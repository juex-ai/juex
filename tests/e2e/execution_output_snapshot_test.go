//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func executionOutputFixture(t *testing.T) (*executionFixture, execution.Device, string) {
	t.Helper()
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	request := nativeRequest(t, "output-snapshot", "exec_command", native.CommandArguments{Command: "printf output"})
	request.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	connected, err := f.executionStore.Connect(ctx, device.ID, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: request.ID, AgentID: f.agent.ID, Kind: request.Kind, State: execprotocol.Running, Output: []byte{'a', 0, 255, 'b'}, NextCursor: 4, OutputBytes: 4}
	if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal(err)
	}
	return f, device, request.ID
}

func TestExecutionOutputHTTPReadsCommittedSnapshotDuringUpdate(t *testing.T) {
	f, device, id := executionOutputFixture(t)
	ctx := context.Background()
	listener := platformListener(t)
	service, err := serverrpc.NewExecution(listener, platformrpc.CredentialsAt(f.credentials, "execution"), f.execution, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, service)
	client, err := executionrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Execution: client, PublicURL: f.origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	dashboard := httptest.NewServer(handler)
	defer dashboard.Close()
	bounded := *f.client
	bounded.Timeout = 2 * time.Second
	url := dashboard.URL + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID + "/environments/" + device.ID + "/operations/" + id + "/output?after=1"
	read := func() managementhttp.OperationOutput {
		return managementCall[managementhttp.OperationOutput](t, &bounded, http.MethodGet, url, f.origin, nil, http.StatusOK)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	after := []byte{'a', 0, 255, 'b', 'n', 'e', 'x', 't'}
	if _, err := tx.Exec(ctx, `UPDATE execution.operations SET state='completed',output=$3,snapshot=snapshot||'{"state":"completed","next_cursor":8,"output_bytes":8}'::jsonb WHERE environment_id=$1 AND id=$2`, device.ID, id, after); err != nil {
		t.Fatal(err)
	}
	before := read()
	if before.State != "running" || before.OutputBytes != 4 || before.NextCursor != 4 || !bytes.Equal(before.Output, []byte{0, 255, 'b'}) {
		t.Fatal("reader mixed committed output and uncommitted metadata", before)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	completed := read()
	if completed.State != "completed" || completed.OutputBytes != 8 || completed.NextCursor != 8 || !bytes.Equal(completed.Output, after[1:]) {
		t.Fatal("reader did not advance to the committed snapshot", completed)
	}
}

func TestExecutionOutputSnapshotSupportsReadOnlyAndExpiredCursors(t *testing.T) {
	f, device, id := executionOutputFixture(t)
	ctx := context.Background()
	config := f.pool.Config()
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := executionpg.New(pool)
	op, err := store.Operation(ctx, device.ID, id, 1, 2)
	if err != nil || op.ResultCursor != 4 || op.Snapshot.NextCursor != 3 || !bytes.Equal(op.Snapshot.Output, []byte{0, 255}) {
		t.Fatal("reading a bounded output page required a writable transaction", op, err)
	}
	if _, err := store.Operation(ctx, device.ID, id, math.MaxInt64, 1); !errors.Is(err, execprotocol.ErrInvalid) {
		t.Fatal("unexpired output accepted a cursor past its committed length", err)
	}
	if _, err := store.Operation(ctx, device.ID, "missing", 0, 1); !errors.Is(err, execprotocol.ErrNotFound) {
		t.Fatal("missing operation", err)
	}
	end, err := store.Operation(ctx, device.ID, id, 4, 1)
	if err != nil || end.Snapshot.NextCursor != 4 || len(end.Snapshot.Output) != 0 {
		t.Fatal("reading at the end of output", end, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.operations SET output='',snapshot=jsonb_set(snapshot,'{output_expired}','true') WHERE environment_id=$1 AND id=$2`, device.ID, id); err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []int64{0, 4, math.MaxInt64} {
		op, err := store.Operation(ctx, device.ID, id, cursor, 1)
		if err != nil || !op.Snapshot.OutputExpired || len(op.Snapshot.Output) != 0 || op.Snapshot.NextCursor != cursor {
			t.Fatal("expired output lost its original consumer cursor", cursor, op, err)
		}
	}
}

func TestExecutionOutputSnapshotWaitsForPurge(t *testing.T) {
	f, device, id := executionOutputFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var writer int32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(hashtext('juex.execution.purge'))`).Scan(&writer); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE execution.operations SET purged=true,output='',snapshot=jsonb_set(snapshot,'{output_expired}','true') WHERE environment_id=$1 AND id=$2`, device.ID, id); err != nil {
		t.Fatal(err)
	}
	type result struct {
		operation execution.Operation
		err       error
	}
	done := make(chan result, 1)
	go func() {
		operation, err := f.executionStore.Operation(ctx, device.ID, id, 3, 1)
		done <- result{operation, err}
	}()
	// Observe the advisory-lock wait before releasing purge, rather than relying
	// on the reader goroutine being scheduled within a fixed sleep.
	for {
		select {
		case read := <-done:
			t.Fatal("output read bypassed the in-progress purge", read)
		default:
		}
		var waiting bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND $1=ANY(pg_blocking_pids(pid)))`, writer).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case read := <-done:
		operation := read.operation
		if read.err != nil || !operation.Purged || !operation.Snapshot.OutputExpired || operation.ResultCursor != 4 || operation.Snapshot.NextCursor != 3 || len(operation.Snapshot.Output) != 0 {
			t.Fatal("output read did not take a fresh snapshot after purge", operation, read.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
