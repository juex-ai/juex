//go:build postgres

package e2e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type pendingStopBackend struct {
	*hostedBackendProbe
	started, finished chan struct{}
	stops             int
}

func (b *pendingStopBackend) Stop(ctx context.Context, _ execution.ManagedResource) error {
	b.stops++
	if b.stops == 1 {
		close(b.started)
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-b.finished:
		return nil
	default:
		// An accepted Docker stop outlives the client's request context.
		return context.DeadlineExceeded
	}
}

func TestManagedStopRemainsFencedAfterTimeoutAndRestart(t *testing.T) {
	f, probe, environment := hostedFixture(t)
	ctx := context.Background()
	backend := &pendingStopBackend{hostedBackendProbe: probe, started: make(chan struct{}), finished: make(chan struct{})}
	f.execution.Managed.Backend = backend
	if _, err := f.pool.Exec(ctx, `UPDATE execution.managed_environments SET running=true,provisioned=true,last_activity=clock_timestamp()-interval '1 hour' WHERE environment_id=$1`, environment.ID); err != nil {
		t.Fatal(err)
	}
	const journal = "original-durable-journal-identity"
	device, err := f.executionStore.Connect(ctx, environment.ID, journal)
	if err != nil {
		t.Fatal(err)
	}
	pass, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.execution.Managed.Reconcile(pass) }()
	select {
	case <-backend.started:
	case <-time.After(5 * time.Second):
		t.Fatal("idle stop did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("stop request did not return its cancellation", err)
	}
	request := nativeRequest(t, "wake-after-stop-timeout", "exec_command", native.CommandArguments{Command: "printf once"})
	request.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, environment.ID, request, 0); err != nil {
		t.Fatal("pending stop blocked durable admission", err)
	}
	assertWaiting := func(store *executionpg.Store, epoch int64) {
		t.Helper()
		if _, err := store.Dispatch(ctx, environment.ID, epoch, request.ID); !errors.Is(err, execprotocol.ErrUnavailable) {
			t.Fatal("work dispatched while the external stop was pending", err)
		}
		operation, err := store.Operation(ctx, environment.ID, request.ID, 0, 100)
		if err != nil || operation.State != "waiting" {
			t.Fatal("pending stop changed the admitted operation", operation.State, err)
		}
	}
	assertWaiting(f.executionStore, device.ConnectionEpoch)
	// Recovery must not depend on the old coordinator's memory or connection.
	store := executionpg.New(f.pool)
	manager := &execution.ManagedManager{Store: store, Backend: backend, Authority: f.execution.Authority, Key: make([]byte, 32)}
	reconnected, err := store.Connect(ctx, environment.ID, journal)
	if err != nil {
		t.Fatal(err)
	}
	assertWaiting(store, reconnected.ConnectionEpoch)
	if err := manager.Reconcile(ctx); !errors.Is(err, context.DeadlineExceeded) || backend.stops != 2 || backend.starts != 0 {
		t.Fatal("new work bypassed the original stop", err, backend.stops, backend.starts)
	}
	assertWaiting(store, reconnected.ConnectionEpoch)
	close(backend.finished)
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Dispatch(ctx, environment.ID, reconnected.ConnectionEpoch, request.ID); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("the stopped instance's connection remained authoritative", err)
	}
	if err := manager.Reconcile(ctx); err != nil || backend.starts != 1 {
		t.Fatal("waiting work did not restart the confirmed stopped environment", err, backend.starts)
	}
	var epoch int64
	if err := f.pool.QueryRow(ctx, `SELECT connection_epoch FROM execution.environments WHERE id=$1`, environment.ID).Scan(&epoch); err != nil || epoch != reconnected.ConnectionEpoch+1 {
		t.Fatal("stop completion did not fence exactly once", epoch, err)
	}
	fresh, err := store.Connect(ctx, environment.ID, journal)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := store.Dispatch(ctx, environment.ID, fresh.ConnectionEpoch, request.ID)
	if err != nil || operation.ID != request.ID || operation.State != "dispatched" {
		t.Fatal("original work did not dispatch after recovery", operation.State, err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations WHERE environment_id=$1`, environment.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("recovery duplicated admitted work", count, err)
	}
}

func TestManagedStopCompletionPreservesAdmissionLockOrder(t *testing.T) {
	f, _, environment := hostedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx, `UPDATE execution.managed_environments SET running=true,stopping=true WHERE environment_id=$1`, environment.ID); err != nil {
		t.Fatal(err)
	}
	started, finish := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.executionStore.LockManaged(ctx, environment.ID, func(r execution.ManagedResource) (execution.ManagedResult, error) {
			close(started)
			<-finish
			return execution.ManagedResult{Provisioned: r.Provisioned}, nil
		})
	}()
	<-started
	admission, err := f.pool.Begin(ctx)
	if err != nil {
		close(finish)
		<-done
		t.Fatal(err)
	}
	defer func() { _ = admission.Rollback(context.Background()) }()
	var pid int
	if err := admission.QueryRow(ctx, `SELECT pg_backend_pid() FROM execution.environments WHERE id=$1 FOR UPDATE`, environment.ID).Scan(&pid); err != nil {
		close(finish)
		<-done
		t.Fatal(err)
	}
	close(finish)
	runtimeEventually(t, func() bool {
		var waiting bool
		err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting)
		return err == nil && waiting
	})
	var activity time.Time
	err = admission.QueryRow(ctx, `UPDATE execution.managed_environments SET last_activity=clock_timestamp() WHERE environment_id=$1 RETURNING last_activity`, environment.ID).Scan(&activity)
	if err != nil {
		t.Error("stop completion reversed admission's environment/activity lock order", err)
		_ = admission.Rollback(context.Background())
	} else if err := admission.Commit(ctx); err != nil {
		t.Error(err)
	}
	if err := <-done; err != nil {
		t.Fatal("stop could not finish after admission", err)
	}
	var retained time.Time
	var stopping, running bool
	if err := f.pool.QueryRow(ctx, `SELECT last_activity,stopping,running FROM execution.managed_environments WHERE environment_id=$1`, environment.ID).Scan(&retained, &stopping, &running); err != nil || retained != activity || stopping || running {
		t.Fatal("stop completion lost new activity or retained its fence", err, retained, activity, stopping, running)
	}
}

func TestManagedStopMigrationPreservesExistingEnvironment(t *testing.T) {
	f, _, environment := hostedFixture(t)
	ctx := context.Background()
	// Reconstruct the deployed schema immediately before the additive stop fence.
	if _, err := f.pool.Exec(ctx, `ALTER TABLE execution.managed_environments DROP COLUMN stopping; DELETE FROM execution.schema_versions WHERE version=(SELECT max(version) FROM execution.schema_versions)`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := f.pool.QueryRow(ctx, `SELECT row_to_json(h)::text FROM execution.managed_environments h WHERE environment_id=$1`, environment.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := executionpg.Migrate(ctx, f.pool); err != nil {
			t.Fatal(err)
		}
	}
	var unchanged, stopping bool
	if err := f.pool.QueryRow(ctx, `SELECT to_jsonb(h)-'stopping'=$2::jsonb,stopping FROM execution.managed_environments h WHERE environment_id=$1`, environment.ID, before).Scan(&unchanged, &stopping); err != nil || !unchanged || stopping {
		t.Fatal("migration changed an existing environment or began an unsolicited stop", err, unchanged, stopping)
	}
}
