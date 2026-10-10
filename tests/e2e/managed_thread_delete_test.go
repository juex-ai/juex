//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedThreadDeletionErasesContentPreservesIdentityAndAccounting(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) { streamManagedReply(w, "Private Worker answer") })
	ctx := context.Background()
	worker := managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/workers", f.origin, map[string]string{"request_id": "delete-worker", "name": "Private"}, 200)
	stop := f.run(t)
	f.submit(t, "private-input", worker.ID, "Private Worker input")
	runtimeEventually(t, func() bool { return f.timeline(t, worker.ID).Thread.State == "idle" })
	stop()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO runtime.thread_state(thread_id,value) VALUES($1,'{"revision":2,"notes":{"content":"private notes"},"tasks":[]}')`,
		`INSERT INTO runtime.thread_working_files(thread_id,location) VALUES($1,'{"working_directory":"preserved-independent-workspace"}')`,
		`INSERT INTO runtime.context_resets(thread_id,request_id,result) VALUES($1,'old-reset','{}')`,
		`INSERT INTO runtime.imported_message_models(thread_id,message_id,model) VALUES($1,gen_random_uuid(),'{"model":"private"}')`,
		`INSERT INTO runtime.attempt_progress(attempt_id,start_sequence) SELECT a.id,1 FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1 ON CONFLICT DO NOTHING`,
	} {
		if _, err := f.pool.Exec(ctx, query, worker.ID); err != nil {
			t.Fatal(query, err)
		}
	}
	// A held input is private retained content, not authority to resume execution.
	if _, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "held-private", ThreadID: worker.ID, Text: "undelivered"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.inputs SET state='held' WHERE thread_id=$1 AND request_id='held-private'`, worker.ID); err != nil {
		t.Fatal(err)
	}
	before := statusRows(t, f.pool, "runtime.usage_records", "runtime.usage_daily", "runtime.usage_periods")
	mainBefore := f.timeline(t, f.main.ID)
	managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+worker.ID+"/archive", f.origin, map[string]bool{"archived": true}, 200)
	managementCall[any](t, f.client, "DELETE", f.base+"/threads/"+worker.ID, f.origin, nil, 409)
	evidence, err := f.store.PendingEvidence(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range evidence {
		if err = f.store.FinishEvidence(ctx, delivery.ID, "test fixture has no Memory receiver"); err != nil {
			t.Fatal(err)
		}
	}
	notice, err := f.store.ClaimNotification(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.authority.RecordNotification(ctx, notice.Event); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishNotification(ctx, notice, true); err != nil {
		t.Fatal(err)
	}
	receipt := managementCall[managedruntime.ThreadDeletionReceipt](t, f.client, "DELETE", f.base+"/threads/"+worker.ID, f.origin, nil, 200)
	if !receipt.Deleted || receipt.ThreadID != worker.ID {
		t.Fatal(receipt)
	}
	replay, err := runtimepg.New(f.pool).DeleteThread(ctx, scope, worker.ID)
	if err != nil || replay != receipt {
		t.Fatal(replay, err)
	}
	for _, table := range []string{"threads", "inputs", "events", "turns", "attempts", "attempt_progress", "thread_state", "thread_working_files", "context_resets", "imported_message_models"} {
		var count int
		query := `SELECT count(*) FROM runtime.` + table
		if table == "threads" {
			query += ` WHERE id<>$1`
		} else if table == "inputs" || table == "events" || table == "turns" || table == "thread_state" || table == "thread_working_files" || table == "context_resets" || table == "imported_message_models" {
			query += ` WHERE thread_id<>$1`
		} else {
			query += ` WHERE $1::uuid IS NOT NULL`
		}
		if err := f.pool.QueryRow(ctx, query, f.main.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
	if !reflect.DeepEqual(before, statusRows(t, f.pool, "runtime.usage_records", "runtime.usage_daily", "runtime.usage_periods")) {
		t.Fatal("deletion rewrote accounting")
	}
	if !reflect.DeepEqual(mainBefore, f.timeline(t, f.main.ID)) {
		t.Fatal("deletion changed Main")
	}
	managementCall[any](t, f.client, "GET", f.base+"/threads/"+worker.ID+"/events?after=0&limit=100", f.origin, nil, 403)
	managementCall[any](t, f.client, "GET", f.base+"/threads/"+worker.ID+"/inspection", f.origin, nil, 403)
	for _, name := range []string{"Private", "Changed payload"} {
		if _, err := f.store.CreateWorker(ctx, scope, f.main.ID, "delete-worker", name); !errors.Is(err, managedruntime.ErrConflict) {
			t.Fatal("create retry resurrected deleted Worker", err)
		}
	}
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Uninitialized"})
	if err != nil {
		t.Fatal(err)
	}
	managementCall[any](t, f.client, "DELETE", f.origin+"/api/tenants/"+f.tenant+"/agents/"+other.ID+"/threads/"+worker.ID, f.origin, nil, 403)
	var initialized bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.agents WHERE id=$1)`, other.ID).Scan(&initialized); err != nil || initialized {
		t.Fatal("delete initialized another Agent", initialized, err)
	}
	managementCall[any](t, f.client, "DELETE", f.base+"/threads/"+uuid.NewString(), f.origin, nil, 403)
	managementCall[any](t, f.client, "DELETE", f.base+"/threads/invalid", f.origin, nil, 400)
}

func TestManagedThreadDeletionRejectsUnsettledResponsibilities(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	worker, err := store.CreateWorker(ctx, scope, main.ID, "worker", "Worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.DeleteThread(ctx, scope, main.ID); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = store.DeleteThread(ctx, scope, worker.ID); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "delete-checks", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "late-usage", ThreadID: worker.ID, Text: "Keep final usage"})
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{MaxOutputTokens: 4096, Generation: 1, Purpose: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CancelThread(ctx, scope, worker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetThreadArchived(ctx, scope, worker.ID, true); err != nil {
		t.Fatal(err)
	}
	before := statusRows(t, pool, "runtime.threads", "runtime.attempts", "runtime.usage_records", "runtime.thread_deletions")
	if _, err = store.DeleteThread(ctx, scope, worker.ID); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("deleted before provider settled", err)
	}
	if !reflect.DeepEqual(before, statusRows(t, pool, "runtime.threads", "runtime.attempts", "runtime.usage_records", "runtime.thread_deletions")) {
		t.Fatal("failed deletion wrote state")
	}
	if err = store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "late"), StopReason: llm.StopEndTurn}, ""); err != nil {
		t.Fatal(err)
	}
	// These durable records are allowed to outlive a cancelled Turn. Each must
	// settle before deletion; changing only Thread retention cannot bypass them.
	cases := []struct{ name, setup, settle string }{
		{"application", `UPDATE runtime.threads SET application='memory' WHERE id=$1`, `UPDATE runtime.threads SET application='' WHERE id=$1`},
		{"child", `INSERT INTO runtime.threads(agent_id,parent_id,kind,name,retention) SELECT agent_id,id,'worker','Archived child','archived' FROM runtime.threads WHERE id=$1`, `DELETE FROM runtime.threads WHERE parent_id=$1`},
		{"evidence", `INSERT INTO runtime.memory_evidence(input_id) SELECT id FROM runtime.inputs WHERE thread_id=$1`, `DELETE FROM runtime.memory_evidence WHERE input_id IN (SELECT id FROM runtime.inputs WHERE thread_id=$1)`},
		{"notification", `INSERT INTO runtime.notification_outbox(event_id,agent_id,thread_id,event,not_before) SELECT gen_random_uuid(),agent_id,id,'{}',clock_timestamp() FROM runtime.threads WHERE id=$1`, `UPDATE runtime.notification_outbox SET state='sent' WHERE thread_id=$1`},
		{"instruction ACK", `INSERT INTO runtime.instruction_preparations(turn_id,thread_id,scope,config,state) SELECT id,thread_id,'{}','{}','cancelled' FROM runtime.turns WHERE thread_id=$1`, `UPDATE runtime.instruction_preparations SET output_acknowledged=true WHERE thread_id=$1`},
		{"unknown hook", `INSERT INTO runtime.hooks(turn_id,thread_id,event,anchor,hook_id,ordinal,scope,declaration,input,state) SELECT id,thread_id,'stop','old','hook',1,'{}','{}','{}','unknown' FROM runtime.turns WHERE thread_id=$1`, `UPDATE runtime.hooks SET state='cancelled' WHERE thread_id=$1`},
		{"observer ACK", `WITH c AS(INSERT INTO runtime.observer_controls(agent_id,thread_id,request_id,start_request,scope,environment_id,request,source_id,mode,desired,state) SELECT agent_id,id,'observer','{}','{}','env','{}',gen_random_uuid(),'once','stopped','cancelled' FROM runtime.threads WHERE id=$1 RETURNING *) INSERT INTO runtime.observation_sources(id,thread_id,agent_id,scope,environment_id,operation_id,kind,control_id,closed,cursor,confirmed_cursor) SELECT source_id,thread_id,agent_id,'{}','env',source_id::text,'observe_command',id,true,10,0 FROM c`, `UPDATE runtime.observation_sources SET confirmed_cursor=cursor WHERE thread_id=$1`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, tc.setup, worker.ID); err != nil {
				t.Fatal(err)
			}
			before := statusRows(t, pool, "runtime.threads", "runtime.thread_deletions", "runtime.attempts", "runtime.usage_records")
			if _, err := store.DeleteThread(ctx, scope, worker.ID); !errors.Is(err, managedruntime.ErrConflict) {
				t.Fatal("unsettled responsibility erased", err)
			}
			if !reflect.DeepEqual(before, statusRows(t, pool, "runtime.threads", "runtime.thread_deletions", "runtime.attempts", "runtime.usage_records")) {
				t.Fatal("rejection was not atomic")
			}
			if _, err := pool.Exec(ctx, tc.settle, worker.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err = store.DeleteThread(ctx, scope, worker.ID); err != nil {
		t.Fatal("settled Worker cannot be deleted", err)
	}
}

func TestManagedThreadDeletionPreservesAcceptedPeerActionReceipt(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	target, err := f.service.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "target", "Target")
	if err != nil {
		t.Fatal(err)
	}
	scope, tool := prepareRuntimeCall(t, f, f.main.ID, "send", llm.Block{Type: llm.BlockToolUse, ToolUseID: "send", ToolName: "thread_send"})
	action := managedruntime.ThreadAction{Kind: "thread_send", ThreadID: target.ID, Query: "accepted exactly once"}
	receipt, err := f.store.ApplyThreadAction(ctx, tool, action, scope)
	if err != nil {
		t.Fatal(err)
	}
	var input managedruntime.InputReceipt
	if err = json.Unmarshal(receipt, &input); err != nil {
		t.Fatal(err)
	}
	if err = f.store.CancelThread(ctx, scope, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SetThreadArchived(ctx, scope, target.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.DeleteThread(ctx, scope, target.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := runtimepg.New(f.pool).ApplyThreadAction(ctx, tool, action, scope)
	var replayed managedruntime.InputReceipt
	if decodeErr := json.Unmarshal(replay, &replayed); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if err != nil || !reflect.DeepEqual(replayed, input) {
		t.Fatal("lost reply became a new action", string(replay), err)
	}
	action.Query = "different request"
	if _, err = f.store.ApplyThreadAction(ctx, tool, action, scope); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal(err)
	}
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("replayed deleted input", count, err)
	}
}

func TestManagedThreadDeletionKeepsOtherThreadsAcceptedInput(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	worker, err := f.service.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "sender", "Sender")
	if err != nil {
		t.Fatal(err)
	}
	scope, tool := prepareRuntimeCall(t, f, worker.ID, "send", llm.Block{Type: llm.BlockToolUse, ToolUseID: "send", ToolName: "thread_send"})
	receipt, err := f.store.ApplyThreadAction(ctx, tool, managedruntime.ThreadAction{Kind: "thread_send", ThreadID: f.main.ID, Query: "retain independently accepted input"}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishTool(ctx, tool, managedruntime.ToolOutcome{State: "ready", Content: string(receipt)}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.CancelThread(ctx, scope, worker.ID); err != nil {
		t.Fatal(err)
	}
	// The sender's admitted original input has an independent Memory outbox.
	evidence, err := f.store.PendingEvidence(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range evidence {
		if err = f.store.FinishEvidence(ctx, d.ID, "test fixture has no Memory receiver"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.store.SetThreadArchived(ctx, scope, worker.ID, true); err != nil {
		t.Fatal(err)
	}
	before := f.timeline(t, f.main.ID)
	if _, err = f.store.DeleteThread(ctx, scope, worker.ID); err != nil {
		t.Fatal(err)
	}
	after := f.timeline(t, f.main.ID)
	if !reflect.DeepEqual(before, after) || after.Thread.PendingInputs != 1 {
		t.Fatal("sender deletion changed accepted recipient input", after)
	}
	var input managedruntime.InputReceipt
	if err = json.Unmarshal(receipt, &input); err != nil {
		t.Fatal(err)
	}
	var state, text string
	if err = f.pool.QueryRow(ctx, `SELECT state,text FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&state, &text); err != nil || state != "queued" || text != "retain independently accepted input" {
		t.Fatal(state, text, err)
	}
}

func TestManagedThreadDeletionRPCFencesAndSerializedRestore(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	listener := platformListener(t)
	server, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(f.credentials, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	worker, err := client.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "rpc-delete", "Worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Archive(ctx, f.actor, f.tenant, f.agent.ID, worker.ID, true); err != nil {
		t.Fatal(err)
	}
	foreign, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "execution"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = foreign.DeleteThread(ctx, f.actor, f.tenant, f.agent.ID, worker.ID); !errors.Is(err, platformrpc.ErrUnavailable) {
		t.Fatal("Execution caller crossed Runtime boundary", err)
	}
	if _, err = client.DeleteThread(ctx, uuid.NewString(), f.tenant, f.agent.ID, worker.ID); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("foreign actor deleted content", err)
	}
	// Hold the actual graph lock, observe both requests waiting on it, then let
	// deletion/restoration compete. Exactly one valid transition may commit.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,5107))`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	restored := make(chan error, 1)
	go func() { _, e := client.DeleteThread(ctx, f.actor, f.tenant, f.agent.ID, worker.ID); deleted <- e }()
	go func() { _, e := client.Archive(ctx, f.actor, f.tenant, f.agent.ID, worker.ID, false); restored <- e }()
	runtimeEventually(t, func() bool {
		var waiting int
		e := f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%5107%'`).Scan(&waiting)
		return e == nil && waiting == 2
	})
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	deletionErr, restoreErr := <-deleted, <-restored
	if (deletionErr == nil) == (restoreErr == nil) {
		t.Fatal("competing lifecycle transitions did not serialize", deletionErr, restoreErr)
	}
	if deletionErr != nil {
		if !errors.Is(deletionErr, managedruntime.ErrConflict) {
			t.Fatal(deletionErr)
		}
		if _, err = client.Archive(ctx, f.actor, f.tenant, f.agent.ID, worker.ID, true); err != nil {
			t.Fatal(err)
		}
		if _, err = client.DeleteThread(ctx, f.actor, f.tenant, f.agent.ID, worker.ID); err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(restoreErr, managedruntime.ErrDenied) {
		t.Fatal(restoreErr)
	}
	receipt, err := client.DeleteThread(ctx, f.actor, f.tenant, f.agent.ID, worker.ID)
	if err != nil || !receipt.Deleted || receipt.ThreadID != worker.ID {
		t.Fatal(receipt, err)
	}
	// Revoked members cannot read a deletion receipt, even after a successful call.
	if _, err = f.pool.Exec(ctx, `UPDATE management.memberships SET status='suspended' WHERE user_id=$1`, f.actor); err != nil {
		t.Fatal(err)
	}
	if _, err = client.DeleteThread(ctx, f.actor, f.tenant, f.agent.ID, worker.ID); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("revoked actor read receipt", err)
	}
}

func TestManagedThreadDeletionWaitsForStoppedSourceSubscriberCleanup(t *testing.T) {
	f, _, _, request, _ := observerFixture(t, `sleep 60`)
	ctx := context.Background()
	sourceThread, err := f.service.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "source-owner", "Observer owner")
	if err != nil {
		t.Fatal(err)
	}
	request.ThreadID = sourceThread.ID
	control, err := f.service.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.store.ClaimObservation(ctx, "delete-observer")
	if err != nil {
		t.Fatal(err)
	}
	fact := managedruntime.Observation{ID: uuid.NewString(), Kind: "command.observation", EnvironmentID: source.EnvironmentID, OperationID: source.OperationID, Offset: 10, Data: json.RawMessage(`{"text":"do not reactivate after stop"}`), CreatedAt: time.Now().UTC()}
	if _, err = f.store.SetSourceSubscription(ctx, scope, control.SourceID, f.main.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishObservation(ctx, source, managedruntime.ObservationBatch{Cursor: 10, Facts: []managedruntime.Observation{fact}, Closed: true}); err != nil {
		t.Fatal(err)
	}
	delivery, err := f.store.ClaimObservationDelivery(ctx, "delete-delivery")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishObservationDelivery(ctx, delivery, true, nil); err != nil {
		t.Fatal(err)
	}
	unrelated, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "unrelated", Text: "retained separately"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.StopObserver(ctx, scope, control.SourceID); err != nil {
		t.Fatal(err)
	}
	// No external producer was admitted. Settle its cancellation/ACK as the
	// observer worker does, deliberately leaving subscriber cleanup outstanding.
	if _, err = f.pool.Exec(ctx, `UPDATE runtime.observer_controls SET state='cancelled' WHERE id=$1`, control.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE runtime.observation_sources SET confirmed_cursor=cursor WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SetThreadArchived(ctx, scope, sourceThread.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.DeleteThread(ctx, scope, sourceThread.ID); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("deletion removed stop fence before subscriber cleanup", err)
	}
	if err = f.store.CleanupStoppedSubscription(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.DeleteThread(ctx, scope, sourceThread.ID); err != nil {
		t.Fatal("settled subscription prevented deletion", err)
	}
	var state string
	if err = f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE source->>'observation_id'=$1`, fact.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatal("stopped input reactivated", state, err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, unrelated.ID).Scan(&state); err != nil || state != "queued" {
		t.Fatal("unrelated input changed", state, err)
	}
}
