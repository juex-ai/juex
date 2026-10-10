//go:build postgres

package e2e

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/juex-ai/juex/internal/managedruntime"
)

func TestManagedQueuedInputsExcludeActiveWork(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		streamManagedReply(w, "Processed")
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f.submit(t, "queued-first", f.main.ID, "First")
	f.submit(t, "queued-second", f.main.ID, "Second")
	assertCounts := func(pending, queued int64) {
		t.Helper()
		thread := f.timeline(t, f.main.ID).Thread
		if thread.PendingInputs != pending || thread.QueuedInputs != queued {
			t.Fatalf("timeline pending=%d queued=%d, want %d/%d", thread.PendingInputs, thread.QueuedInputs, pending, queued)
		}
		threads := managementCall[[]managedruntime.Thread](t, f.client, "GET", f.base+"/threads", f.origin, nil, 200)
		if len(threads) != 1 || threads[0].PendingInputs != pending || threads[0].QueuedInputs != queued {
			t.Fatalf("thread list: %+v", threads)
		}
	}
	assertCounts(2, 2)
	f.run(t)
	runtimeEventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	})
	assertCounts(2, 1)
	once.Do(func() { close(release) })
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" && calls.Load() == 2 })
	assertCounts(0, 0)
}
