//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func runtimeImportFixture(t *testing.T, agentID string) managedruntime.AgentImport {
	t.Helper()
	at := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	main, worker := uuid.NewString(), uuid.NewString()
	message := func(text string) llm.Message {
		m := llm.TextMessage(llm.RoleUser, text)
		m.ID = uuid.NewString()
		return m
	}
	old, retained, summary, later := message("old history"), message("retained source"), message("imported summary"), message("later message")
	retained.Role = llm.RoleAssistant
	retained.Blocks = append([]llm.Block{{Type: llm.BlockReasoning, Text: "retained reasoning", Signature: "source-signature"}}, retained.Blocks...)
	summary.Kind = llm.MessageKindCompact
	events := make([]managedruntime.Event, 0, 4)
	for i, m := range []llm.Message{old, retained, summary, later} {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		generation := int64(1)
		if i >= 2 {
			generation = 2
		}
		events = append(events, managedruntime.Event{ID: uuid.NewString(), ThreadID: main, Sequence: int64(i + 1), Generation: generation, Kind: "message.appended", Data: data, CreatedAt: at})
	}
	model := runtimeConfig().Models[0]
	model.ModelAuthorizationEpoch = 1
	return managedruntime.AgentImport{Source: "fixture/old-fleet/old-agent", SourceSHA256: strings.Repeat("a", 64), Threads: []managedruntime.ImportedThread{
		{Thread: managedruntime.Thread{ID: main, AgentID: agentID, Kind: "main", Name: "Main", Retention: "active", State: "idle", Generation: 2, Sequence: 4, CreatedAt: at, UpdatedAt: at}, Events: events,
			ModelOrigins: map[string]managedruntime.ModelConfig{retained.ID: model},
			Context:      []string{summary.ID, retained.ID, old.ID, later.ID}, Inputs: []managedruntime.ImportedInput{{InputReceipt: managedruntime.InputReceipt{ID: uuid.NewString(), RequestID: "old-terminal", ThreadID: main, State: "failed", AcceptedAt: at}, Text: "never replay this"}}},
		{Thread: managedruntime.Thread{ID: worker, AgentID: agentID, ParentID: main, Kind: "worker", Application: "memory", Name: "Memory review", Retention: "archived", State: "idle", Generation: 1, CreatedAt: at, UpdatedAt: at}, Application: &managedruntime.ImportedApplication{Application: "memory", JobID: "old-review", State: "failed"}},
	}}
}

func TestManagedRuntimeImportPreservesHistoryContextAndPurpose(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	scope.AgentID = uuid.NewString()
	ctx := context.Background()
	data := runtimeImportFixture(t, scope.AgentID)
	if err := store.ImportAgent(ctx, scope, data); err != nil {
		t.Fatal(err)
	}
	store = runtimepg.New(pool)
	main, err := store.EnsureAgent(ctx, scope)
	if err != nil || main.ID != data.Threads[0].Thread.ID {
		t.Fatal("imported Main replaced", main.ID, err)
	}
	for _, table := range []string{"notification_outbox", "thread_deliveries", "attempts", "compactions", "memory_evidence"} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.`+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("import manufactured work or provider facts", table, count, err)
		}
	}
	page, err := store.Timeline(ctx, scope, main.ID, 0, 100)
	if err != nil || len(page.Events) != len(data.Threads[0].Events) {
		t.Fatal("history not readable", err)
	}
	worker := data.Threads[1].Thread.ID
	if _, err := store.SetThreadArchived(ctx, scope, worker, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{ThreadID: worker, RequestID: "ordinary", Text: "run a shell"}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("imported application Worker became unrestricted", err)
	}
	if _, err := store.CreateWorker(ctx, scope, worker, "child", "Child"); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("imported application Worker gained child creation", err)
	}
	if _, err := store.ThreadApplication(ctx, scope, worker); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("old application authority remained executable", err)
	}
	job := managedruntime.ApplicationJob{Application: "memory", ID: "old-review", Epoch: 1, Fence: 1, Name: "Memory review", Instruction: "Run again", MaxCalls: 2}
	if _, err := store.AdmitApplication(ctx, scope, job); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("imported job was rebound to new execution authority", err)
	}
	receipt, err := store.ApplicationReceipt(ctx, scope, "memory", "old-review")
	if err != nil || receipt.State != "failed" {
		t.Fatal("old application outcome changed", receipt, err)
	}
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "new", Text: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "after-import", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range work.History {
		ids = append(ids, m.ID)
	}
	want := append(append([]string{}, data.Threads[0].Context...), input.ID)
	if !reflect.DeepEqual(ids, want) {
		t.Fatal("imported context order changed", ids, want)
	}
	if !reflect.DeepEqual(work.ModelOrigins, data.Threads[0].ModelOrigins) {
		t.Fatal("verified imported model provenance lost", work.ModelOrigins)
	}
	// A retry acknowledges the original import even after legitimate new work.
	if err := store.ImportAgent(ctx, scope, data); err != nil {
		t.Fatal("retry changed or rejected live continuation", err)
	}
	request := compactionRequest(t, store, lease, work)
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Compacted after import"), StopReason: llm.StopEndTurn}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, response, ""); err != nil {
		t.Fatal(err)
	}
	resumed, err := runtimepg.New(pool).BeginTurn(ctx, lease, scope, input.ID, managedruntime.TurnConfig{})
	if err != nil || resumed.Generation != 3 || len(resumed.History) != 2 || resumed.History[1].ID != input.ID {
		t.Fatal("native compaction after import lost context", resumed.Generation, err)
	}
}

func TestManagedRuntimeImportModelOriginsRemainThreadScoped(t *testing.T) {
	_, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	first, second := scope, scope
	first.AgentID, second.AgentID = uuid.NewString(), uuid.NewString()
	left, right := runtimeImportFixture(t, first.AgentID), runtimeImportFixture(t, second.AgentID)
	// Message identity is not global authority. Distinct imports can retain an
	// identical message ID, but each Thread must resolve its own model origin.
	var message llm.Message
	if err := json.Unmarshal(right.Threads[0].Events[1].Data, &message); err != nil {
		t.Fatal(err)
	}
	message.ID = left.Threads[0].Context[1]
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	right.Threads[0].Events[1].Data = encoded
	right.Threads[0].Context[1] = message.ID
	otherModel := runtimeConfig().Models[0]
	otherModel.ModelID, otherModel.Model, otherModel.ModelAuthorizationEpoch = uuid.NewString(), "other-model", 2
	right.Threads[0].ModelOrigins = map[string]managedruntime.ModelConfig{message.ID: otherModel}
	right.Threads[1].Application.JobID = "other-review"
	for _, item := range []struct {
		scope managedruntime.Scope
		data  managedruntime.AgentImport
	}{{first, left}, {second, right}} {
		if err := store.ImportAgent(ctx, item.scope, item.data); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		scope managedruntime.Scope
		data  managedruntime.AgentImport
	}{{first, left}, {second, right}} {
		input, err := store.AcceptInput(ctx, item.scope, managedruntime.InputRequest{RequestID: "continue", Text: "Continue"})
		if err != nil {
			t.Fatal(err)
		}
		lease, err := store.Claim(ctx, item.scope.AgentID, "import-origin", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		work, err := store.BeginTurn(ctx, lease, item.scope, input.ID, runtimeConfig())
		if err != nil || !reflect.DeepEqual(work.ModelOrigins, item.data.Threads[0].ModelOrigins) {
			t.Fatal("model origin crossed Thread boundary", work.ModelOrigins, err)
		}
	}
}

func TestManagedRuntimeImportRetainsSeveralApplicationWorkersWithoutJobs(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	scope.AgentID = uuid.NewString()
	ctx := context.Background()
	data := runtimeImportFixture(t, scope.AgentID)
	data.Threads[1].Application = &managedruntime.ImportedApplication{Application: "memory"}
	for range 2 {
		worker := data.Threads[1]
		worker.Thread.ID = uuid.NewString()
		data.Threads = append(data.Threads, worker)
	}
	if err := store.ImportAgent(ctx, scope, data); err != nil {
		t.Fatal("multiple historical roles rejected", err)
	}
	store = runtimepg.New(pool)
	threads, err := store.Threads(ctx, scope)
	if err != nil || len(threads) != 4 {
		t.Fatal("historical Worker identities were merged", len(threads), err)
	}
	for _, thread := range threads {
		if thread.Kind == "main" {
			continue
		}
		if thread.Application != "memory" {
			t.Fatal("historical restriction lost", thread.ID)
		}
		if _, err := store.SetThreadArchived(ctx, scope, thread.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "ordinary", ThreadID: thread.ID, Text: "run a command"}); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal("historical role accepted ordinary input", err)
		}
		if _, err := store.AcceptCompaction(ctx, scope, thread.ID, managedruntime.CompactionRequest{RequestID: "compact"}); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal("historical role accepted compaction input", err)
		}
		if _, err := store.CreateWorker(ctx, scope, thread.ID, "child", "Child"); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal("historical role created an unrestricted child", err)
		}
		if _, err := store.ThreadApplication(ctx, scope, thread.ID); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal("historical role regained execution authority", err)
		}
		job := applicationJob()
		job.IdleSourceThread = thread.ID
		if _, err := store.AdmitApplication(ctx, scope, job); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal("historical application became automatic human evidence source", err)
		}
	}
	for _, table := range []string{"application_jobs", "attempts", "notification_outbox"} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.`+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("import fabricated jobs or work", table, count, err)
		}
	}
	if err := store.ImportAgent(ctx, scope, data); err != nil {
		t.Fatal("historical role retry was not idempotent", err)
	}
}

func TestManagedRuntimeImportIsAtomicAndIdempotent(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	if err := store.ImportAgent(ctx, scope, runtimeImportFixture(t, scope.AgentID)); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("overwrote an existing Agent", err)
	}
	scope.AgentID = uuid.NewString()
	data := runtimeImportFixture(t, scope.AgentID)
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Go(func() { results <- store.ImportAgent(ctx, scope, data) })
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	foreign := scope
	foreign.UserID = uuid.NewString()
	if err := store.ImportAgent(ctx, foreign, data); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("cross-owner retry accepted", err)
	}
	changed := data
	changed.SourceSHA256 = strings.Repeat("b", 64)
	if err := store.ImportAgent(ctx, scope, changed); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("changed source accepted", err)
	}
	changed = runtimeImportFixture(t, scope.AgentID)
	if err := store.ImportAgent(ctx, scope, changed); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("same source hash accepted different converted payload", err)
	}
	other := scope
	other.AgentID = uuid.NewString()
	broken := runtimeImportFixture(t, other.AgentID)
	// A globally colliding event UUID fails after the new Agent insert.
	broken.Threads[0].Events[1].ID = data.Threads[0].Events[0].ID
	if err := store.ImportAgent(ctx, other, broken); err == nil {
		t.Fatal("conflicting event accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.agents WHERE id=$1`, other.AgentID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed import left partial state", count, err)
	}
}

func TestManagedRuntimeImportPurgeRemovesProvenanceAndPreventsRecreation(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	scope.AgentID = uuid.NewString()
	ctx := context.Background()
	data := runtimeImportFixture(t, scope.AgentID)
	if err := store.ImportAgent(ctx, scope, data); err != nil {
		t.Fatal(err)
	}
	request := lifecycle.Request{Target: lifecycle.Target{ID: uuid.NewString(), TenantID: scope.TenantID, UserID: scope.UserID, FleetID: scope.FleetID, AgentIDs: []string{scope.AgentID}}, Phase: lifecycle.Erase}
	if receipt, err := store.Purge(ctx, request); err != nil || !receipt.DataRemoved {
		t.Fatal("purge failed", receipt, err)
	}
	for _, table := range []string{"imports", "imported_message_models", "context_checkpoints"} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.`+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("purge left imported private state", table, count, err)
		}
	}
	if err := store.ImportAgent(ctx, scope, data); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("import resurrected a purged Agent", err)
	}
}
