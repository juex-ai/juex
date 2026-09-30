//go:build postgres

package e2e

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/managed"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func TestManagedMemoryRecallSnapshotAndHumanFence(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	binding, proposal := f.propose(t, "recall")
	if _, err := f.service.Decide(ctx, f.scope, binding, memoryDecision("recall-entry", proposal), "decide"); err != nil {
		t.Fatal(err)
	}
	if value, err := f.service.Recall(ctx, f.scope.Access, "concise"); err != nil || value.Text != "" {
		t.Fatal("Basic recalled automatically", value, err)
	}
	if _, err := f.service.Configure(ctx, f.human, 1, true, mc.Advanced); err != nil {
		t.Fatal(err)
	}
	gateway := managed.RuntimeApplications{Memory: f.service}
	scope := toRuntimeScope(f.scope)
	snapshot, err := gateway.Recall(ctx, scope, "concise")
	if err != nil || !strings.Contains(snapshot.Text, "recall-entry") || len(snapshot.Text) > 4096 || !gateway.RecallValid(ctx, scope, snapshot) {
		t.Fatal(snapshot, err)
	}
	if _, err := f.service.Administer(ctx, f.human, mc.AdminRequest{Key: "forget", Action: "delete", EntryIDs: []string{"recall-entry"}}); err != nil {
		t.Fatal(err)
	}
	if gateway.RecallValid(ctx, scope, snapshot) {
		t.Fatal("forgotten snapshot remained injectable")
	}
}

func TestManagedRuntimeRecallPreparingRecoveryDoesNotRepeatRetrieval(t *testing.T) {
	_, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "recall", ThreadID: main.ID, Text: "Original recall query"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "first", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, query, prepare, err := store.BeginRecall(ctx, lease, work)
	if err != nil || !prepare || query != "Original recall query" {
		t.Fatal(query, prepare, err)
	}
	if err := store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	next, err := store.Claim(ctx, scope.AgentID, "replacement", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := store.BeginTurn(ctx, next, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	value, _, prepare, err := store.BeginRecall(ctx, next, recovered)
	if err != nil || prepare || value.State != "unavailable" {
		t.Fatal("unknown retrieval repeated", value, prepare, err)
	}
	if err := store.FinishRecall(ctx, lease, work, managedruntime.RecallSnapshot{State: "ready", Text: "stale"}); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("stale recall committed", err)
	}
}

type recallFixtureGateway struct {
	runtimeApplicationGateway
	Retrievals  atomic.Int32
	Valid       atomic.Bool
	Unavailable bool
}

func (g *recallFixtureGateway) Recall(context.Context, managedruntime.Scope, string) (managedruntime.RecallSnapshot, error) {
	g.Retrievals.Add(1)
	if g.Unavailable {
		return managedruntime.RecallSnapshot{}, errors.New("Memory unavailable")
	}
	return managedruntime.RecallSnapshot{Epoch: 1, Fence: 1, Text: "unique_recall_marker"}, nil
}
func (g *recallFixtureGateway) RecallValid(context.Context, managedruntime.Scope, managedruntime.RecallSnapshot) bool {
	return g.Valid.Load()
}
func (g *recallFixtureGateway) Call(ctx context.Context, work managedruntime.ToolWork, job *managedruntime.ApplicationJob) (any, error) {
	g.Valid.Store(false)
	return g.runtimeApplicationGateway.Call(ctx, work, job)
}

func TestManagedRuntimeRecallOncePerInputAndOptionalFailure(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "unavailable"}[unavailable], func(t *testing.T) {
			g := &recallFixtureGateway{Unavailable: unavailable}
			g.Valid.Store(true)
			var calls atomic.Int32
			f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				n := calls.Add(1)
				want := !unavailable && n == 1
				if strings.Contains(string(body), "unique_recall_marker") != want {
					t.Errorf("recall content for model call %d", n)
				}
				if n == 1 {
					streamManagedTool(w, "memory_search", map[string]any{"query": "test"})
				} else {
					streamManagedReply(w, "Finished without another retrieval")
				}
			})
			stop := runApplicationFixture(t, f, g)
			input := f.submit(t, "recall", "", "Read relevant history")
			runtimeEventually(t, func() bool {
				var state string
				_ = f.pool.QueryRow(context.Background(), `SELECT state FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&state)
				return state == "completed"
			})
			stop()
			if calls.Load() != 2 || g.Retrievals.Load() != 1 {
				t.Fatal("retrieval repeated across tool iterations", calls.Load(), g.Retrievals.Load())
			}
		})
	}
}
