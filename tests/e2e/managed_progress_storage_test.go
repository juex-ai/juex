//go:build postgres

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func TestManagedProgressDoesNotReadRequestBodies(t *testing.T) {
	for _, operation := range []string{"write", "timeline", "history"} {
		t.Run(operation, func(t *testing.T) {
			pool, store, scope, thread := runtimeDatabase(t)
			ctx := context.Background()
			request := largeModelOriginRequest(t)
			config := runtimeConfig()
			config.Models[0] = request.Model
			input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "progress", Text: "Continue"})
			if err != nil {
				t.Fatal(err)
			}
			lease, err := store.Claim(ctx, scope.AgentID, "writer", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			work, err := store.BeginTurn(ctx, lease, scope, input.ID, config)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := managedruntime.ProgressSnapshot{Revision: 1, Blocks: []managedruntime.ProgressBlock{{Kind: "text", Text: "Partial answer"}}}
			if err := store.WriteProgress(ctx, lease, attempt.ID, snapshot); err != nil {
				t.Fatal(err)
			}
			restricted := runtimepg.New(runtimePoolWithoutRequestBodies(t, pool))
			read := restricted.Timeline
			switch operation {
			case "write":
				snapshot.Revision = 2
				snapshot.Blocks[0].Text = "More partial answer"
				if err := restricted.WriteProgress(ctx, lease, attempt.ID, snapshot); err != nil {
					t.Fatal("progress write required request body", err)
				}
				read = store.Timeline
			case "history":
				read = restricted.History
			}
			page, err := read(ctx, scope, thread.ID, 0, 200)
			if err != nil {
				t.Fatal("progress read required request body", err)
			}
			if len(page.Progress) != 1 {
				t.Fatal("partial answer missing", page.Progress)
			}
			progress := page.Progress[0]
			if progress.AttemptID != attempt.ID || progress.TurnID != work.TurnID || progress.Generation != work.Generation || progress.Model != request.Model.Provider+":"+request.Model.Model || progress.State != "running" || progress.Snapshot.Revision != snapshot.Revision || len(progress.Snapshot.Blocks) != 1 || progress.Snapshot.Blocks[0].Text != snapshot.Blocks[0].Text {
				t.Fatal("progress projection changed", progress)
			}
		})
	}
}
