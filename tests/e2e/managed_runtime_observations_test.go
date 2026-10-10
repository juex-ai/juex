//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func TestManagedRuntimeObservationShutdownDuringSourceWake(t *testing.T) {
	f := executionDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	device, _ := f.pairDevice(t, execprotocol.MCP)
	_, job := prepareRuntimeTool(t, f, device.ID)
	request := execprotocol.Request{Version: execprotocol.Version, ID: job.ID, AgentID: f.agent.ID, Kind: "mcp_connect", Arguments: json.RawMessage(`{"command":"fixture"}`)}
	if err := f.store.PrepareTool(ctx, job, device.ID, request); err != nil {
		t.Fatal(err)
	}
	source, err := f.store.ClaimObservation(ctx, "stopping-observer")
	if err != nil {
		t.Fatal(err)
	}
	releaseDuringRuntimeWake(t, ctx, f, "observation_sources", source.ID, "WITH sources AS%", func() error {
		return f.store.ReleaseObservationClaims(ctx, "stopping-observer")
	})
	next, err := f.store.ClaimObservation(ctx, "restarted-observer")
	if err != nil || next.ID != source.ID || next.LeaseEpoch <= source.LeaseEpoch || next.WakeVersion != source.WakeVersion+1 {
		t.Fatal("restart could not reclaim the preserved wake immediately", next, err)
	}
	if err := f.store.FinishObservation(ctx, source, managedruntime.ObservationBatch{}); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("stopped worker retained its write fence", err)
	}
}

func TestManagedRuntimeMCPObservationsOutliveActivationAndRestart(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	var connection, subscription atomic.Value
	var deviceID string
	workdir := t.TempDir()
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			streamManagedTool(w, "mcp_connect", map[string]any{"environment_id": deviceID, "command": os.Args[0], "args": []string{"-test.run=^TestNativeExecutorMCPHelper$"}, "environment": map[string]string{"JUEX_NATIVE_MCP_HELPER": "1", "JUEX_NATIVE_MCP_COUNTER": filepath.Join(workdir, "counter")}})
		case 3:
			streamManagedTool(w, "subscribe", map[string]any{"kind": "mcp.notification", "environment_id": deviceID, "operation_id": connection.Load().(string), "method": "notifications/claude/channel"})
		case 5:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			encoded, _ := json.Marshal(body["messages"])
			if !strings.Contains(string(encoded), "after-call") || !strings.Contains(string(encoded), "External observation") {
				t.Error("missing subscribed event provenance")
			}
			streamManagedReply(w, "Notification handled")
		case 6:
			streamManagedTool(w, "unsubscribe", map[string]any{"subscription_id": subscription.Load().(string)})
		default:
			streamManagedReply(w, "Ready")
		}
	})
	device, token := f.pairDevice(t, execprotocol.MCP)
	deviceID = device.ID
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: workdir, EnvironmentID: device.ID, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	gateway := runtimeExecutionGateway(t, f)
	interrupted := &interruptedObservationStore{Store: f.store, claimed: make(chan struct{})}
	stop := runRuntimeToolsStore(t, f, gateway, interrupted)
	f.submit(t, "connect", f.main.ID, "Connect to the MCP server")
	runtimeEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	var id string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM runtime.tools WHERE request->>'kind'='mcp_connect'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	connection.Store(id)
	runtimeEventually(t, func() bool {
		var released bool
		return f.pool.QueryRow(ctx, `SELECT holder='' FROM runtime.agents WHERE id=$1`, f.agent.ID).Scan(&released) == nil && released
	})
	emit := func(name string) {
		t.Helper()
		request := nativeRequest(t, name, "mcp_call", native.MCPArguments{ConnectionID: id, Name: "echo", Arguments: map[string]any{"text": "notify"}})
		request.AgentID = f.agent.ID
		if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil {
			t.Fatal(err)
		}
		executionEventually(t, f, device.ID, name, func(op execution.Operation) bool { return op.State == "completed" })
	}
	waitFacts := func(count int) {
		t.Helper()
		runtimeEventually(t, func() bool {
			var got int
			return f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.observations WHERE kind='mcp.notification'`).Scan(&got) == nil && got == count
		})
	}
	// Stop after PostgreSQL has acquired a source lease but before the worker
	// receives its result. Restart must not wait for that graceful owner's TTL.
	interrupted.armed.Store(true)
	if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{{ID: uuid.NewString(), Kind: "operation.updated", TenantID: f.tenant, UserID: f.actor, EnvironmentID: device.ID, OperationID: id, CreatedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-interrupted.claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("observation worker did not acquire the interrupted lease")
	}
	stop()
	emit("no-subscription")
	stop = runRuntimeTools(t, f, gateway)
	waitFacts(1)
	if calls.Load() != 2 {
		t.Fatal("unsubscribed notification woke model", calls.Load())
	}
	f.submit(t, "subscribe", f.main.ID, "Subscribe to future MCP messages")
	runtimeEventually(t, func() bool { return calls.Load() == 4 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	var subID string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM runtime.subscriptions WHERE enabled`).Scan(&subID); err != nil {
		t.Fatal(err)
	}
	subscription.Store(subID)
	stop()
	emit("while-runtime-stopped")
	stop = runRuntimeTools(t, f, gateway)
	runtimeEventually(t, func() bool { return calls.Load() == 5 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	waitFacts(2)
	var observed, consumed int64
	if err := f.pool.QueryRow(ctx, `SELECT observed_bytes FROM execution.operations WHERE id=$1`, id).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool {
		return f.pool.QueryRow(ctx, `SELECT cursor FROM runtime.observation_sources WHERE operation_id=$1`, id).Scan(&consumed) == nil && consumed > 0 && f.pool.QueryRow(ctx, `SELECT observed_bytes FROM execution.operations WHERE id=$1`, id).Scan(&observed) == nil && observed == consumed
	})
	var source managedruntime.InputSource
	var raw []byte
	if err := f.pool.QueryRow(ctx, `SELECT source FROM runtime.inputs WHERE source->>'kind'='observation'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &source) != nil || source.SubscriptionID != subID || source.EnvironmentID != device.ID {
		t.Fatal(string(raw))
	}
	var provenance bool
	for _, event := range f.timeline(t, f.main.ID).Events {
		if event.Kind == "message.appended" {
			var message llm.Message
			_ = json.Unmarshal(event.Data, &message)
			if message.Kind == llm.MessageKindSystemNotice && strings.Contains(message.FirstText(), source.ObservationID) {
				provenance = true
			}
		}
	}
	if !provenance {
		t.Fatal("notification represented as direct user input")
	}
	// Duplicated transport hints may trigger another read, never another fact/input.
	event := execprotocol.Event{ID: uuid.NewString(), Kind: "operation.updated", TenantID: f.tenant, UserID: f.actor, EnvironmentID: device.ID, OperationID: id, CreatedAt: time.Now()}
	for range 2 {
		if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{event}); err != nil {
			t.Fatal(err)
		}
	}
	f.submit(t, "unsubscribe", f.main.ID, "Stop the subscription")
	runtimeEventually(t, func() bool { return calls.Load() == 7 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	emit("after-unsubscribe")
	waitFacts(3)
	stop()
	if calls.Load() != 7 {
		t.Fatal("notification woke disabled subscription", calls.Load())
	}
	var inputs, connections int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='observation'`).Scan(&inputs); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations WHERE request->>'kind'='mcp_connect'`).Scan(&connections); err != nil {
		t.Fatal(err)
	}
	if inputs != 1 || connections != 1 {
		t.Fatal("replayed wakeup or MCP connection", inputs, connections)
	}
	assertRuntimeTranscript(t, f)
}

type interruptedObservationStore struct {
	*runtimepg.Store
	armed   atomic.Bool
	claimed chan struct{}
}

func (s *interruptedObservationStore) ClaimObservation(ctx context.Context, holder string) (managedruntime.ObservationSource, error) {
	source, err := s.Store.ClaimObservation(ctx, holder)
	if err == nil && s.armed.CompareAndSwap(true, false) {
		close(s.claimed)
		<-ctx.Done()
		return source, ctx.Err()
	}
	return source, err
}

func TestManagedRuntimeObservationLeaseHandoffFencesPreviousOwner(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	_, job := prepareRuntimeTool(t, f, device.ID)
	if _, err := f.store.ApplySubscription(ctx, job, managedruntime.SubscriptionRequest{Kind: "environment.presence", EnvironmentID: device.ID}, device.Version, 0, "", json.RawMessage(`{"online":false}`)); err != nil {
		t.Fatal(err)
	}
	previous, err := f.store.ClaimObservationDelivery(ctx, "previous-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReleaseObservationClaims(ctx, ""); !errors.Is(err, managedruntime.ErrInvalid) {
		t.Fatal("empty owner must not release idle rows", err)
	}
	if err := f.store.ReleaseObservationClaims(ctx, "another-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimObservationDelivery(ctx, "new-owner"); !errors.Is(err, managedruntime.ErrNoWork) {
		t.Fatal("released another worker's claim", err)
	}
	if err := f.store.ReleaseObservationClaims(ctx, "previous-owner"); err != nil {
		t.Fatal(err)
	}
	current, err := f.store.ClaimObservationDelivery(ctx, "new-owner")
	if err != nil || current.ID != previous.ID {
		t.Fatal("pending notification was not reclaimed", current.ID, err)
	}
	if err := f.store.FinishObservationDelivery(ctx, previous, true, json.RawMessage(`{"online":true}`)); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("previous owner completed the reclaimed notification", err)
	}
	if err := f.store.FinishObservationDelivery(ctx, current, true, json.RawMessage(`{"online":true}`)); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRuntimeObservationCancellationFencesClaimedDelivery(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	scope, job := prepareRuntimeTool(t, f, device.ID)
	sub, err := f.store.ApplySubscription(ctx, job, managedruntime.SubscriptionRequest{Kind: "environment.presence", EnvironmentID: device.ID}, device.Version, 0, "", json.RawMessage(`{"online":false}`))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := f.store.ClaimObservationDelivery(ctx, "before-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CancelThread(ctx, scope, f.main.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishObservationDelivery(ctx, delivery, true, json.RawMessage(`{"online":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplySubscription(ctx, job, managedruntime.SubscriptionRequest{Kind: "environment.presence", EnvironmentID: device.ID}, device.Version, 0, "", json.RawMessage(`{}`)); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("cancelled tool reenabled subscription", err)
	}
	var enabled bool
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT enabled FROM runtime.subscriptions WHERE id=$1`, sub.ID).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='observation'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if enabled || count != 0 || f.timeline(t, f.main.ID).Thread.State != "idle" {
		t.Fatal("late event revived cancelled Thread", enabled, count)
	}
}

func TestManagedRuntimeObservationSubscriptionsFanOutAndCoalescePresence(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	scope, mainJob := prepareRuntimeTool(t, f, device.ID)
	worker, err := f.store.CreateWorker(ctx, scope, f.main.ID, "observing-worker", "Observer")
	if err != nil {
		t.Fatal(err)
	}
	_, workerJob := prepareRuntimeThreadTool(t, f, worker.ID, device.ID, "worker-subscribe")
	for _, job := range []managedruntime.ToolWork{mainJob, workerJob} {
		if _, err := f.store.ApplySubscription(ctx, job, managedruntime.SubscriptionRequest{Kind: "environment.presence", EnvironmentID: device.ID}, device.Version, 0, "", json.RawMessage(`{"online":false}`)); err != nil {
			t.Fatal(err)
		}
	}
	drain := func(valid bool, state string) {
		t.Helper()
		for {
			delivery, err := f.store.ClaimObservationDelivery(ctx, "presence-worker")
			if errors.Is(err, managedruntime.ErrNoWork) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.FinishObservationDelivery(ctx, delivery, valid, json.RawMessage(state)); err != nil {
				t.Fatal(err)
			}
		}
	}
	drain(true, `{"online":false}`)
	event := execprotocol.Event{ID: uuid.NewString(), Kind: "environment.presence", TenantID: f.tenant, UserID: f.actor, EnvironmentID: device.ID, AgentIDs: []string{f.agent.ID}, Data: json.RawMessage(`{"online":true}`), CreatedAt: time.Now()}
	for range 2 {
		if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{event}); err != nil {
			t.Fatal(err)
		}
	}
	drain(true, `{"online":true}`)
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='observation'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("one input per subscriber", count, err)
	}
	event.ID = uuid.NewString()
	event.Data = json.RawMessage(`{"online":false}`)
	event.CreatedAt = time.Now().Add(-time.Hour)
	if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{event}); err != nil {
		t.Fatal(err)
	}
	drain(true, `{"online":true}`)
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='observation'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("stale presence woke subscribers", count, err)
	}
	event.ID = uuid.NewString()
	if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{event}); err != nil {
		t.Fatal(err)
	}
	drain(false, `{}`)
	// Delivery invalidation can commit before a subscription tool's result is
	// acknowledged. Retrying that action must not adopt the newer device grant.
	if _, err := f.store.ApplySubscription(ctx, mainJob, managedruntime.SubscriptionRequest{Kind: "environment.presence", EnvironmentID: device.ID}, device.Version+1, 0, "", json.RawMessage(`{"online":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='observation' AND state='queued'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked subscriptions retained queued inputs", count, err)
	}
	var enabled int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.subscriptions WHERE enabled`).Scan(&enabled); err != nil || enabled != 0 {
		t.Fatal("revoked subscriptions remain enabled", enabled, err)
	}
	wrong := scope
	wrong.UserID = uuid.NewString()
	if _, err := f.store.Observation(ctx, wrong, event.ID); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("cross-owner observation read", err)
	}
}

func TestManagedRuntimeObservationWakeCannotSurviveDeviceReauthorization(t *testing.T) {
	ctx := context.Background()
	var deviceID string
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			streamManagedTool(w, "subscribe", map[string]any{"kind": "environment.presence", "environment_id": deviceID})
			return
		}
		streamManagedReply(w, "Subscribed")
	})
	device, _ := f.pairDevice(t)
	deviceID = device.ID
	gateway := runtimeExecutionGateway(t, f)
	stop := runRuntimeTools(t, f, gateway)
	f.submit(t, "subscribe", f.main.ID, "Subscribe to device presence")
	runtimeEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	stop()
	for {
		delivery, err := f.store.ClaimObservationDelivery(ctx, "initial")
		if errors.Is(err, managedruntime.ErrNoWork) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.FinishObservationDelivery(ctx, delivery, true, json.RawMessage(`{"online":false,"availability":""}`)); err != nil {
			t.Fatal(err)
		}
	}
	event := execprotocol.Event{ID: uuid.NewString(), Kind: "environment.presence", TenantID: f.tenant, UserID: f.actor, EnvironmentID: device.ID, AgentIDs: []string{f.agent.ID}, Data: json.RawMessage(`{"online":true}`), CreatedAt: time.Now()}
	if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{event}); err != nil {
		t.Fatal(err)
	}
	delivery, err := f.store.ClaimObservationDelivery(ctx, "online")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishObservationDelivery(ctx, delivery, true, json.RawMessage(`{"online":true,"availability":""}`)); err != nil {
		t.Fatal(err)
	}
	restricted, err := f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, device.Version, map[string][]execprotocol.Capability{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, restricted.Version, device.Grants); err != nil {
		t.Fatal(err)
	}
	stop = runRuntimeTools(t, f, gateway)
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='observation' AND state IN ('held','cancelled')`).Scan(&count) == nil && count == 1
	})
	stop()
	if calls.Load() != 2 {
		t.Fatal("old event ran after device grant restoration", calls.Load())
	}
}

func TestManagedRuntimeObservationSourceFencesAndPersistsPartialRecord(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t, execprotocol.MCP)
	_, job := prepareRuntimeTool(t, f, device.ID)
	request := execprotocol.Request{Version: execprotocol.Version, ID: job.ID, AgentID: f.agent.ID, Kind: "mcp_connect", Arguments: json.RawMessage(`{"command":"fixture"}`)}
	if err := f.store.PrepareTool(ctx, job, device.ID, request); err != nil {
		t.Fatal(err)
	}
	old, err := f.store.ClaimObservation(ctx, "old-observer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.observation_sources SET lease_until='-infinity' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.store.ClaimObservation(ctx, "replacement")
	if err != nil {
		t.Fatal(err)
	}
	batch := managedruntime.ObservationBatch{Cursor: 4, Pending: []byte("part"), More: true}
	if err := f.store.FinishObservation(ctx, old, batch); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("old observer wrote", err)
	}
	if err := f.store.FinishObservation(ctx, fresh, batch); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.store.ClaimObservation(ctx, "after-restart")
	if err != nil || recovered.Cursor != 4 || string(recovered.Pending) != "part" {
		t.Fatal(recovered, err)
	}
	acks, err := f.store.ObservationAcks(ctx, 100)
	if err != nil || len(acks) != 1 || acks[0].Cursor != 4 {
		t.Fatal("partial record was not handed off durably", acks, err)
	}
	if err := f.store.ConfirmObservationAck(ctx, acks[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishObservation(ctx, recovered, managedruntime.ObservationBatch{Cursor: 8, Closed: true, Facts: []managedruntime.Observation{{ID: uuid.NewString(), Kind: "operation.terminal", EnvironmentID: device.ID, OperationID: job.ID, Offset: 8, Data: json.RawMessage(`{"state":"failed"}`), CreatedAt: time.Now()}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimObservation(ctx, "closed"); !errors.Is(err, managedruntime.ErrNoWork) {
		t.Fatal("closed observer reclaimed", err)
	}
}

func TestManagedRuntimeObservationAcknowledgmentDuringCursorAdvance(t *testing.T) {
	f := executionDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	device, _ := f.pairDevice(t, execprotocol.MCP)
	_, job := prepareRuntimeTool(t, f, device.ID)
	request := execprotocol.Request{Version: execprotocol.Version, ID: job.ID, AgentID: f.agent.ID, Kind: "mcp_connect", Arguments: json.RawMessage(`{"command":"fixture"}`)}
	if err := f.store.PrepareTool(ctx, job, device.ID, request); err != nil {
		t.Fatal(err)
	}
	source, err := f.store.ClaimObservation(ctx, "observer")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishObservation(ctx, source, managedruntime.ObservationBatch{Cursor: 4, More: true}); err != nil {
		t.Fatal(err)
	}
	acks, err := f.store.ObservationAcks(ctx, 100)
	if err != nil || len(acks) != 1 || acks[0].Cursor != 4 {
		t.Fatal("missing first acknowledgment", acks, err)
	}

	// Hold the next page's cursor update until the previous acknowledgment is
	// waiting on that row, so concurrency does not depend on scheduler timing.
	writer, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	var writerPID uint32
	if err := writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `UPDATE runtime.observation_sources SET cursor=8 WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	confirmed := make(chan error, 1)
	go func() { confirmed <- f.store.ConfirmObservationAck(ctx, acks[0]) }()
	runtimeEventually(t, func() bool {
		var waiting bool
		return f.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND query LIKE 'UPDATE runtime.observation_sources SET confirmed_cursor=%')`, writerPID).Scan(&waiting) == nil && waiting
	})
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-confirmed; err != nil {
		t.Fatal("concurrent page update prevented acknowledgment", err)
	}
	var cursor, confirmedCursor int64
	if err := f.pool.QueryRow(ctx, `SELECT cursor,confirmed_cursor FROM runtime.observation_sources WHERE id=$1`, source.ID).Scan(&cursor, &confirmedCursor); err != nil || cursor != 8 || confirmedCursor != 4 {
		t.Fatal("acknowledgment changed the unread page boundary", cursor, confirmedCursor, err)
	}
	next, err := f.store.ObservationAcks(ctx, 100)
	if err != nil || len(next) != 1 || next[0].Cursor != 8 {
		t.Fatal("next page was not acknowledged independently", next, err)
	}
	if err := f.store.ConfirmObservationAck(ctx, next[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ConfirmObservationAck(ctx, acks[0]); err != nil {
		t.Fatal("delayed acknowledgment retry", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT cursor,confirmed_cursor FROM runtime.observation_sources WHERE id=$1`, source.ID).Scan(&cursor, &confirmedCursor); err != nil || cursor != 8 || confirmedCursor != 8 {
		t.Fatal("delayed acknowledgment moved the confirmed cursor backward", cursor, confirmedCursor, err)
	}
}
