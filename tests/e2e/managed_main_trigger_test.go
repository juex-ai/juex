//go:build postgres

package e2e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func mainTrigger() managedruntime.MainTrigger {
	return managedruntime.MainTrigger{ID: uuid.NewString(), Epoch: 1, Name: "Calendar wake", Content: "Use the existing Main conversation", ScheduledAt: time.Now().UTC().Truncate(time.Second)}
}

func TestManagedMainTriggerAdmissionOwnsOneInputAndDoesNotOwnMain(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	trigger := mainTrigger()
	ordinary, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "calendar/" + trigger.ID, ThreadID: main.ID, Text: "Existing unrelated conversation"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AdmitMainTrigger(ctx, scope, trigger)
	if err != nil || first.State != "accepted" || first.ThreadID != main.ID || first.InputID == ordinary.ID {
		t.Fatal(first, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := runtimepg.New(pool).AdmitMainTrigger(ctx, scope, trigger)
			if err != nil || got != first {
				t.Error(got, err)
			}
		})
	}
	wg.Wait()
	changed := trigger
	changed.Content = "Different content"
	if _, err := store.AdmitMainTrigger(ctx, scope, changed); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("identity reused", err)
	}
	cancelled, err := store.CancelMainTrigger(ctx, scope, trigger.ID)
	if err != nil || cancelled != first {
		t.Fatal("accepted input was revoked", cancelled, err)
	}
	var queued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE thread_id=$1 AND state='queued'`, main.ID).Scan(&queued); err != nil || queued != 2 {
		t.Fatal(queued, err)
	}
	if job, err := store.ThreadApplication(ctx, scope, main.ID); err != nil || job != nil {
		t.Fatal("Main bound as application Worker", job, err)
	}
	before := mainTrigger()
	if receipt, err := store.CancelMainTrigger(ctx, scope, before.ID); err != nil || receipt.State != "cancelled" {
		t.Fatal(receipt, err)
	}
	if receipt, err := store.AdmitMainTrigger(ctx, scope, before); err != nil || receipt.State != "cancelled" || receipt.InputID != "" {
		t.Fatal(receipt, err)
	}
	foreign := scope
	foreign.AgentID = uuid.NewString()
	if _, err := store.MainTriggerReceipt(ctx, foreign, trigger.ID); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("foreign receipt", err)
	}
	renewed := scope
	renewed.AgentExecutionEpoch++
	if _, err := store.AdmitMainTrigger(ctx, renewed, trigger); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("renewed authority adopted receipt", err)
	}
}

func TestManagedMainTriggerCancelAndAdmitHaveOneDurableWinner(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	ordinary, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "unrelated", Text: "Do not cancel this work"})
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		q := mainTrigger()
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			if _, err := store.AdmitMainTrigger(ctx, scope, q); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			<-start
			if _, err := store.CancelMainTrigger(ctx, scope, q.ID); err != nil {
				t.Error(err)
			}
		})
		close(start)
		wg.Wait()
		receipt, err := store.MainTriggerReceipt(ctx, scope, q.ID)
		if err != nil {
			t.Fatal(err)
		}
		var inputs int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'application_job_id'=$1`, q.ID).Scan(&inputs); err != nil {
			t.Fatal(err)
		}
		if receipt.State == "accepted" && (inputs != 1 || receipt.ThreadID != main.ID) || receipt.State == "cancelled" && inputs != 0 {
			t.Fatal(receipt, inputs)
		}
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, ordinary.ID).Scan(&state); err != nil || state != "queued" {
		t.Fatal("unrelated Main input cancelled", state, err)
	}
}
