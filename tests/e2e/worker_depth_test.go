package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	web "github.com/juex-ai/juex/internal/entrypoints/agenthttp"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestWorkerDepthAPIConstrainsStorageWhenModuleDisabled(t *testing.T) {
	isolateModuleConfig(t)
	for _, depth := range []int{1, 2} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Preset: config.PresetMinimal, WorkerThreadMaxDepth: depth}
			provider := &moduleEntryProvider{}
			srv := web.NewServer(web.Options{Cfg: cfg, Provider: provider})
			defer srv.Close()
			handler := srv.APIHandler()
			parent := "0"
			for range depth {
				var created thread.Info
				body := moduleEntryRequest(t, handler, "POST", "/api/threads", `{"parent_thread_id":"`+parent+`"}`, 201)
				if err := json.Unmarshal([]byte(body), &created); err != nil {
					t.Fatal(err)
				}
				parent = created.ID
			}
			store := thread.NewStore(cfg.AgentStateDir)
			before, err := os.ReadFile(store.IndexPath())
			if err != nil {
				t.Fatal(err)
			}
			body := moduleEntryRequest(t, handler, "POST", "/api/threads", `{"parent_thread_id":"`+parent+`"}`, 400)
			if !strings.Contains(body, fmt.Sprintf("max_depth=%d", depth)) {
				t.Fatalf("error = %s", body)
			}
			after, err := os.ReadFile(store.IndexPath())
			if err != nil || string(before) != string(after) || provider.calls.Load() != 0 {
				t.Fatal("denied storage creation had side effects")
			}
		})
	}
}

type depthRecoveryProvider struct {
	started          chan struct{}
	once             sync.Once
	mu               sync.Mutex
	unavailableTools []string
}

func (*depthRecoveryProvider) Name() string { return "depth-recovery" }
func (p *depthRecoveryProvider) Complete(ctx context.Context, _ string, history []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	for _, spec := range specs {
		if strings.HasPrefix(spec.Name, "thread_") {
			p.unavailableTools = append(p.unavailableTools, spec.Name)
		}
	}
	p.mu.Unlock()
	if lastDirectUserText(history) == "resume pending" {
		p.once.Do(func() { close(p.started) })
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "host execution complete"), StopReason: llm.StopEndTurn}, nil
}

func TestWorkerDepthDowngradeKeepsHostRecoveryAndRetention(t *testing.T) {
	isolateModuleConfig(t)
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Preset: config.PresetMinimal,
		WorkerThreadMaxDepth: 2, Modules: config.ModulePolicy{"worker-threads": {Enabled: true}},
	}
	main, err := app.New(app.Options{Config: cfg, Provider: &moduleEntryProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = main.CloseAndWait() })
	parent, err := main.Workers().Create(t.Context(), "parent", "parent", "", false)
	if err != nil {
		t.Fatal(err)
	}
	parentAgent, ok := main.ManagedWorkerAgent(parent.ThreadID)
	if !ok {
		t.Fatal("missing parent runtime")
	}
	waitDepthWorkerIdle(t, main.Workers(), parent.ThreadID)
	child, err := parentAgent.Workers().Create(t.Context(), "child", "child", "", true)
	if err != nil {
		t.Fatal(err)
	}
	waitDepthWorkerIdle(t, parentAgent.Workers(), child.ThreadID)
	childAgent, ok := parentAgent.ManagedWorkerAgent(child.ThreadID)
	if !ok {
		t.Fatal("missing child runtime")
	}
	if childAgent.Workers() != nil {
		t.Fatal("depth 2 Worker initialized worker-threads")
	}
	_, err = childAgent.Engine.ReceivePendingInput(t.Context(), runtime.PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, "resume pending"), DeferDelivery: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := main.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	if parentAgent.Workers().ShouldDeferContinuation() {
		t.Fatal("closed parent retained subscriptions or result handoffs")
	}

	cfg.WorkerThreadMaxDepth = 1
	provider := &depthRecoveryProvider{started: make(chan struct{})}
	parentReopened, err := app.New(app.Options{Config: cfg, ThreadID: parent.ThreadID, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	if parentReopened.Workers() != nil {
		t.Fatal("depth downgrade restored parent's worker module")
	}
	for _, module := range parentReopened.Engine.RuntimeModules.Modules() {
		if module.ID() == "worker-threads" {
			t.Fatal("depth downgrade restored parent lifecycle hooks")
		}
	}
	if err := parentReopened.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	srv := web.NewServer(web.Options{Cfg: cfg, Provider: provider})
	defer srv.Close()
	handler := srv.APIHandler()
	base := "/api/threads/" + child.ThreadID
	moduleEntryRequest(t, handler, "GET", base+"/status", "", 200)
	// Status is passive. A host input opens execution and triggers recovery
	// before accepting the new turn, without loading the capped parent module.
	requestDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("POST", base+"/inputs", strings.NewReader(`{"prompt":"host followup"}`)))
		requestDone <- recorder
	}()
	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("host did not recover pending deep Worker input")
	}
	moduleEntryRequest(t, handler, "POST", base+"/stop", "{}", 200)
	select {
	case response := <-requestDone:
		if response.Code != 202 {
			t.Fatalf("host input: %d %s", response.Code, response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("host input did not settle after recovery stopped")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var snapshot runtime.StatusSnapshot
		body := moduleEntryRequest(t, handler, "GET", base+"/status", "", 200)
		if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
			t.Fatal(err)
		}
		if !snapshot.Thread.State.IsWorking() && snapshot.Thread.PendingCount == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deep Worker failed to stop: %s", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	moduleEntryRequest(t, handler, "POST", "/api/threads", `{"parent_thread_id":"`+child.ThreadID+`"}`, 400)
	moduleEntryRequest(t, handler, "POST", base+"/archive", "{}", 200)
	if body := moduleEntryRequest(t, handler, "GET", base, "", 200); !strings.Contains(body, "retained history") {
		t.Fatalf("deep history was lost: %s", body)
	}
	moduleEntryRequest(t, handler, "POST", base+"/unarchive", "{}", 200)
	moduleEntryRequest(t, handler, "POST", base+"/archive", "{}", 200)
	moduleEntryRequest(t, handler, "DELETE", base, "", 200)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.unavailableTools) != 0 {
		t.Fatalf("host recovery exposed capped Worker tools: %v", provider.unavailableTools)
	}
}

func waitDepthWorkerIdle(t *testing.T, manager *agent.WorkerManager, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.Status(id)
		if err == nil && status.State == agent.WorkerThreadStateIdle {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Worker did not become idle")
}
