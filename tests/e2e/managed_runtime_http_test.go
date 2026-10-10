//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

type managedRuntimeFixture struct {
	pool                        *pgxpool.Pool
	directory                   *managementpg.Directory
	store                       *runtimepg.Store
	authority                   managed.RuntimeAuthority
	service                     *managedruntime.Service
	agent                       management.Agent
	main                        managedruntime.Thread
	actor, tenant, origin, base string
	client                      *http.Client
}

func managedRuntimeHTTP(t *testing.T, provider http.HandlerFunc) *managedRuntimeFixture {
	t.Helper()
	ctx := context.Background()
	pool, d := managementDatabase(t)
	if err := runtimepg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	providerServer := httptest.NewServer(provider)
	t.Cleanup(providerServer.Close)
	u, err := d.CreateUser(ctx, "runtime@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Runtime", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	model, err := d.ConfigureModel(ctx, management.ModelConfiguration{Provider: "fixture", Name: "test-model", Protocol: llm.ProtocolOpenAIChat, Endpoint: providerServer.URL, APIKey: "test-key", ContextWindow: 32768, MaxOutput: 4096, OutputReserve: 4096, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := d.CreateAgent(ctx, u.ID, tenant.ID, u.ID, management.AgentConfig{Name: "Assistant", Instructions: "Be precise", Configuration: &management.Configuration{Models: []string{model.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := managementpg.NewAuth(d)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := auth.OperatorRecovery(ctx, u.Email)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.SetPassword(ctx, linkToken(t, setup), "runtime test password"); err != nil {
		t.Fatal(err)
	}
	store := runtimepg.New(pool)
	authority := managed.RuntimeAuthority{Directory: d}
	service := &managedruntime.Service{Store: store, Authority: authority}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: d, Runtime: service, PublicURL: origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	client := managementClient(t)
	managementCall[management.Session](t, client, "POST", origin+"/api/auth/login", origin, map[string]string{"email": u.Email, "password": "runtime test password"}, 200)
	base := origin + "/api/tenants/" + tenant.ID + "/agents/" + agent.ID
	threads := managementCall[[]managedruntime.Thread](t, client, "GET", base+"/threads", origin, nil, 200)
	if len(threads) != 1 || threads[0].Kind != "main" {
		t.Fatal(threads)
	}
	return &managedRuntimeFixture{pool: pool, directory: d, store: store, authority: authority, service: service, agent: agent, main: threads[0], actor: u.ID, tenant: tenant.ID, origin: origin, base: base, client: client}
}

func (f *managedRuntimeFixture) run(t *testing.T) func() {
	t.Helper()
	runner, err := managedruntime.NewRunner(f.store, f.authority, managedruntime.RunnerConfig{Concurrency: 4, PollInterval: 20 * time.Millisecond, AuthorityInterval: 20 * time.Millisecond, IdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("Runtime did not shut down")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func (f *managedRuntimeFixture) submit(t *testing.T, id, thread, text string) managedruntime.InputReceipt {
	t.Helper()
	return managementCall[managedruntime.InputReceipt](t, f.client, "POST", f.base+"/inputs", f.origin, managedruntime.InputRequest{RequestID: id, ThreadID: thread, Text: text}, 200)
}

func (f *managedRuntimeFixture) timeline(t *testing.T, thread string) managedruntime.Timeline {
	t.Helper()
	return managementCall[managedruntime.Timeline](t, f.client, "GET", f.base+"/threads/"+thread+"/events", f.origin, nil, 200)
}

func runtimeEventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("durable Runtime transition did not occur")
}

func streamManagedReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	encoded, _ := json.Marshal(text)
	_, _ = fmt.Fprintf(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%s},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":4,\"prompt_tokens_details\":{\"cached_tokens\":3}}}\n\ndata: [DONE]\n\n", encoded)
}

func TestManagedRuntimeHTTPConversationAndIsolation(t *testing.T) {
	var calls atomic.Int32
	requests := make(chan map[string]any, 4)
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		calls.Add(1)
		streamManagedReply(w, "Hello from the provider")
	})
	first := f.submit(t, "request-one", f.main.ID, "First message")
	duplicate := f.submit(t, "request-one", f.main.ID, "First message")
	if first.ID != duplicate.ID {
		t.Fatal("input was not deduplicated")
	}
	if timeline := f.timeline(t, f.main.ID); timeline.Thread.State != "queued" || timeline.Thread.PendingInputs != 1 {
		t.Fatal(timeline.Thread)
	}
	stop := f.run(t)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" })
	f.submit(t, "request-two", f.main.ID, "Second message")
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" && calls.Load() == 2 })
	stop()
	firstRequest, secondRequest := <-requests, <-requests
	if len(firstRequest["messages"].([]any)) != 2 || len(secondRequest["messages"].([]any)) != 4 {
		t.Fatal("history not reconstructed from durable events", firstRequest, secondRequest)
	}
	var attempts, reported, input, output, cached int
	err := f.pool.QueryRow(context.Background(), `SELECT count(*),count(*) FILTER(WHERE usage_status='complete'),sum((usage->>'input_tokens')::int),sum((usage->>'output_tokens')::int),sum((usage->>'cached_input_tokens')::int) FROM runtime.attempts`).Scan(&attempts, &reported, &input, &output, &cached)
	if err != nil || attempts != 2 || reported != 2 || input != 24 || output != 8 || cached != 6 {
		t.Fatalf("usage=%d,%d,%d,%d,%d err=%v", attempts, reported, input, output, cached, err)
	}
	other, err := f.directory.CreateUser(context.Background(), "outside@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Events(context.Background(), other.ID, f.tenant, f.agent.ID, f.main.ID, 0, 200); err == nil {
		t.Fatal("cross-owner history readable")
	}
	managementCall[any](t, f.client, "GET", f.base+"/threads/"+f.main.ID+"/events?limit=501", f.origin, nil, 400)
	newStore := runtimepg.New(f.pool)
	authority, err := f.authority.Authorize(context.Background(), f.actor, f.tenant, f.agent.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := newStore.Timeline(context.Background(), authority, f.main.ID, 0, 500)
	if err != nil || reloaded.Thread.Sequence < 10 || reloaded.Thread.PendingInputs != 0 {
		t.Fatal(reloaded, err)
	}
}

func TestManagedRuntimeWorkersCancelIndependently(t *testing.T) {
	mainStarted := make(chan struct{})
	requests := make(chan string, 2)
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var body any
		_ = json.NewDecoder(r.Body).Decode(&body)
		encoded, _ := json.Marshal(body)
		requests <- string(encoded)
		if strings.Contains(string(encoded), "main block") {
			close(mainStarted)
			<-r.Context().Done()
			return
		}
		streamManagedReply(w, "Worker finished")
	})
	worker := managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/workers", f.origin, managementhttp.WorkerRequest{RequestID: "worker-one", Name: "Research"}, 200)
	duplicate := managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/workers", f.origin, managementhttp.WorkerRequest{RequestID: "worker-one", Name: "Research"}, 200)
	if worker.ID != duplicate.ID || worker.ParentID != f.main.ID {
		t.Fatal(worker, duplicate)
	}
	f.submit(t, "main", f.main.ID, "main block")
	f.run(t)
	select {
	case <-mainStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Main not started")
	}
	f.submit(t, "worker", worker.ID, "independent worker")
	runtimeEventually(t, func() bool { return f.timeline(t, worker.ID).Thread.State == "idle" })
	managementCall[any](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/cancel", f.origin, struct{}{}, 200)
	runtimeEventually(t, func() bool {
		var started int
		_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.attempts WHERE state='started'`).Scan(&started)
		return started == 0
	})
	mainTimeline := f.timeline(t, f.main.ID)
	workerTimeline := f.timeline(t, worker.ID)
	if mainTimeline.Thread.State != "idle" || workerTimeline.Thread.State != "idle" {
		t.Fatal(mainTimeline.Thread, workerTimeline.Thread)
	}
	for _, event := range mainTimeline.Events {
		if event.Kind == "message.appended" {
			var m llm.Message
			_ = json.Unmarshal(event.Data, &m)
			if m.Role == llm.RoleAssistant {
				t.Fatal("cancelled reply appended")
			}
		}
	}
	for i := 0; i < 2; i++ {
		request := <-requests
		if strings.Contains(request, "independent worker") && strings.Contains(request, "main block") {
			t.Fatal("Worker inherited Main raw history")
		}
	}
}

func TestManagedRuntimeRestartRecoversOriginalTurn(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		streamManagedReply(w, "Recovered")
	})
	f.submit(t, "recover", f.main.ID, "Recover this input")
	stop := f.run(t)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	var turnID string
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM runtime.turns`).Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	stop()
	f.run(t)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" })
	var turns, unknown, complete int
	if err := f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM runtime.turns),count(*) FILTER(WHERE state='unknown'),count(*) FILTER(WHERE state='completed') FROM runtime.attempts WHERE turn_id=$1`, turnID).Scan(&turns, &unknown, &complete); err != nil {
		t.Fatal(err)
	}
	if turns != 1 || unknown != 1 || complete != 1 || calls.Load() != 2 {
		t.Fatalf("turns=%d unknown=%d complete=%d calls=%d", turns, unknown, complete, calls.Load())
	}
}

func TestManagedRuntimeArchiveStopsAndDoesNotReplayQueue(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		streamManagedReply(w, "New authorized input")
	})
	f.submit(t, "running", f.main.ID, "running input")
	f.submit(t, "old-queue", f.main.ID, "old queued input")
	f.run(t)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("not started")
	}
	archived, err := f.directory.SetAgentArchived(context.Background(), f.actor, f.tenant, f.agent.ID, f.agent.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool {
		var count int
		_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.inputs WHERE state IN ('queued','active')`).Scan(&count)
		return count == 0
	})
	if _, err := f.directory.SetAgentArchived(context.Background(), f.actor, f.tenant, f.agent.ID, archived.Version, false); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "fresh", f.main.ID, "new input")
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" })
	if calls.Load() != 2 {
		t.Fatal("old queue replayed", calls.Load())
	}
	var held int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.inputs WHERE request_id='old-queue' AND state='held'`).Scan(&held); err != nil || held != 1 {
		t.Fatal("old input was not retained as held", held, err)
	}
}
