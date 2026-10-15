//go:build postgres

package e2e

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func TestManagedRuntimeWorkActiveSeparatesLeaseLossFromCancellation(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "lease-watch", Text: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "runtime", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	config := pool.Config()
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	readPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer readPool.Close()
	reader := runtimepg.New(readPool)
	if active, err := reader.WorkActive(ctx, lease, work.TurnID); err != nil || !active {
		t.Fatal("active work requires a writable connection", active, err)
	}
	for _, change := range []struct {
		name string
		edit func(*managedruntime.Lease)
	}{
		{"holder", func(l *managedruntime.Lease) { l.Holder = "other" }},
		{"epoch", func(l *managedruntime.Lease) { l.Epoch++ }},
		{"missing-agent", func(l *managedruntime.Lease) { l.AgentID = "00000000-0000-4000-8000-000000000099" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			stale := lease
			change.edit(&stale)
			if active, err := reader.WorkActive(ctx, stale, work.TurnID); active || !errors.Is(err, managedruntime.ErrFence) {
				t.Fatal("lost authority reported as cancelled work", active, err)
			}
		})
	}
	if err := store.CancelThread(ctx, scope, main.ID); err != nil {
		t.Fatal(err)
	}
	if active, err := reader.WorkActive(ctx, lease, work.TurnID); active || err != nil {
		t.Fatal("explicit cancellation must remain inactive work", active, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime.agents SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, scope.AgentID); err != nil {
		t.Fatal(err)
	}
	if active, err := reader.WorkActive(ctx, lease, work.TurnID); active || !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("expired authority reported as cancelled work", active, err)
	}
}

type runtimeLeaseObservation struct {
	lease managedruntime.Lease
	turn  string
}

type delayedLeaseObservationStore struct {
	*runtimepg.Store
	first   atomic.Bool
	entered chan runtimeLeaseObservation
	read    chan struct{}
	sampled chan struct{}
	deliver chan struct{}
}

func (s *delayedLeaseObservationStore) WorkActive(ctx context.Context, lease managedruntime.Lease, turn string) (bool, error) {
	if !s.first.CompareAndSwap(false, true) {
		return s.Store.WorkActive(ctx, lease, turn)
	}
	s.entered <- runtimeLeaseObservation{lease: lease, turn: turn}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-s.read:
	}
	active, err := s.Store.WorkActive(ctx, lease, turn)
	close(s.sampled)
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-s.deliver:
	}
	return active, err
}

func TestManagedRuntimeInFlightRenewalPreservesAttemptRecovery(t *testing.T) {
	var calls atomic.Int32
	providerStarted := make(chan struct{})
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(providerStarted)
			<-r.Context().Done()
			return
		}
		streamManagedReply(w, "Recovered the original input")
	})
	store := &delayedLeaseObservationStore{Store: f.store, entered: make(chan runtimeLeaseObservation, 1), read: make(chan struct{}), sampled: make(chan struct{}), deliver: make(chan struct{})}
	runner, err := managedruntime.NewRunner(store, f.authority, managedruntime.RunnerConfig{Concurrency: 1, PollInterval: 10 * time.Millisecond, AuthorityInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Runtime did not stop")
		}
	})
	input := f.submit(t, "in-flight-renewal", f.main.ID, "Recover this input")
	select {
	case <-providerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	var observed runtimeLeaseObservation
	select {
	case observed = <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("lease watch did not start")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.agents SET lease_until=clock_timestamp()+interval '500 milliseconds' WHERE id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	renewal, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer renewal.Rollback(context.Background())
	updated, err := renewal.Exec(ctx, `UPDATE runtime.agents SET lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1 AND holder=$2 AND epoch=$3 AND lease_until>clock_timestamp()`, observed.lease.AgentID, observed.lease.Holder, observed.lease.Epoch)
	if err != nil || updated.RowsAffected() != 1 {
		t.Fatal("renewal was not admitted before the original deadline", updated, err)
	}
	runtimeEventually(t, func() bool {
		var expired bool
		if err := f.pool.QueryRow(ctx, `SELECT lease_until<=clock_timestamp() FROM runtime.agents WHERE id=$1`, f.agent.ID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		return expired
	})
	close(store.read)
	select {
	case <-store.sampled:
	case <-time.After(time.Second):
		t.Fatal("lease watch blocked on the uncommitted renewal")
	}
	// Deliver the old lease observation only after its same-epoch renewal is
	// visible, so a later write fence would succeed. This reproduces a delayed
	// renewal commit without changing durability or waiting for physical I/O.
	if err := renewal.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	close(store.deliver)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" })
	var turnState string
	var inputs, turns, unknown, completed int
	err = f.pool.QueryRow(ctx, `SELECT t.state,(SELECT count(*) FROM runtime.inputs WHERE thread_id=$1),(SELECT count(*) FROM runtime.turns WHERE input_id=$2),(SELECT count(*) FROM runtime.attempts WHERE turn_id=t.id AND state='unknown'),(SELECT count(*) FROM runtime.attempts WHERE turn_id=t.id AND state='completed') FROM runtime.turns t WHERE t.id=$3`, f.main.ID, input.ID, observed.turn).Scan(&turnState, &inputs, &turns, &unknown, &completed)
	if err != nil || turnState != "completed" || inputs != 1 || turns != 1 || unknown != 1 || completed != 1 || calls.Load() != 2 {
		t.Fatal("lease observation cancelled or replayed durable work", turnState, inputs, turns, unknown, completed, calls.Load(), err)
	}
}
