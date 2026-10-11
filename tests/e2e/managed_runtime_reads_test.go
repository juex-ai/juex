//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/managedruntime"
)

func TestManagedRuntimeReadsDuringActivationWrites(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) { streamManagedReply(w, "Ready") })
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	initial := f.timeline(t, f.main.ID)
	for _, lock := range []struct{ name, query string }{
		{"agent", `UPDATE runtime.agents SET epoch=epoch+1 WHERE id=$1`},
		{"thread", `UPDATE runtime.threads SET sequence=sequence+1 WHERE agent_id=$1 AND kind='main'`},
	} {
		t.Run(lock.name, func(t *testing.T) {
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, lock.query, f.agent.ID); err != nil {
				t.Fatal(err)
			}
			// A slow activation commit must not block inspection of the last
			// committed conversation, or expose its uncommitted watermark.
			t.Run("ensure", func(t *testing.T) {
				bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				main, err := f.store.EnsureAgent(bounded, scope)
				if err != nil || main.ID != f.main.ID || main.Sequence != initial.Thread.Sequence {
					t.Fatal("existing Main lookup waited for a writer", main, err)
				}
			})
			for _, thread := range []string{"", f.main.ID} {
				t.Run("timeline/"+thread, func(t *testing.T) {
					bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
					defer cancel()
					page, err := f.store.Timeline(bounded, scope, thread, 0, 200)
					if err != nil || page.Thread.Sequence != initial.Thread.Sequence || len(page.Events) != len(initial.Events) || page.HasMore {
						t.Fatal("Timeline failed to read committed snapshot", page, err)
					}
				})
			}
			t.Run("http", func(t *testing.T) {
				bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				request, err := http.NewRequestWithContext(bounded, http.MethodGet, f.base+"/threads/"+f.main.ID+"/events", nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := f.client.Do(request)
				if err != nil {
					t.Fatal("public Timeline waited for activation writes", err)
				}
				defer response.Body.Close()
				var page managedruntime.Timeline
				if err := json.NewDecoder(response.Body).Decode(&page); err != nil || response.StatusCode != http.StatusOK || page.Thread.Sequence != initial.Thread.Sequence || len(page.Events) != len(initial.Events) {
					t.Fatal("public Timeline lost the committed conversation", response.StatusCode, page, err)
				}
			})
		})
	}
}

func TestManagedRuntimeTimelineSnapshotAcrossCommit(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	initial, err := store.Timeline(ctx, scope, main.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	// Pause the event query after Timeline has read its Thread watermark.
	if _, err := writer.Exec(ctx, `LOCK runtime.events IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	var writerPID int
	if err := writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		page managedruntime.Timeline
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, err := store.Timeline(ctx, scope, main.ID, 0, 200)
		done <- result{page, err}
	}()
	runtimeEventually(t, func() bool {
		var blocked bool
		return pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, writerPID).Scan(&blocked) == nil && blocked
	})
	if _, err := writer.Exec(ctx, `WITH next AS (UPDATE runtime.threads SET sequence=sequence+1 WHERE id=$1 RETURNING sequence,generation)
INSERT INTO runtime.events(thread_id,sequence,generation,kind,data) SELECT $1,sequence,generation,'message.appended','{"id":"concurrent-message","role":"assistant","blocks":[{"type":"text","text":"Arrived"}]}' FROM next`, main.ID); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	read := <-done
	if read.err != nil || read.page.Thread.Sequence != initial.Thread.Sequence || read.page.NextSequence != initial.NextSequence || len(read.page.Events) != len(initial.Events) || read.page.HasMore {
		t.Fatal("Timeline mixed committed snapshots", read.page, read.err)
	}
	next, err := store.Timeline(ctx, scope, main.ID, initial.NextSequence, 200)
	if err != nil || next.Thread.Sequence != initial.Thread.Sequence+1 || next.NextSequence != initial.NextSequence+1 || len(next.Events) != 1 || next.HasMore {
		t.Fatal("subsequent Timeline lost committed event", next, err)
	}
}
