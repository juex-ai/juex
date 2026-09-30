//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
)

func applicationJob() managedruntime.ApplicationJob {
	return managedruntime.ApplicationJob{Application: "memory", ID: uuid.NewString(), Epoch: 1, Fence: 1, Name: "Memory review", Instruction: "Review only the supplied original evidence.", MaxCalls: 2}
}

func TestManagedRuntimeApplicationAdmissionCancellationAndIsolation(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	job := applicationJob()
	// Public Worker request identities cannot seed an application's context.
	shadow, err := store.CreateWorker(ctx, scope, main.ID, "app/memory/"+job.ID, job.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "inject", ThreadID: shadow.ID, Text: "Unrelated private task"}); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.AdmitApplication(ctx, scope, job)
	if err != nil || receipt.ThreadID == shadow.ID {
		t.Fatal(receipt, err)
	}
	if repeated, err := store.AdmitApplication(ctx, scope, job); err != nil || repeated.InputID != receipt.InputID {
		t.Fatal("duplicate application acceptance", repeated, err)
	}
	changed := job
	changed.Instruction = "A different task"
	if _, err := store.AdmitApplication(ctx, scope, changed); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("application identity reused", err)
	}
	if _, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "human", ThreadID: receipt.ThreadID, Text: "Run a shell"}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("ordinary input injected", err)
	}
	if _, err := store.AcceptCompaction(ctx, scope, receipt.ThreadID, managedruntime.CompactionRequest{RequestID: "manual"}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("manual task injected", err)
	}
	if _, err := store.CreateWorker(ctx, scope, receipt.ThreadID, "nested", "Escape"); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("application spawned child", err)
	}
	if err := store.CancelThread(ctx, scope, main.ID); err != nil {
		t.Fatal(err)
	}
	if current, err := store.ApplicationReceipt(ctx, scope, job.Application, job.ID); err != nil || current.State != "queued" {
		t.Fatal("source cancellation propagated", current, err)
	}
	if err := store.CancelApplication(ctx, scope, job.Application, job.ID); err != nil {
		t.Fatal(err)
	}
	if current, err := store.ApplicationReceipt(ctx, scope, job.Application, job.ID); err != nil || current.State != "cancelled" {
		t.Fatal(current, err)
	}
	if current, err := store.AdmitApplication(ctx, scope, job); err != nil || current.State != "cancelled" || current.InputID != receipt.InputID {
		t.Fatal("cancelled job replayed", current, err)
	}
	before := applicationJob()
	if err := store.CancelApplication(ctx, scope, before.Application, before.ID); err != nil {
		t.Fatal(err)
	}
	if current, err := store.AdmitApplication(ctx, scope, before); err != nil || current.State != "cancelled" || current.ThreadID != "" {
		t.Fatal("cancel-before-admit made Worker", current, err)
	}
	before.Instruction = "Reused cancelled identity"
	if _, err := store.AdmitApplication(ctx, scope, before); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("cancelled identity accepted different content", err)
	}
	foreign := scope
	foreign.UserID = uuid.NewString()
	if _, err := store.ApplicationReceipt(ctx, foreign, job.Application, job.ID); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("cross-owner job read", err)
	}
	var inputs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE thread_id=$1`, receipt.ThreadID).Scan(&inputs); err != nil || inputs != 1 {
		t.Fatal("application input identity", inputs, err)
	}
}

type runtimeApplicationGateway struct {
	Revoked atomic.Bool
	Calls   atomic.Int32
}

func (g *runtimeApplicationGateway) Check(context.Context, managedruntime.Scope, managedruntime.ApplicationJob) error {
	if g.Revoked.Load() {
		return managedruntime.ErrDenied
	}
	return nil
}
func (g *runtimeApplicationGateway) Tools(context.Context, managedruntime.Scope, *managedruntime.ApplicationJob) (managedruntime.ApplicationTools, error) {
	return managedruntime.ApplicationTools{Tools: []llm.ToolSpec{{Name: "memory_search", Description: "Read bounded knowledge", Schema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}}}}}, nil
}
func (g *runtimeApplicationGateway) Call(context.Context, managedruntime.ToolWork, *managedruntime.ApplicationJob) (any, error) {
	g.Calls.Add(1)
	return map[string]any{"entries": []any{}}, nil
}

func runApplicationFixture(t *testing.T, f *managedRuntimeFixture, gateway managedruntime.ApplicationGateway) func() {
	t.Helper()
	runner, err := managedruntime.NewRunner(f.store, f.authority, managedruntime.RunnerConfig{Applications: gateway, Concurrency: 2, PollInterval: 20 * time.Millisecond, AuthorityInterval: 20 * time.Millisecond})
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
				t.Error("application Runtime did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestManagedRuntimeApplicationToolsAndPersistentBudget(t *testing.T) {
	var calls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		for _, tool := range request.Tools {
			if tool.Function.Name != "read_context" && tool.Function.Name != "memory_search" {
				t.Error("restricted Worker exposed", tool.Function.Name)
			}
		}
		calls.Add(1)
		streamManagedTool(w, "memory_search", map[string]any{"query": "evidence"})
	})
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
	gateway := &runtimeApplicationGateway{}
	stop := runApplicationFixture(t, f, gateway)
	runtimeEventually(t, func() bool {
		current, err := f.store.ApplicationReceipt(ctx, scope, job.Application, job.ID)
		return err == nil && current.State == "held"
	})
	stop()
	if calls.Load() != 2 || gateway.Calls.Load() != 2 {
		t.Fatal("model/tool budget", calls.Load(), gateway.Calls.Load())
	}
	var attempts int
	var reason string
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1`, receipt.ThreadID).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatal(attempts, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT data->>'reason' FROM runtime.events WHERE thread_id=$1 AND kind='input.held'`, receipt.ThreadID).Scan(&reason); err != nil || reason != "application_budget_exhausted" {
		t.Fatal(reason, err)
	}
	// Restart and repeat admission cannot reset attempts or replace the Worker.
	if again, err := f.store.AdmitApplication(ctx, scope, job); err != nil || again.ThreadID != receipt.ThreadID || again.State != "held" {
		t.Fatal(again, err)
	}
	runApplicationFixture(t, f, gateway)
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatal("restart reset budget", calls.Load())
	}
}

func TestManagedRuntimeApplicationRevocationCancelsModel(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	f := managedRuntimeHTTP(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	})
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
	gateway := &runtimeApplicationGateway{}
	runApplicationFixture(t, f, gateway)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("application model did not start")
	}
	gateway.Revoked.Store(true)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("application revocation did not cancel model")
	}
	runtimeEventually(t, func() bool {
		current, err := f.store.ApplicationReceipt(ctx, scope, job.Application, job.ID)
		return err == nil && current.State == "cancelled"
	})
	var replies int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.events WHERE thread_id=$1 AND kind='message.appended' AND data->>'role'='assistant'`, receipt.ThreadID).Scan(&replies); err != nil || replies != 0 {
		t.Fatal("revoked result committed", replies, err)
	}
}

func TestManagedRuntimeApplicationRPCNamespace(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) { streamManagedReply(w, "Reviewed") })
	ctx := context.Background()
	pki := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	f.service.Applications = &runtimeApplicationGateway{}
	listener := platformListener(t)
	server, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(pki, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	calendar, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "calendar"))
	if err != nil {
		t.Fatal(err)
	}
	management, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	job := applicationJob()
	if _, err := management.AdmitApplication(ctx, scope, job); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Management forged app task", err)
	}
	busyJob := applicationJob()
	busyJob.IdleSourceThread = f.main.ID
	if _, err := client.AdmitApplication(ctx, scope, busyJob); !errors.Is(err, managedruntime.ErrSourceBusy) {
		t.Fatal("idle wait lost over RPC", err)
	}
	if _, err := calendar.AdmitApplication(ctx, scope, job); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Calendar forged Memory task", err)
	}
	if _, err := client.Submit(ctx, f.actor, f.tenant, f.agent.ID, managedruntime.InputRequest{RequestID: "escape", Text: "Run arbitrary work"}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Memory called ordinary input API", err)
	}
	if _, err := client.Threads(ctx, f.actor, f.tenant, f.agent.ID); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Memory listed private Threads", err)
	}
	if _, err := client.Events(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, 0, 20); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Memory read arbitrary history", err)
	}
	if err := client.Cancel(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Memory cancelled unrelated Main", err)
	}
	if _, err := client.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "escape", "Arbitrary worker"); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Memory created unrestricted Worker", err)
	}
	if _, err := client.Compact(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, managedruntime.CompactionRequest{RequestID: "escape"}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Memory controlled Main context", err)
	}
	if _, err := client.Archive(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, true); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Memory controlled unrelated retention", err)
	}
	receipt, err := client.AdmitApplication(ctx, scope, job)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := client.ApplicationReceipt(ctx, scope, job.Application, job.ID); err != nil || current.InputID != receipt.InputID {
		t.Fatal(current, err)
	}
	if err := calendar.CancelApplication(ctx, scope, "memory", job.ID); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Calendar cancelled Memory", err)
	}
	if err := client.CancelApplication(ctx, scope, "memory", job.ID); err != nil {
		t.Fatal(err)
	}
	// The same app can recover a receipt after its authority has been revoked.
	f.service.Applications.(*runtimeApplicationGateway).Revoked.Store(true)
	if again, err := client.AdmitApplication(ctx, scope, job); err != nil || again.State != "cancelled" {
		t.Fatal("settled receipt required renewed execution grant", again, err)
	}
}

func TestManagedRuntimeApplicationUnknownAttemptConsumesBudget(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	f := managedRuntimeHTTP(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	job := applicationJob()
	job.MaxCalls = 1
	receipt, err := f.store.AdmitApplication(ctx, scope, job)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &runtimeApplicationGateway{}
	stop := runApplicationFixture(t, f, gateway)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("application attempt did not start")
	}
	stop()
	runApplicationFixture(t, f, gateway)
	runtimeEventually(t, func() bool {
		value, err := f.store.ApplicationReceipt(ctx, scope, job.Application, job.ID)
		return err == nil && value.State == "held"
	})
	var state string
	if err := f.pool.QueryRow(ctx, `SELECT a.state FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1`, receipt.ThreadID).Scan(&state); err != nil || state != "unknown" || calls.Load() != 1 {
		t.Fatal("recovery reset unknown attempt budget", state, calls.Load(), err)
	}
}

func (g *runtimeApplicationGateway) Cancel(context.Context, managedruntime.ToolWork) error {
	return nil
}
