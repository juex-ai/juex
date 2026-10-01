//go:build postgres

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func configureManagedHooks(t *testing.T, f *executionFixture, hooks []hookpolicy.Declaration) {
	t.Helper()
	agent, err := f.directory.ConfigureAgent(context.Background(), f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Instructions: f.agent.Instructions, ModelID: f.agent.ModelID, Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	f.agent = agent
}

func managedHook(id, environment string, event hookpolicy.Event, command string) hookpolicy.Declaration {
	return hookpolicy.Declaration{ID: id, Enabled: true, Required: true, Events: []hookpolicy.Event{event}, EnvironmentID: environment, Command: []string{"/bin/sh", "-c", command}, TimeoutSeconds: 10, MaxOutputBytes: 8192}
}

func hooksEventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("hook workflow did not reach its durable state")
}

func TestManagedHooksStopDefersCompletionAndToolEffectsRunOnce(t *testing.T) {
	var calls atomic.Int32
	var environment string
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			streamManagedTool(w, "write", map[string]any{"environment_id": environment, "path": "result.txt", "content": "original result"})
		case 2:
			streamManagedReply(w, "First proposed completion")
		default:
			encoded, _ := json.Marshal(body["messages"])
			if !strings.Contains(string(encoded), "Continue once") || !strings.Contains(string(encoded), "post hook context") {
				t.Error("hook decisions absent from model context")
			}
			streamManagedReply(w, "Final completion")
		}
	})
	device, token := f.pairDevice(t)
	environment = device.ID
	directory := t.TempDir()
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "executor"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	configureManagedHooks(t, f, []hookpolicy.Declaration{
		managedHook("start", device.ID, hookpolicy.ThreadStart, "cat >> start-input; printf 'start context'"),
		managedHook("input", device.ID, hookpolicy.UserPromptSubmit, "cat >> user-input; printf 'input context'"),
		managedHook("before", device.ID, hookpolicy.PreToolUse, "printf p >> before-count"),
		managedHook("after", device.ID, hookpolicy.PostToolUse, "test -f result.txt || exit 1; printf q >> after-count; printf 'post hook context' >&2; exit 2"),
		managedHook("finish", device.ID, hookpolicy.Stop, "printf s >> stop-count; if test ! -f continued; then touch stop-entered; while test ! -f release; do sleep 0.02; done; touch continued; printf 'Continue once' >&2; exit 2; fi"),
	})
	input := f.submit(t, "hook-lifecycle", f.main.ID, "Run a file operation")
	stop := runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	hooksEventually(t, func() bool { _, err := os.Stat(filepath.Join(directory, "stop-entered")); return err == nil })
	var state string
	var evidence, completions int
	if err := f.pool.QueryRow(context.Background(), `SELECT state FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.memory_evidence WHERE input_id=$1`, input.ID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.events WHERE thread_id=$1 AND kind='turn.completed'`, f.main.ID).Scan(&completions); err != nil {
		t.Fatal(err)
	}
	if state != "active" || evidence != 0 || completions != 0 || calls.Load() != 2 {
		t.Fatal("completed before Stop decision", state, evidence, completions, calls.Load())
	}
	if err := os.WriteFile(filepath.Join(directory, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	hooksEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" && calls.Load() == 3 })
	stop()
	for name, want := range map[string]string{"result.txt": "original result", "before-count": "p", "after-count": "q", "stop-count": "ss"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(data) != want {
			t.Fatal(name, string(data), err)
		}
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.memory_evidence WHERE input_id=$1`, input.ID).Scan(&evidence); err != nil || evidence != 1 {
		t.Fatal(evidence, err)
	}
	assertRuntimeTranscript(t, f)
}

func TestManagedHooksOfflineSnapshotSurvivesRestart(t *testing.T) {
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); streamManagedReply(w, "after hook") })
	device, token := f.pairDevice(t)
	hook := managedHook("input", device.ID, hookpolicy.UserPromptSubmit, "printf old >> version")
	configureManagedHooks(t, f, []hookpolicy.Declaration{hook})
	f.submit(t, "offline-hook", f.main.ID, "Wait on the chosen device")
	gateway := runtimeExecutionGateway(t, f)
	stop := runRuntimeTools(t, f, gateway)
	hooksEventually(t, func() bool {
		var n int
		err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.hooks WHERE request IS NOT NULL AND state='waiting'`).Scan(&n)
		return err == nil && n == 1
	})
	stop()
	if calls.Load() != 0 {
		t.Fatal("offline hook did not gate model calls")
	}
	hook.Command = []string{"/bin/sh", "-c", "printf new >> version"}
	configureManagedHooks(t, f, []hookpolicy.Declaration{hook})
	directory := t.TempDir()
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "executor"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	runRuntimeTools(t, f, gateway)
	hooksEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" && calls.Load() == 1 })
	if data, err := os.ReadFile(filepath.Join(directory, "version")); err != nil || string(data) != "old" {
		t.Fatal("frozen hook changed or replayed", string(data), err)
	}
}

func TestManagedHooksInputRejectionAndDisableInvalidateOldWork(t *testing.T) {
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); streamManagedReply(w, "allowed") })
	device, token := f.pairDevice(t)
	directory := t.TempDir()
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "executor"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	hook := managedHook("deny", device.ID, hookpolicy.UserPromptSubmit, "printf rejected; exit 2")
	configureManagedHooks(t, f, []hookpolicy.Declaration{hook})
	f.submit(t, "rejected-hook", f.main.ID, "Rejected before model work")
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	hooksEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "failed" })
	if calls.Load() != 0 {
		t.Fatal("hook rejection allowed model work")
	}
	before := f.agent.ExecutionEpoch
	hook.Enabled = false
	configureManagedHooks(t, f, []hookpolicy.Declaration{hook})
	if f.agent.ExecutionEpoch != before+1 {
		t.Fatal("disabling hook did not revoke frozen work")
	}
	f.submit(t, "fresh-without-hook", f.main.ID, "Fresh authorized input")
	hooksEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" && calls.Load() == 1 })
}

func TestManagedHooksUnknownDoesNotSkipOptionalPolicy(t *testing.T) {
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); streamManagedReply(w, "must not run") })
	device, _ := f.pairDevice(t)
	hook := managedHook("uncertain", device.ID, hookpolicy.UserPromptSubmit, "printf unsafe >> effects")
	hook.Required = false
	configureManagedHooks(t, f, []hookpolicy.Declaration{hook})
	f.submit(t, "uncertain-hook", f.main.ID, "wait")
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	var id string
	hooksEventually(t, func() bool {
		return f.pool.QueryRow(context.Background(), `SELECT id FROM runtime.hooks WHERE request IS NOT NULL AND state='waiting'`).Scan(&id) == nil
	})
	connection, err := f.executionStore.Connect(context.Background(), device.ID, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(context.Background(), device.ID, connection.ConnectionEpoch, id); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.Settle(context.Background(), device.ID, connection.ConnectionEpoch, id, execprotocol.Unknown, "executor result lost after dispatch"); err != nil {
		t.Fatal(err)
	}
	hooksEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "blocked" })
	if calls.Load() != 0 {
		t.Fatal("unknown optional hook silently skipped")
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM execution.operations WHERE request->>'kind'='run_hook'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("hook replayed with new identity", count, err)
	}
}

func TestManagedHooksCancelOfflineAndMemoryWorkerHasNoProcessHooks(t *testing.T) {
	f := executionDatabaseWithProvider(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected model call") })
	device, token := f.pairDevice(t)
	configureManagedHooks(t, f, []hookpolicy.Declaration{managedHook("input", device.ID, hookpolicy.UserPromptSubmit, "printf forbidden >> effects"), managedHook("start", device.ID, hookpolicy.ThreadStart, "printf forbidden >> effects")})
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	job := applicationJob()
	receipt, err := f.store.AdmitApplication(ctx, scope, job)
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.Claim(ctx, scope.AgentID, "inspect-memory-hooks", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := f.store.BeginTurn(ctx, lease, scope, receipt.InputID, config)
	if err != nil || work.Deferred {
		t.Fatal("Memory Worker inherited user process hooks", work.Deferred, err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.hooks WHERE thread_id=$1`, receipt.ThreadID).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := f.store.CancelApplication(ctx, scope, job.Application, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "offline-cancel", f.main.ID, "stop before reconnect")
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	hooksEventually(t, func() bool {
		var n int
		err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.hooks WHERE request IS NOT NULL AND state='waiting'`).Scan(&n)
		return err == nil && n == 1
	})
	if err := f.service.Cancel(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID); err != nil {
		t.Fatal(err)
	}
	hooksEventually(t, func() bool {
		var n int
		err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.hooks WHERE thread_id=$1 AND state='cancelled'`, f.main.ID).Scan(&n)
		return err == nil && n == 1
	})
	directory := t.TempDir()
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "executor"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	hooksEventually(t, func() bool {
		var online bool
		err := f.pool.QueryRow(ctx, `SELECT online_until>clock_timestamp() FROM execution.environments WHERE id=$1`, device.ID).Scan(&online)
		return err == nil && online
	})
	if _, err := os.Stat(filepath.Join(directory, "effects")); !os.IsNotExist(err) {
		t.Fatal("cancelled hook ran on reconnect", err)
	}
}

func TestManagedHooksCompactionCommitsOnceAcrossPostHookRestart(t *testing.T) {
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			encoded, _ := json.Marshal(body)
			if !strings.Contains(string(encoded), "Keep hook identifiers") {
				t.Error("pre-compaction hook instructions missing")
			}
			streamManagedReply(w, "Historical work completed. Preserve current task and exact identifiers.")
		} else {
			streamManagedReply(w, "Continued after compact hook")
		}
	})
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.Claim(ctx, scope.AgentID, "seed-hook-history", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 18; i++ {
		input, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: fmt.Sprint("prior-hook-", i), Text: strings.Repeat("Historical detailed facts. ", 800)})
		if err != nil {
			t.Fatal(err)
		}
		work, err := f.store.BeginTurn(ctx, lease, scope, input.ID, config)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := f.store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Purpose: "conversation"})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Done"), StopReason: llm.StopEndTurn}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	device, token := f.pairDevice(t)
	directory := t.TempDir()
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "executor"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	configureManagedHooks(t, f, []hookpolicy.Declaration{
		managedHook("before-compact", device.ID, hookpolicy.PreCompact, "printf p >> before-count; printf 'Keep hook identifiers'"),
		managedHook("after-compact", device.ID, hookpolicy.PostCompact, "printf q >> after-count; touch post-entered; while test ! -f release; do sleep 0.02; done; printf 'compaction hook finished'"),
	})
	f.submit(t, "compact-hooks", f.main.ID, "Continue after compaction")
	gateway := runtimeExecutionGateway(t, f)
	stop := runRuntimeTools(t, f, gateway)
	hooksEventually(t, func() bool { _, err := os.Stat(filepath.Join(directory, "post-entered")); return err == nil })
	if calls.Load() != 1 || f.timeline(t, f.main.ID).Thread.Generation != 2 {
		t.Fatal("compaction was not committed before post hook", calls.Load())
	}
	stop()
	if err := os.WriteFile(filepath.Join(directory, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	runRuntimeTools(t, f, gateway)
	hooksEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" && calls.Load() == 2 })
	for name, want := range map[string]string{"before-count": "p", "after-count": "q"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(data) != want {
			t.Fatal(name, string(data), err)
		}
	}
	if f.timeline(t, f.main.ID).Thread.Generation != 2 {
		t.Fatal("post-hook restart repeated compaction")
	}
}

func TestManagedHooksPostUnknownCancellationStopsOriginalProcessAndPreservesResult(t *testing.T) {
	var calls atomic.Int32
	var environment string
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 1 {
			t.Error("model resumed through unknown Post Hook")
		}
		streamManagedTool(w, "exec_command", map[string]any{"environment_id": environment, "command": "printf original-process; while :; do sleep 0.05; printf .; done"})
	})
	ctx := context.Background()
	device, token := f.pairDevice(t)
	environment = device.ID
	hookDevice, _ := f.pairDevice(t)
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "executor"), EnvironmentID: device.ID, WorkingDirectory: t.TempDir(), Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	configureManagedHooks(t, f, []hookpolicy.Declaration{managedHook("after", hookDevice.ID, hookpolicy.PostToolUse, "printf post")})
	f.submit(t, "background-post-unknown", f.main.ID, "Start process")
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	var hookID, originalID string
	hooksEventually(t, func() bool {
		return f.pool.QueryRow(ctx, `SELECT h.id,j.id FROM runtime.hooks h JOIN runtime.tools j ON h.anchor=j.id::text WHERE h.event='PostToolUse' AND h.state='waiting' AND h.request IS NOT NULL AND j.deferred_result IS NOT NULL`).Scan(&hookID, &originalID) == nil
	})
	connection, err := f.executionStore.Connect(ctx, hookDevice.ID, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, hookDevice.ID, connection.ConnectionEpoch, hookID); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.Settle(ctx, hookDevice.ID, connection.ConnectionEpoch, hookID, execprotocol.Unknown, "hook executor interrupted"); err != nil {
		t.Fatal(err)
	}
	hooksEventually(t, func() bool {
		var state string
		var live bool
		return f.pool.QueryRow(ctx, `SELECT state,operation_live FROM runtime.tools WHERE id=$1`, originalID).Scan(&state, &live) == nil && state == "unknown" && live
	})
	if err := f.service.Cancel(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID); err != nil {
		t.Fatal(err)
	}
	hooksEventually(t, func() bool {
		var stopped bool
		return f.pool.QueryRow(ctx, `SELECT o.state='cancelled' AND NOT j.operation_live FROM execution.operations o JOIN runtime.tools j ON j.id::text=o.id WHERE o.id=$1`, originalID).Scan(&stopped) == nil && stopped
	})
	var fact bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.events WHERE thread_id=$1 AND kind='message.appended' AND data::text LIKE '%original-process%' AND data::text LIKE '%original result above remains valid%')`, f.main.ID).Scan(&fact); err != nil || !fact {
		t.Fatal("confirmed result lost during cancellation", fact, err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations WHERE request->>'kind'='exec_command'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("original process replayed", count, err)
	}
	assertRuntimeTranscript(t, f)
}
