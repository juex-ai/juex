//go:build postgres

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/managed"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution/native"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func streamManagedTool(w http.ResponseWriter, name string, arguments map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	encoded, _ := json.Marshal(arguments)
	chunk, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_fixture", "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}}, "finish_reason": nil}}})
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":8}}\n\ndata: [DONE]\n\n", chunk)
}

func runtimeExecutionGateway(t *testing.T, f *executionFixture) managed.RuntimeTools {
	t.Helper()
	listener := platformListener(t)
	server, err := serverrpc.NewExecution(listener, platformrpc.CredentialsAt(f.credentials, "execution"), f.execution, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := executionrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(context.Background()) == nil })
	return managed.RuntimeTools{Client: client}
}

func runRuntimeTools(t *testing.T, f *executionFixture, gateway managedruntime.ToolGateway) func() {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			for _, table := range []string{"threads", "attempts", "tools"} {
				var data []byte
				if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(v)-'request'),'[]') FROM runtime.`+table+` v`).Scan(&data); err == nil {
					t.Log(table, string(data))
				}
			}
		}
	})
	config := managedruntime.RunnerConfig{Concurrency: 1, PollInterval: 20 * time.Millisecond, AuthorityInterval: 20 * time.Millisecond, IdleTimeout: 100 * time.Millisecond, Tools: gateway}
	config.Files, _ = gateway.(managedruntime.FileGateway)
	runner, err := managedruntime.NewRunner(f.store, f.authority, config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("tool Runtime shutdown timed out")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func assertRuntimeTranscript(t *testing.T, f *executionFixture) {
	t.Helper()
	var history []llm.Message
	for _, event := range f.timeline(t, f.main.ID).Events {
		if event.Kind == "message.appended" {
			var message llm.Message
			if err := json.Unmarshal(event.Data, &message); err != nil {
				t.Fatal(err)
			}
			history = append(history, message)
		}
	}
	if err := llm.ValidateToolTranscript(history); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRuntimeOfflineToolsReleaseSlotAndResumeAfterRestart(t *testing.T) {
	var deviceID string
	var mainCalls, workerCalls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		encoded, _ := json.Marshal(body["messages"])
		if strings.Contains(string(encoded), "worker-only") {
			workerCalls.Add(1)
			streamManagedReply(w, "Worker completed while Main waits")
			return
		}
		if mainCalls.Add(1) == 1 {
			if !strings.Contains(string(encoded), deviceID) || body["tools"] == nil {
				t.Error("model lacks authorized environment/tool context")
			}
			streamManagedTool(w, "read", map[string]any{"environment_id": deviceID, "path": "result.txt"})
		} else {
			if !strings.Contains(string(encoded), "durable result") || !strings.Contains(string(encoded), "call_fixture") {
				t.Error("tool result missing from resumed model context", string(encoded))
			}
			streamManagedReply(w, "Read the device file")
		}
	})
	ctx := context.Background()
	device, token := f.pairDevice(t)
	deviceID = device.ID
	gateway := runtimeExecutionGateway(t, f)
	stop := runRuntimeTools(t, f, gateway)
	f.submit(t, "main-read", f.main.ID, "Read the file on my device")
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.tools WHERE state='waiting' AND next_check='infinity'`).Scan(&count) == nil && count == 1
	})
	var observationID string
	if err := f.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&observationID); err != nil {
		t.Fatal(err)
	}
	observation := execprotocol.Event{ID: observationID, Kind: "environment.presence", TenantID: f.tenant, UserID: f.actor, EnvironmentID: device.ID, AgentIDs: []string{f.agent.ID}, Data: json.RawMessage(`{"name":"Main notification","online":false}`), CreatedAt: time.Now()}
	for range 2 {
		if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{observation}); err != nil {
			t.Fatal(err)
		}
	}
	worker, err := f.service.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "parallel-worker", "Independent")
	if err != nil {
		t.Fatal(err)
	}
	f.submit(t, "worker-input", worker.ID, "worker-only")
	runtimeEventually(t, func() bool { return workerCalls.Load() == 1 && f.timeline(t, worker.ID).Thread.State == "idle" })
	if mainCalls.Load() != 1 {
		t.Fatal("offline tool spun model requests")
	}
	var observationPending bool
	if err := f.pool.QueryRow(ctx, `SELECT consumed_at IS NULL FROM runtime.observations WHERE event_id=$1`, observationID).Scan(&observationPending); err != nil || !observationPending {
		t.Fatal("Worker consumed Main's observation", err)
	}
	var operationID string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM runtime.tools`).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	stop()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "result.txt"), []byte("durable result"), 0600); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: work, EnvironmentID: device.ID, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	runRuntimeTools(t, f, gateway)
	runtimeEventually(t, func() bool { return mainCalls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	var turns, tools, attempts, operations int
	for query, target := range map[string]*int{`SELECT count(*) FROM runtime.turns WHERE thread_id=$1`: &turns, `SELECT count(*) FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1`: &tools, `SELECT count(*) FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1`: &attempts} {
		if err := f.pool.QueryRow(ctx, query, f.main.ID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations WHERE id=$1`, operationID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if turns != 1 || tools != 1 || attempts != 2 || operations != 1 {
		t.Fatal("recovery duplicated durable work", turns, tools, attempts, operations)
	}
	assertRuntimeTranscript(t, f)
	if err := f.pool.QueryRow(ctx, `SELECT consumed_at IS NULL FROM runtime.observations WHERE event_id=$1`, observationID).Scan(&observationPending); err != nil || observationPending {
		t.Fatal("Main did not consume its durable observation", err)
	}
}

func TestManagedRuntimeCancellationClosesToolTranscriptAndStopsOfflineWork(t *testing.T) {
	var deviceID string
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			streamManagedTool(w, "write", map[string]any{"environment_id": deviceID, "path": "must-not-exist", "content": "forbidden"})
			return
		}
		streamManagedReply(w, "New input handled")
	})
	ctx := context.Background()
	device, token := f.pairDevice(t)
	deviceID = device.ID
	gateway := runtimeExecutionGateway(t, f)
	runRuntimeTools(t, f, gateway)
	f.submit(t, "cancel-me", f.main.ID, "Write the file")
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations WHERE state='waiting'`).Scan(&count) == nil && count == 1
	})
	if err := f.service.Cancel(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations WHERE state='cancelled'`).Scan(&count) == nil && count == 1
	})
	work := t.TempDir()
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: work, EnvironmentID: device.ID, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	f.submit(t, "after-cancel", f.main.ID, "Continue with this new instruction")
	runtimeEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	if _, err := os.Stat(filepath.Join(work, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("cancelled offline effect ran", err)
	}
	assertRuntimeTranscript(t, f)
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.Operation(ctx, scope, device.ID, "absent-operation", 0); !errors.Is(err, execprotocol.ErrNotFound) {
		t.Fatal("absent owned operation cannot be reconciled", err)
	}
}

func TestManagedRuntimeCancelCompletedTurnStopsItsBackgroundOperations(t *testing.T) {
	var deviceID string
	var mainCalls, workerCalls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		encoded, _ := json.Marshal(body["messages"])
		calls := &mainCalls
		if strings.Contains(string(encoded), "worker-background") {
			calls = &workerCalls
		}
		if calls.Add(1) == 1 {
			streamManagedTool(w, "exec_command", map[string]any{"environment_id": deviceID, "command": "printf ready; sleep 60"})
			return
		}
		streamManagedReply(w, "Background process started")
	})
	ctx := context.Background()
	device, token := f.pairDevice(t)
	deviceID = device.ID
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: device.ID, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	f.submit(t, "main-background", f.main.ID, "Run a background process")
	runtimeEventually(t, func() bool { return mainCalls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	worker, err := f.service.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "background-worker", "Independent process")
	if err != nil {
		t.Fatal(err)
	}
	f.submit(t, "worker-background", worker.ID, "worker-background")
	runtimeEventually(t, func() bool { return workerCalls.Load() == 2 && f.timeline(t, worker.ID).Thread.State == "idle" })
	if err := f.service.Cancel(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool {
		var cancelled bool
		return f.pool.QueryRow(ctx, `SELECT o.state='cancelled' FROM execution.operations o JOIN runtime.tools j ON j.id::text=o.id JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1`, f.main.ID).Scan(&cancelled) == nil && cancelled
	})
	var workerRunning, mainCompleted bool
	if err := f.pool.QueryRow(ctx, `SELECT o.state='running' AND NOT o.cancel_requested FROM execution.operations o JOIN runtime.tools j ON j.id::text=o.id JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1`, worker.ID).Scan(&workerRunning); err != nil || !workerRunning {
		t.Fatal("cancellation reached independent Worker", err, workerRunning)
	}
	if err := f.pool.QueryRow(ctx, `SELECT state='completed' FROM runtime.turns WHERE thread_id=$1`, f.main.ID).Scan(&mainCompleted); err != nil || !mainCompleted {
		t.Fatal("cancellation rewrote completed history", err, mainCompleted)
	}
	assertRuntimeTranscript(t, f)
}

func prepareRuntimeTool(t *testing.T, f *executionFixture, device string) (managedruntime.Scope, managedruntime.ToolWork) {
	t.Helper()
	return prepareRuntimeThreadTool(t, f, f.main.ID, device, "prepared-input")
}

func prepareRuntimeThreadTool(t *testing.T, f *executionFixture, thread, device, requestID string) (managedruntime.Scope, managedruntime.ToolWork) {
	t.Helper()
	call := llm.Block{Type: llm.BlockToolUse, ToolUseID: "prepared-call", ToolName: "read", Input: map[string]any{"environment_id": device, "path": "result.txt"}}
	return prepareRuntimeCall(t, f.managedRuntimeFixture, thread, requestID, call)
}

func prepareRuntimeCall(t *testing.T, f *managedRuntimeFixture, thread, requestID string, call llm.Block) (managedruntime.Scope, managedruntime.ToolWork) {
	t.Helper()
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: requestID, ThreadID: thread, Text: "Read the device file"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.Claim(ctx, f.agent.ID, "before-crash", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	work, err := f.store.BeginTurn(ctx, lease, scope, receipt.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := f.store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Messages: work.History})
	if err != nil {
		t.Fatal(err)
	}
	response := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{call}}}
	if err := f.store.FinishAttempt(ctx, lease, attempt.ID, response, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	job, err := f.store.ClaimTool(ctx, "lost-delivery-worker")
	if err != nil {
		t.Fatal(err)
	}
	return scope, job
}

func TestManagedRuntimePreparedToolCrashAndUnknownOutcome(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			var calls atomic.Int32
			f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				streamManagedReply(w, "Recovered prepared operation")
			})
			ctx := context.Background()
			device, token := f.pairDevice(t)
			gateway := runtimeExecutionGateway(t, f)
			scope, job := prepareRuntimeTool(t, f, device.ID)
			request := execprotocol.Request{Version: execprotocol.Version, ID: job.ID, AgentID: f.agent.ID, Kind: "read", Arguments: json.RawMessage(`{"path":"result.txt"}`)}
			if err := f.store.PrepareTool(ctx, job, device.ID, request); err != nil {
				t.Fatal(err)
			}
			if unknown {
				if _, err := gateway.Submit(ctx, scope, device.ID, request); err != nil {
					t.Fatal(err)
				}
				connected, err := f.executionStore.Connect(ctx, device.ID, rand.Text())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, job.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.executionStore.Connect(ctx, device.ID, rand.Text()); !errors.Is(err, execprotocol.ErrDenied) {
					t.Fatal(err)
				}
			} else {
				work := t.TempDir()
				if err := os.WriteFile(filepath.Join(work, "result.txt"), []byte("durable result"), 0600); err != nil {
					t.Fatal(err)
				}
				engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: work, EnvironmentID: device.ID, Grants: device.Ceiling})
				connectExecutionDevice(t, f, device, token, engine)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE runtime.tools SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
				t.Fatal(err)
			}
			runRuntimeTools(t, f, gateway)
			if unknown {
				runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "blocked" })
				if calls.Load() != 0 {
					t.Fatal("unknown external effect caused automatic model retry")
				}
			} else {
				runtimeEventually(t, func() bool { return calls.Load() == 1 && f.timeline(t, f.main.ID).Thread.State == "idle" })
				assertRuntimeTranscript(t, f)
			}
			if err := f.store.FinishTool(ctx, job, managedruntime.ToolOutcome{State: "ready", Content: "stale response"}); !errors.Is(err, managedruntime.ErrFence) {
				t.Fatal("expired delivery worker committed", err)
			}
			var count int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations WHERE id=$1`, job.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("lost stable operation identity", count, err)
			}
		})
	}
}

func TestManagedRuntimeToolCancellationBeforePreparation(t *testing.T) {
	f := executionDatabase(t)
	device, _ := f.pairDevice(t)
	scope, job := prepareRuntimeTool(t, f, device.ID)
	ctx := context.Background()
	if err := f.store.CancelThread(ctx, scope, f.main.ID); err != nil {
		t.Fatal(err)
	}
	request := execprotocol.Request{Version: execprotocol.Version, ID: job.ID, AgentID: f.agent.ID, Kind: "read", Arguments: json.RawMessage(`{"path":"result.txt"}`)}
	if err := f.store.PrepareTool(ctx, job, device.ID, request); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("cancelled tool prepared for dispatch", err)
	}
	assertRuntimeTranscript(t, f)
}

func TestManagedRuntimeToolEventCannotLoseWakeDuringSettlement(t *testing.T) {
	f := executionDatabase(t)
	device, _ := f.pairDevice(t)
	_, job := prepareRuntimeTool(t, f, device.ID)
	ctx := context.Background()
	request := execprotocol.Request{Version: execprotocol.Version, ID: job.ID, AgentID: f.agent.ID, Kind: "read", Arguments: json.RawMessage(`{"path":"result.txt"}`)}
	if err := f.store.PrepareTool(ctx, job, device.ID, request); err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := f.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	event := execprotocol.Event{ID: eventID, Kind: "operation.changed", TenantID: f.tenant, UserID: f.actor, EnvironmentID: device.ID, AgentIDs: []string{f.agent.ID}, OperationID: job.ID, Data: json.RawMessage(`{"state":"completed"}`), CreatedAt: time.Now()}
	if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{event}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishTool(ctx, job, managedruntime.ToolOutcome{State: "waiting", OperationLive: true}); err != nil {
		t.Fatal(err)
	}
	next, err := f.store.ClaimTool(ctx, "next-worker")
	if err != nil || next.ID != job.ID || next.WakeVersion != job.WakeVersion+1 {
		t.Fatal("concurrent completion wake was lost", next, err)
	}
	if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{event}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishTool(ctx, next, managedruntime.ToolOutcome{State: "waiting", OperationLive: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimTool(ctx, "duplicate-wake"); !errors.Is(err, managedruntime.ErrNoWork) {
		t.Fatal("duplicate event spun an offline operation", err)
	}
}
