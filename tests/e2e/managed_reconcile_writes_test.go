//go:build postgres

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution/native"
)

func TestManagedReconcilePersistsOnlyLifecycleChanges(t *testing.T) {
	f, backend, environment := hostedFixture(t)
	ctx := context.Background()
	type row struct {
		xmin, ctid, failure  string
		running, provisioned bool
		activity             time.Time
	}
	read := func() row {
		t.Helper()
		var value row
		if err := f.pool.QueryRow(ctx, `SELECT xmin::text,ctid::text,running,provisioned,last_error,last_activity FROM execution.managed_environments WHERE environment_id=$1`, environment.ID).Scan(&value.xmin, &value.ctid, &value.running, &value.provisioned, &value.failure, &value.activity); err != nil {
			t.Fatal(err)
		}
		return value
	}
	stable := func(want row, wantError bool) {
		t.Helper()
		for range 2 {
			err := f.execution.Managed.Reconcile(ctx)
			if (err != nil) != wantError {
				t.Fatal("unexpected reconciliation result", err)
			}
			if got := read(); got != want {
				t.Fatalf("unchanged lifecycle rewrote its durable row: before=%+v after=%+v", want, got)
			}
		}
	}
	stable(read(), false)
	if backend.starts != 0 {
		t.Fatal("idle reconciliation started an executor")
	}
	request := nativeRequest(t, "reconcile-write-boundary", "exec_command", native.CommandArguments{Command: "printf once"})
	request.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, environment.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	backend.fail = true
	if err := f.execution.Managed.Reconcile(ctx); err == nil {
		t.Fatal("start failure was hidden")
	}
	failed := read()
	if failed.running || failed.provisioned || failed.failure == "" {
		t.Fatal("start failure was not persisted", failed)
	}
	stable(failed, true)
	backend.fail = false
	if err := f.execution.Managed.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	running := read()
	if !running.running || !running.provisioned || running.failure != "" || running.xmin == failed.xmin {
		t.Fatal("recovery did not persist running/provisioned or clear the error", running)
	}
	starts := backend.starts
	stable(running, false)
	if backend.starts != starts+2 {
		t.Fatal("unchanged database state skipped backend reconciliation", starts, backend.starts)
	}
	if err := f.execution.Cancel(ctx, f.actor, f.tenant, f.agent.ID, environment.ID, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.managed_environments SET last_activity=clock_timestamp()-interval '1 hour' WHERE environment_id=$1`, environment.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Managed.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	stopped := read()
	if stopped.running || !stopped.provisioned || stopped.failure != "" || backend.stops != 1 {
		t.Fatal("idle stop lost the provisioned identity", stopped, backend.stops)
	}
	stable(stopped, false)
	if backend.stops != 1 {
		t.Fatal("stopped environment was stopped again", backend.stops)
	}
}
