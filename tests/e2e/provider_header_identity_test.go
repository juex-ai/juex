package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agentstate"
	"github.com/juex-ai/juex/internal/framework/thread"
	"github.com/juex-ai/juex/internal/providers"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

const requestIdentityTemplate = "${juex_agent_id}/${juex_thread_id}/${juex_generation_id}/${juex_context_scope_id}"

func headerIdentityConfig(t *testing.T) config.Config {
	t.Helper()
	work, home := t.TempDir(), t.TempDir()
	resolved, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	compaction := config.DefaultCompactionConfig()
	compaction.KeepRecentTokens = 1
	return config.Config{ModuleInventory: modulecatalog.Inventory(), Preset: config.PresetMinimal,
		WorkDir: work, HomeJuexDir: home, AgentID: resolved.Agent.ID, AgentAddress: resolved.Address, AgentStateDir: resolved.Address.StateDir(),
		ProviderID: "headers", ProviderProtocol: "openai/chat", Model: "model", ContextWindow: 32000, Compaction: compaction,
	}
}

func headerIdentityProvider(t *testing.T, url string) llm.Provider {
	t.Helper()
	streaming := false
	p, err := providers.New(providerprofile.Config{ID: "headers", Protocol: "openai/chat", Model: "model", APIKey: "test", BaseURL: url,
		Headers: map[string]string{"X-Identity": requestIdentityTemplate}, Capabilities: llm.CapabilityOverrides{Streaming: &streaming}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func currentHeaderIdentity(cfg config.Config, th *thread.Thread) string {
	generation, scope := th.ContextIdentity()
	return cfg.AgentID + "/" + th.ID + "/" + generation + "/" + scope
}

func headerIdentityReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "reply", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 1}})
}

func TestProviderHeadersFollowGenerationAndRestore(t *testing.T) {
	isolateModuleConfig(t)
	var mode atomic.Int32
	started := make(chan struct{}, 1)
	type capture struct {
		identity string
		summary  bool
	}
	var mu sync.Mutex
	var requests []capture
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		summary := len(body.Messages) > 0 && strings.Contains(string(body.Messages[0].Content), "preparing a compact summary")
		mu.Lock()
		requests = append(requests, capture{r.Header.Get("X-Identity"), summary})
		mu.Unlock()
		if summary {
			switch mode.Load() {
			case 1:
				http.Error(w, "invalid summary request", http.StatusBadRequest)
				return
			case 2:
				started <- struct{}{}
				<-r.Context().Done()
				return
			}
			headerIdentityReply(w, "## Tasks\nContinue the requested work.\n## Critical Context\nEarlier work is preserved.\n## Next Steps\nAnswer the next input.")
			return
		}
		headerIdentityReply(w, "ok")
	}))
	defer server.Close()
	cfg := headerIdentityConfig(t)
	open := func() *app.App {
		a, err := app.New(app.Options{Config: cfg, Provider: headerIdentityProvider(t, server.URL), DisableMCP: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.CloseAndWait() })
		return a
	}
	a := open()
	turn := func(a *app.App) {
		t.Helper()
		if _, err := a.Engine.Turn(t.Context(), "continue"); err != nil {
			t.Fatal(err)
		}
	}
	last := func() capture { t.Helper(); mu.Lock(); defer mu.Unlock(); return requests[len(requests)-1] }
	seed := func() {
		for range 4 {
			for _, message := range []llm.Message{llm.TextMessage(llm.RoleUser, strings.Repeat("previous context ", 80)), llm.TextMessage(llm.RoleAssistant, "recorded")} {
				if err := a.Thread.Append(message); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	initial := currentHeaderIdentity(cfg, a.Thread)
	turn(a)
	turn(a)
	if got := last(); got.identity != initial {
		t.Fatalf("initial: %+v", got)
	}
	seed()
	if _, err := a.CompactWithInstructions(t.Context(), "manual", false, ""); err != nil {
		t.Fatal(err)
	}
	if got := last(); !got.summary || got.identity != initial {
		t.Fatalf("summary identity: %+v", got)
	}
	compacted := currentHeaderIdentity(cfg, a.Thread)
	if compacted == initial || strings.Split(compacted, "/")[3] != strings.Split(initial, "/")[3] {
		t.Fatalf("compact boundary: %s -> %s", initial, compacted)
	}
	turn(a)
	if got := last(); got.identity != compacted {
		t.Fatalf("post compact: %+v", got)
	}
	if err := a.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	a = open()
	turn(a)
	if got := last(); got.identity != compacted {
		t.Fatalf("restore: %+v", got)
	}
	seed()
	mode.Store(1)
	if _, err := a.CompactWithInstructions(t.Context(), "manual", false, ""); err == nil {
		t.Fatal("expected summary failure")
	}
	if currentHeaderIdentity(cfg, a.Thread) != compacted {
		t.Fatal("failed compact changed identity")
	}
	mode.Store(2)
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() { _, err := a.CompactWithInstructions(ctx, "manual", false, ""); finished <- err }()
	select {
	case <-started:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("summary did not start")
	}
	if err := <-finished; err == nil {
		t.Fatal("expected cancellation")
	}
	if currentHeaderIdentity(cfg, a.Thread) != compacted {
		t.Fatal("cancelled compact changed identity")
	}
	mode.Store(0)
	if err := a.Engine.NewContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	renewed := currentHeaderIdentity(cfg, a.Thread)
	if parts, old := strings.Split(renewed, "/"), strings.Split(compacted, "/"); parts[1] != old[1] || parts[2] == old[2] || parts[3] == old[3] {
		t.Fatalf("new boundary: %s -> %s", compacted, renewed)
	}
	turn(a)
	if got := last(); got.identity != renewed {
		t.Fatalf("post new: %+v", got)
	}
}

func TestProviderHeadersIsolateAgentsAndWorkers(t *testing.T) {
	isolateModuleConfig(t)
	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		want := body.Messages[len(body.Messages)-1].Content
		if r.Header.Get("X-Identity") != want {
			t.Errorf("identity = %q, want %q", r.Header.Get("X-Identity"), want)
		}
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		headerIdentityReply(w, "ok")
	}))
	defer server.Close()
	provider := headerIdentityProvider(t, server.URL)
	var apps []*app.App
	var expected []string
	for agentIndex := range 2 {
		cfg := headerIdentityConfig(t)
		open := func(id string) {
			a, err := app.New(app.Options{Config: cfg, Provider: provider, ThreadID: id, DisableMCP: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.CloseAndWait() })
			apps = append(apps, a)
			expected = append(expected, currentHeaderIdentity(cfg, a.Thread))
		}
		open("")
		if agentIndex == 0 {
			store := thread.NewStore(cfg.AgentStateDir)
			for i := range 2 {
				worker, err := store.CreateWorker("0", fmt.Sprintf("worker-%d", i), 1)
				if err != nil {
					t.Fatal(err)
				}
				id := worker.ID
				if err := worker.Close(); err != nil {
					t.Fatal(err)
				}
				open(id)
			}
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, a := range apps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Engine.Turn(ctx, expected[i]); err != nil {
				t.Error(err)
			}
		}()
	}
	for range apps {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Error("concurrent requests did not arrive")
		}
	}
	close(release)
	wg.Wait()
}
