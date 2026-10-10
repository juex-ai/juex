//go:build postgres

package e2e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/management"
)

func holdManagedLifecycle(t *testing.T, f *executionFixture, id string) func() {
	t.Helper()
	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.executionStore.LockManaged(context.Background(), id, func(r execution.ManagedResource) (execution.ManagedResult, error) {
			close(locked)
			<-release
			return execution.ManagedResult{Running: false, Provisioned: r.Provisioned}, nil
		})
	}()
	<-locked
	return sync.OnceFunc(func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}

func waitManagedDispatchLock(t *testing.T, f *executionFixture) {
	t.Helper()
	runtimeEventually(t, func() bool {
		var waiting bool
		err := f.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%juex.execution.lifecycle.%')`).Scan(&waiting)
		return err == nil && waiting
	})
}

func TestManagedListingDoesNotWaitForLifecycleOrBlockOtherAgents(t *testing.T) {
	f, _, environment := hostedFixture(t)
	ctx := context.Background()
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Independent allocation"})
	if err != nil {
		t.Fatal(err)
	}
	release := holdManagedLifecycle(t, f, environment.ID)
	defer release()
	result := make(chan error, 1)
	go func() {
		read, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		envs, err := f.execution.Environments(read, f.actor, f.tenant, f.agent.ID)
		if err == nil && (len(envs) != 1 || envs[0].ID != environment.ID) {
			err = execprotocol.ErrInvalid
		}
		result <- err
	}()
	// Allow the first request to acquire its provisioning locks before testing
	// a different Agent. The lifecycle itself stays blocked until both return.
	time.Sleep(100 * time.Millisecond)
	read, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if envs, err := f.execution.Environments(read, f.actor, f.tenant, other.ID); err != nil || len(envs) != 1 || envs[0].ID == environment.ID {
		t.Error("one Agent's lifecycle blocked another allocation", envs, err)
	}
	if err := <-result; err != nil {
		t.Error("listing existing environment waited for lifecycle", err)
	}
}

func TestManagedWaitingDispatchRechecksConnectionAndCancellation(t *testing.T) {
	for _, action := range []string{"reconnect", "cancel"} {
		t.Run(action, func(t *testing.T) {
			f, _, environment := hostedFixture(t)
			ctx := context.Background()
			if _, err := f.pool.Exec(ctx, `UPDATE execution.managed_environments SET last_activity=clock_timestamp()-interval '1 hour' WHERE environment_id=$1`, environment.ID); err != nil {
				t.Fatal(err)
			}
			release := holdManagedLifecycle(t, f, environment.ID)
			defer release()
			request := nativeRequest(t, "work-during-stop", "exec_command", native.CommandArguments{Command: "printf once"})
			request.AgentID = f.agent.ID
			if _, err := f.execution.Submit(ctx, f.actor, f.tenant, environment.ID, request, 0); err != nil {
				t.Fatal(err)
			}
			var activity time.Time
			if err := f.pool.QueryRow(ctx, `SELECT last_activity FROM execution.managed_environments WHERE environment_id=$1`, environment.ID).Scan(&activity); err != nil {
				t.Fatal(err)
			}
			dispatched := make(chan error, 1)
			go func() {
				_, err := f.executionStore.Dispatch(ctx, environment.ID, 0, request.ID)
				dispatched <- err
			}()
			waitManagedDispatchLock(t, f)
			wantErr, wantState := execprotocol.ErrConflict, "waiting"
			if action == "reconnect" {
				if _, err := f.executionStore.Connect(ctx, environment.ID, "original-durable-journal-identity"); err != nil {
					t.Fatal(err)
				}
			} else {
				wantErr, wantState = execprotocol.ErrDenied, "cancelled"
				if err := f.execution.Cancel(ctx, f.actor, f.tenant, f.agent.ID, environment.ID, request.ID); err != nil {
					t.Fatal(err)
				}
			}
			release()
			if err := <-dispatched; !errors.Is(err, wantErr) {
				t.Fatal("stale dispatch was not fenced", err)
			}
			op, err := f.executionStore.Operation(ctx, environment.ID, request.ID, 0, 100)
			if err != nil || op.State != wantState {
				t.Fatal("stop changed durable work", op.State, err)
			}
			var retained time.Time
			if err := f.pool.QueryRow(ctx, `SELECT last_activity FROM execution.managed_environments WHERE environment_id=$1`, environment.ID).Scan(&retained); err != nil || retained.Before(activity) {
				t.Fatal("lifecycle lost new activity", retained, activity, err)
			}
		})
	}
}

func TestManagedAdmissionRPCQueuesBeforeLifecycleStopCompletes(t *testing.T) {
	f, _, environment := hostedFixture(t)
	listener := platformListener(t)
	server, err := serverrpc.NewExecution(listener, platformrpc.CredentialsAt(f.credentials, "execution"), f.execution, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := executionrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	release := holdManagedLifecycle(t, f, environment.ID)
	defer release()
	request := nativeRequest(t, "wake-during-graceful-stop", "exec_command", native.CommandArguments{Command: "printf once"})
	request.AgentID = f.agent.ID
	result := make(chan error, 1)
	go func() {
		_, err := client.Submit(context.Background(), f.actor, f.tenant, environment.ID, request, 0)
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal("managed stop lost admission reply", err)
		}
	case <-time.After(time.Second):
		t.Fatal("admission waited for managed stop")
	}
	op, err := f.executionStore.Operation(context.Background(), environment.ID, request.ID, 0, 100)
	if err != nil || op.State != "waiting" {
		t.Fatal("work did not remain queued for the next wake", op.State, err)
	}
	if _, err := client.Submit(context.Background(), f.actor, f.tenant, environment.ID, request, 0); err != nil {
		t.Fatal("same request could not be recovered during stop", err)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM execution.operations WHERE environment_id=$1 AND id=$2`, environment.ID, request.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("admission retry duplicated work", count, err)
	}
}

func TestManagedPurgePreservesReceiptLockOrder(t *testing.T) {
	f, _, environment := hostedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx, `UPDATE execution.managed_environments SET purging=true,purge_data=true WHERE environment_id=$1`, environment.ID); err != nil {
		t.Fatal(err)
	}
	started, finish := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.executionStore.LockManaged(ctx, environment.ID, func(execution.ManagedResource) (execution.ManagedResult, error) {
			close(started)
			<-finish
			return execution.ManagedResult{Purged: true}, nil
		})
	}()
	<-started
	receipt, err := f.pool.Begin(ctx)
	if err != nil {
		close(finish)
		<-done
		t.Fatal(err)
	}
	defer func() { _ = receipt.Rollback(context.Background()) }()
	var pid int
	if err := receipt.QueryRow(ctx, `SELECT pg_backend_pid() FROM execution.environments WHERE id=$1 FOR UPDATE`, environment.ID).Scan(&pid); err != nil {
		close(finish)
		<-done
		t.Fatal(err)
	}
	close(finish)
	// Wait until final purge needs the environment row held by a receipt.
	// The receipt's activity trigger must still be able to lock the managed row.
	runtimeEventually(t, func() bool {
		var waiting bool
		err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting)
		return err == nil && waiting
	})
	if _, err := receipt.Exec(ctx, `UPDATE execution.managed_environments SET last_activity=clock_timestamp() WHERE environment_id=$1`, environment.ID); err != nil {
		t.Error("purge reversed the receipt's environment/activity lock order", err)
		_ = receipt.Rollback(context.Background())
	} else if err := receipt.Commit(ctx); err != nil {
		t.Error(err)
	}
	if err := <-done; err != nil {
		t.Fatal("purge could not finish after receipt", err)
	}
}
