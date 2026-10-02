//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/managedruntime"
)

func TestManagedRuntimeCancelHeldRestoresAutomaticMemory(t *testing.T) {
	for _, reason := range []string{"model_unavailable", "compaction_failed", "authority_changed"} {
		t.Run(reason, func(t *testing.T) {
			var calls atomic.Int32
			f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				streamManagedReply(w, "Replacement completed")
			})
			ctx := context.Background()
			scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			old := f.submit(t, "held-request", f.main.ID, "Original work")
			lease, err := f.store.Claim(ctx, scope.AgentID, "held-fixture", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.HoldInput(ctx, lease, old.ID, reason); err != nil {
				t.Fatal(err)
			}
			if err := f.store.Release(ctx, lease); err != nil {
				t.Fatal(err)
			}
			replacement := f.submit(t, "replacement-request", f.main.ID, "Continue after fixing the configuration")
			stop := f.run(t)
			runtimeEventually(t, func() bool { return calls.Load() == 1 && f.timeline(t, f.main.ID).Thread.State == "idle" })
			stop()
			if thread := f.timeline(t, f.main.ID).Thread; thread.HeldInputs != 1 || thread.PendingInputs != 0 {
				t.Fatal("held inputs must remain visible independently of pending work", thread)
			}
			age := func() {
				if _, err := f.pool.Exec(ctx, `UPDATE runtime.threads SET updated_at=clock_timestamp()-interval '61 seconds' WHERE id=$1`, f.main.ID); err != nil {
					t.Fatal(err)
				}
			}
			job := applicationJob()
			job.IdleSourceThread = f.main.ID
			age()
			if _, err := f.store.AdmitApplication(ctx, scope, job); !errors.Is(err, managedruntime.ErrSourceBusy) {
				t.Fatal("unresolved held input must prevent automatic review", err)
			}
			managementCall[map[string]any](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/cancel", f.origin, map[string]any{}, 200)
			if thread := f.timeline(t, f.main.ID).Thread; thread.HeldInputs != 0 {
				t.Fatal("discarded inputs remain held", thread)
			}
			if retry := f.submit(t, "held-request", f.main.ID, "Original work"); retry.ID != old.ID || retry.State != "cancelled" {
				t.Fatal("discarded input could replay", retry)
			}
			if retry := f.submit(t, "replacement-request", f.main.ID, "Continue after fixing the configuration"); retry.ID != replacement.ID || retry.State != "completed" {
				t.Fatal("completed history changed", retry)
			}
			age()
			if receipt, err := f.store.AdmitApplication(ctx, scope, job); err != nil || receipt.InputID == "" {
				t.Fatal("explicit discard did not unblock automatic Memory", receipt, err)
			}
			if calls.Load() != 1 {
				t.Fatal("held work was replayed", calls.Load())
			}
			var cancellations int
			for _, event := range f.timeline(t, f.main.ID).Events {
				if event.Kind == "thread.cancelled" {
					cancellations++
				}
			}
			if cancellations != 1 {
				t.Fatal("missing durable cancellation", cancellations)
			}
		})
	}
}
