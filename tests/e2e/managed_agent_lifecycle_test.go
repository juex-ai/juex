//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func TestManagedAgentPauseReceiptAndRecovery(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	change := managedruntime.AgentLifecycleChange{RequestID: "pause-one", Action: "pause", Version: 1}
	paused, err := store.ChangeAgentLifecycle(ctx, scope, change)
	if err != nil || paused.Outcome != "applied" || paused.State.Mode != "paused" || paused.State.Version != 2 {
		t.Fatal(paused, err)
	}
	restarted := runtimepg.New(pool)
	if _, err = restarted.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "paused", Text: "wait"}); !errors.Is(err, managedruntime.ErrPaused) {
		t.Fatal("paused admitted input", err)
	}
	if _, err = restarted.CreateWorker(ctx, scope, main.ID, "paused-worker", "Worker"); !errors.Is(err, managedruntime.ErrPaused) {
		t.Fatal("paused created Worker", err)
	}
	if _, err = restarted.Claim(ctx, scope.AgentID, "paused-activation", time.Minute); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("paused activated", err)
	}
	resumed, err := restarted.ChangeAgentLifecycle(ctx, scope, managedruntime.AgentLifecycleChange{RequestID: "resume", Action: "resume", Version: 2})
	if err != nil || resumed.State.Mode != "running" || resumed.State.Version != 3 {
		t.Fatal(resumed, err)
	}
	retry, err := restarted.ChangeAgentLifecycle(ctx, scope, change)
	if err != nil || retry.State.Version != 2 {
		t.Fatal("lost original receipt", retry, err)
	}
	state, err := restarted.AgentRunState(ctx, scope)
	if err != nil || state.Mode != "running" || state.Version != 3 {
		t.Fatal("late pause changed current state", state, err)
	}
	change.Interrupt = true
	if _, err = restarted.ChangeAgentLifecycle(ctx, scope, change); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("identity rebound", err)
	}
	if _, err = restarted.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "resumed", Text: "continue"}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedAgentDeferredAndActivationRestart(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	request := managedruntime.InputRequest{RequestID: "one-input", Text: "preserve my work"}
	input, err := store.AcceptInput(ctx, scope, request)
	if err != nil {
		t.Fatal(err)
	}
	change := managedruntime.AgentLifecycleChange{RequestID: "idle-only", Action: "restart", Version: 1}
	deferred, err := store.ChangeAgentLifecycle(ctx, scope, change)
	if err != nil || deferred.Outcome != "deferred" || deferred.State.Version != 1 || len(deferred.State.Busy) == 0 {
		t.Fatal(deferred, err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "old", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{MaxOutputTokens: 4096, Messages: work.History, Purpose: "main"})
	if err != nil {
		t.Fatal(err)
	}
	change.RequestID = "explicit-restart"
	change.Interrupt = true
	receipt, err := store.ChangeAgentLifecycle(ctx, scope, change)
	if err != nil || receipt.Outcome != "applied" || receipt.State.Mode != "running" {
		t.Fatal(receipt, err)
	}
	if err = store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "stale")}, ""); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("stale activation wrote", err)
	}
	newLease, err := store.Claim(ctx, scope.AgentID, "new", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := store.BeginTurn(ctx, newLease, scope, input.ID, runtimeConfig())
	if err != nil || recovered.TurnID != work.TurnID || recovered.InputID != input.ID {
		t.Fatal("restart changed identity", recovered, err)
	}
	var attemptState string
	if err = pool.QueryRow(ctx, `SELECT state FROM runtime.attempts WHERE id=$1`, attempt.ID).Scan(&attemptState); err != nil || attemptState != "unknown" {
		t.Fatal(attemptState, err)
	}
	if err = store.CancelThread(ctx, scope, main.ID); err != nil {
		t.Fatal(err)
	}
	change.RequestID = "idle-only"
	change.Interrupt = false
	old, err := store.ChangeAgentLifecycle(ctx, scope, change)
	if err != nil || old.Outcome != "deferred" {
		t.Fatal("deferred silently executed later", old, err)
	}
	if _, err = store.AcceptInput(ctx, scope, request); err != nil {
		t.Fatal("original receipt unavailable", err)
	}
}

func TestManagedAgentPauseSerializesInputAdmission(t *testing.T) {
	for _, inputFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "input_first", false: "pause_first"}[inputFirst], func(t *testing.T) {
			pool, store, scope, main := runtimeDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			var blocker int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			inputDone := make(chan error, 1)
			pauseDone := make(chan error, 1)
			var receipt managedruntime.AgentLifecycleReceipt
			submit := func() {
				_, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "racing-input", Text: "accepted once"})
				inputDone <- err
			}
			pause := func() {
				var err error
				receipt, err = store.ChangeAgentLifecycle(ctx, scope, managedruntime.AgentLifecycleChange{RequestID: "racing-pause", Action: "pause", Version: 1})
				pauseDone <- err
			}
			if inputFirst {
				if _, err = tx.Exec(ctx, `SELECT id FROM runtime.threads WHERE id=$1 FOR UPDATE`, main.ID); err != nil {
					t.Fatal(err)
				}
				go submit()
			} else {
				if _, err = tx.Exec(ctx, `LOCK TABLE runtime.lifecycle_receipts IN SHARE MODE`); err != nil {
					t.Fatal(err)
				}
				go pause()
			}
			var firstPID int
			runtimeEventually(t, func() bool {
				return pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) LIMIT 1`, blocker).Scan(&firstPID) == nil
			})
			if inputFirst {
				go pause()
			} else {
				go submit()
			}
			runtimeEventually(t, func() bool {
				var blocked bool
				return pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, firstPID).Scan(&blocked) == nil && blocked
			})
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			inputErr, pauseErr := <-inputDone, <-pauseDone
			if pauseErr != nil {
				t.Fatal(pauseErr)
			}
			if inputFirst {
				if inputErr != nil || receipt.Outcome != "deferred" {
					t.Fatal(receipt, inputErr)
				}
			} else {
				if !errors.Is(inputErr, managedruntime.ErrPaused) || receipt.Outcome != "applied" {
					t.Fatal(receipt, inputErr)
				}
			}
		})
	}
}

func TestManagedAgentPausedApplicationsAndPeerAdmission(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	source, work := prepareRuntimeCall(t, f, f.main.ID, "send", llm.Block{Type: llm.BlockToolUse, ToolUseID: "send", ToolName: "agent_send"})
	target, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Paused target"})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, target.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	main, err := f.store.EnsureAgent(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ChangeAgentLifecycle(ctx, scope, managedruntime.AgentLifecycleChange{RequestID: "pause", Action: "pause", Version: 1}); err != nil {
		t.Fatal(err)
	}
	job := applicationJob()
	if _, err = f.store.AdmitApplication(ctx, scope, job); !errors.Is(err, managedruntime.ErrPaused) {
		t.Fatal("paused Memory", err)
	}
	calendar := job
	calendar.Application = "calendar"
	calendar.ID = uuid.NewString()
	if _, err = f.store.AdmitApplication(ctx, scope, calendar); !errors.Is(err, managedruntime.ErrPaused) {
		t.Fatal("paused Calendar", err)
	}
	trigger := managedruntime.MainTrigger{ID: uuid.NewString(), Epoch: 1, Name: "Notify", Content: "original occurrence", ScheduledAt: time.Now()}
	if _, err = f.store.AdmitMainTrigger(ctx, scope, trigger); !errors.Is(err, managedruntime.ErrPaused) {
		t.Fatal("paused Main trigger", err)
	}
	action := managedruntime.ThreadAction{Kind: "agent_send", Query: "explicit peer message"}
	if _, err = f.store.ApplyThreadAction(ctx, work, action, scope); !errors.Is(err, managedruntime.ErrPaused) {
		t.Fatal("paused peer delivery", source, err)
	}
	var n int
	if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime.inputs WHERE thread_id=$1)+(SELECT count(*) FROM runtime.application_jobs WHERE agent_id=$2)+(SELECT count(*) FROM runtime.main_triggers WHERE agent_id=$2)+(SELECT count(*) FROM runtime.thread_actions WHERE target_agent_id=$2)`, main.ID, target.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("paused admission left facts", n, err)
	}
	if _, err = f.store.ChangeAgentLifecycle(ctx, scope, managedruntime.AgentLifecycleChange{RequestID: "resume", Action: "resume", Version: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ApplyThreadAction(ctx, work, action, scope); err != nil {
		t.Fatal("peer retry", err)
	}
	if _, err = f.store.AdmitApplication(ctx, scope, job); err != nil {
		t.Fatal("Memory retry", err)
	}
	if _, err = f.store.AdmitApplication(ctx, scope, calendar); err != nil {
		t.Fatal("Calendar retry", err)
	}
	if _, err = f.store.AdmitMainTrigger(ctx, scope, trigger); err != nil {
		t.Fatal("Main trigger retry", err)
	}
}

func TestManagedAgentLifecycleFirstReadHTTPAndRPCRoles(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	agent, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Never opened"})
	if err != nil {
		t.Fatal(err)
	}
	base := f.origin + "/api/tenants/" + f.tenant + "/agents/" + agent.ID
	state := managementCall[managedruntime.AgentRunState](t, f.client, "GET", base+"/run-state", f.origin, nil, 200)
	if state.Initialized || state.Version != 1 || state.Mode != "running" {
		t.Fatal(state)
	}
	var n int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.agents WHERE id=$1`, agent.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("read initialized Agent", n, err)
	}
	pause := managedruntime.AgentLifecycleChange{RequestID: "first-pause", Action: "pause", Version: state.Version}
	receipt := managementCall[managedruntime.AgentLifecycleReceipt](t, f.client, "POST", base+"/lifecycle", f.origin, pause, 200)
	if receipt.State.Mode != "paused" || !receipt.State.Initialized {
		t.Fatal(receipt)
	}
	pki := filepath.Join(t.TempDir(), "pki")
	if err = platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	listener := platformListener(t)
	server, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(pki, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	for _, role := range []string{"management", "memory", "calendar"} {
		client, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, role))
		if err != nil {
			t.Fatal(err)
		}
		runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
		value, readErr := client.AgentRunState(ctx, f.actor, f.tenant, agent.ID)
		replay, writeErr := client.ChangeAgentLifecycle(ctx, f.actor, f.tenant, agent.ID, pause)
		if role == "management" {
			if readErr != nil || writeErr != nil || value.Mode != "paused" || replay.State.Version != 2 {
				t.Fatal(role, value, replay, readErr, writeErr)
			}
		} else if !errors.Is(readErr, managedruntime.ErrDenied) || !errors.Is(writeErr, managedruntime.ErrDenied) {
			t.Fatal("role controlled lifecycle", role, readErr, writeErr)
		}
	}
}

func TestManagedAgentRestartRunnerRecoversProvider(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	cancelled := make(chan struct{})
	stopProvider := make(chan struct{})
	defer close(stopProvider)
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = io.Copy(io.Discard, r.Body)
			close(started)
			select {
			case <-r.Context().Done():
				close(cancelled)
			case <-stopProvider:
			}

			return
		}
		streamManagedReply(w, "Recovered the original input")
	})
	f.run(t)
	f.submit(t, "original-input", f.main.ID, "continue after restart")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	receipt := managementCall[managedruntime.AgentLifecycleReceipt](t, f.client, "POST", f.base+"/lifecycle", f.origin, managedruntime.AgentLifecycleChange{RequestID: "restart-provider", Action: "restart", Version: 1, Interrupt: true}, 200)
	if receipt.Outcome != "applied" {
		t.Fatal(receipt)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("old provider request did not stop")
	}
	runtimeEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	var inputs, turns, unknown, completed int
	if err := f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM runtime.inputs),(SELECT count(*) FROM runtime.turns),(SELECT count(*) FROM runtime.attempts WHERE state='unknown'),(SELECT count(*) FROM runtime.attempts WHERE state='completed')`).Scan(&inputs, &turns, &unknown, &completed); err != nil || inputs != 1 || turns != 1 || unknown != 1 || completed != 1 {
		t.Fatal("recovery identity/accounting", inputs, turns, unknown, completed, err)
	}
}

func TestManagedAgentRestartPreservesNativeOperation(t *testing.T) {
	var calls atomic.Int32
	var environment string
	dir := t.TempDir()
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			streamManagedTool(w, "exec_command", map[string]any{"environment_id": environment, "command": "printf x >> counter; while [ ! -f release ]; do sleep 0.05; done; printf 'original operation completed'"})
			return
		}
		streamManagedReply(w, "Operation settled")
	})
	device, token := f.pairDevice(t)
	environment = device.ID
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: dir, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	gateway := runtimeExecutionGateway(t, f)
	runRuntimeTools(t, f, gateway)
	f.submit(t, "one-shell", f.main.ID, "execute one command")
	runtimeEventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(dir, "counter"))
		return err == nil && string(data) == "x"
	})
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM runtime.tools WHERE request->>'kind'='exec_command'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	receipt := managementCall[managedruntime.AgentLifecycleReceipt](t, f.client, "POST", f.base+"/lifecycle", f.origin, managedruntime.AgentLifecycleChange{RequestID: "restart-tool", Action: "restart", Version: 1, Interrupt: true}, 200)
	if receipt.Outcome != "applied" {
		t.Fatal(receipt)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), []byte("continue"), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	data, err := os.ReadFile(filepath.Join(dir, "counter"))
	if err != nil || string(data) != "x" {
		t.Fatal("operation replayed", string(data), err)
	}
	var tools, operations int
	if err = f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM runtime.tools WHERE id=$1::uuid),(SELECT count(*) FROM execution.operations WHERE id=$1::text AND state='completed')`, id).Scan(&tools, &operations); err != nil || tools != 1 || operations != 1 {
		t.Fatal("original receipt lost", tools, operations, err)
	}
}

func TestManagedAgentResumeOrdersDelayedPause(t *testing.T) {
	_, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	receipt, err := store.ChangeAgentLifecycle(ctx, scope, managedruntime.AgentLifecycleChange{RequestID: "remain-running", Action: "resume", Version: 1})
	if err != nil || receipt.State.Version != 2 || receipt.State.ActivationEpoch != 0 {
		t.Fatal(receipt, err)
	}
	if _, err = store.ChangeAgentLifecycle(ctx, scope, managedruntime.AgentLifecycleChange{RequestID: "late-pause", Action: "pause", Version: 1}); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("late pause overrode explicit resume", err)
	}
}

func TestManagedAgentPausedObserverRetainsDeliveryAndRearm(t *testing.T) {
	f, _, _, request, _ := observerFixture(t, `printf '%s\n' '{"message":"one"}'`)
	ctx := context.Background()
	request.Mode, request.Subscribe = "continuous", true
	control, err := f.service.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	work, err := f.store.ClaimObserver(ctx, "rearm")
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.store.ClaimObservation(ctx, "observe")
	if err != nil {
		t.Fatal(err)
	}
	fact := managedruntime.Observation{ID: uuid.NewString(), Kind: "command.observation", EnvironmentID: source.EnvironmentID, OperationID: source.OperationID, Offset: 10, Data: json.RawMessage(`{"text":"one original fact"}`), CreatedAt: time.Now().UTC()}
	if err = f.store.FinishObservation(ctx, source, managedruntime.ObservationBatch{Cursor: 10, Closed: true, Facts: []managedruntime.Observation{fact}}); err != nil {
		t.Fatal(err)
	}
	delivery, err := f.store.ClaimObservationDelivery(ctx, "delivery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ChangeAgentLifecycle(ctx, scope, managedruntime.AgentLifecycleChange{RequestID: "pause", Action: "pause", Version: 1, Interrupt: true}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishObservationDelivery(ctx, delivery, true, nil); !errors.Is(err, managedruntime.ErrPaused) {
		t.Fatal("paused delivery", err)
	}
	var pending, enabled bool
	if err = f.pool.QueryRow(ctx, `SELECT d.state='pending',s.enabled FROM runtime.observation_deliveries d JOIN runtime.subscriptions s ON s.id=d.subscription_id WHERE d.id=$1`, delivery.ID).Scan(&pending, &enabled); err != nil || !pending || !enabled {
		t.Fatal("pause lost pending intent", pending, enabled, err)
	}
	if err = f.store.ConfirmObservationAck(ctx, managedruntime.ObservationAck{SourceID: source.ID, Cursor: 10}); err != nil {
		t.Fatal("pause blocked ACK", err)
	}
	if err = f.store.FinishObserver(ctx, work, managedruntime.ObserverOutcome{State: "completed", Admitted: true, Restart: true}); err != nil {
		t.Fatal(err)
	}
	var original, desired string
	if err = f.pool.QueryRow(ctx, `SELECT source_id,desired FROM runtime.observer_controls WHERE id=$1`, control.ID).Scan(&original, &desired); err != nil || original != control.SourceID || desired != "running" {
		t.Fatal("paused rearm changed identity/intent", original, desired, err)
	}
	rejected := request
	rejected.RequestID = uuid.NewString()
	if _, err = f.service.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, rejected); !errors.Is(err, managedruntime.ErrPaused) {
		t.Fatal("new observer while paused", err)
	}
	if _, err = f.store.ChangeAgentLifecycle(ctx, scope, managedruntime.AgentLifecycleChange{RequestID: "resume", Action: "resume", Version: 2}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishObservationDelivery(ctx, delivery, true, nil); err != nil {
		t.Fatal("delivery retry", err)
	}
	var n int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'observation_id'=$1`, fact.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	// Expedite only this fixture's timer; production retains its 5s minimum.
	if _, err = f.pool.Exec(ctx, `UPDATE runtime.observer_controls SET next_check=clock_timestamp() WHERE id=$1`, control.ID); err != nil {
		t.Fatal(err)
	}
	next, err := f.store.ClaimObserver(ctx, "resume-rearm")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishObserver(ctx, next, managedruntime.ObserverOutcome{State: "completed", Admitted: true, Restart: true}); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT source_id FROM runtime.observer_controls WHERE id=$1`, control.ID).Scan(&original); err != nil || original == control.SourceID {
		t.Fatal("resume did not rearm", original, err)
	}
}
