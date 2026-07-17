package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/session"
)

type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }
func (stubProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	return llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "ack"),
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{InputTokens: 4, OutputTokens: 2},
	}, nil
}

// newTestServer builds a Server bound to a tempdir + stub provider.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	work := t.TempDir()
	cfg := config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, Compaction: config.DefaultCompactionConfig()}
	srv := NewServer(Options{
		Cfg:      cfg,
		Provider: stubProvider{},
	})
	t.Cleanup(srv.Close)
	return srv
}

func TestServer_HealthzReturnsOK(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok\n" {
		t.Errorf("body = %q", body)
	}
}

func TestServerSessionsShareProcessModelHealth(t *testing.T) {
	srv := newTestServer(t)
	first, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	second, err := srv.openSession(context.Background(), "", app.SessionModeNewSide)
	if err != nil {
		t.Fatal(err)
	}
	if first.app.Engine.ModelHealth == nil || first.app.Engine.ModelHealth != second.app.Engine.ModelHealth || first.app.Engine.ModelHealth != srv.modelHealth {
		t.Fatalf("model health is not process-shared: first=%p second=%p server=%p", first.app.Engine.ModelHealth, second.app.Engine.ModelHealth, srv.modelHealth)
	}
}

func TestRunEnsuresActivePrimarySession(t *testing.T) {
	srv := newTestServer(t)
	srv.opts.Addr = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer stopRunServer(t, cancel, errCh)

	var h session.History
	var open bool
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for h.Active == nil || !open {
		select {
		case <-deadline:
			if h.Active == nil {
				t.Fatal("server did not create an active primary session")
			}
			t.Fatalf("session %q not open in server", h.Active.ID)
		case <-tick.C:
			var err error
			h, err = session.LoadHistory(srv.opts.Cfg.HistoryPath())
			if err != nil {
				continue
			}
			if h.Active != nil {
				_, open = srv.sessions.Load(h.Active.ID)
			}
		}
	}
	if h.Active.Kind != session.KindPrimary || !h.Active.Active {
		t.Fatalf("active session = %+v, want active primary", h.Active)
	}
}

func TestRunDoesNotRequireProviderConfigAtStartup(t *testing.T) {
	srv := NewServer(Options{
		Cfg: config.Config{WorkDir: t.TempDir()},
	})
	srv.opts.Addr = "127.0.0.1:0"
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer stopRunServer(t, cancel, errCh)

	var h session.History
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for h.Active == nil {
		select {
		case <-deadline:
			t.Fatalf("active session = %+v, want active primary", h.Active)
		case <-tick.C:
			var err error
			h, err = session.LoadHistory(srv.opts.Cfg.HistoryPath())
			if err != nil {
				continue
			}
		}
	}
	if h.Active.Kind != session.KindPrimary || !h.Active.Active {
		t.Fatalf("active session = %+v, want active primary", h.Active)
	}
	if _, ok := srv.sessions.Load(h.Active.ID); ok {
		t.Fatalf("server opened runtime app for %s without provider config", h.Active.ID)
	}
}

func TestRunPublishesAPIOnlyAgentEndpointByDefault(t *testing.T) {
	srv := newTestServer(t)
	srv.opts.Addr = "127.0.0.1:0"
	srv.opts.Cfg.AgentStateDir = t.TempDir()
	ready := make(chan ReadyInfo, 1)
	srv.opts.OnReady = func(info ReadyInfo) { ready <- info }

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()

	var info ReadyInfo
	select {
	case info = <-ready:
	case err := <-errCh:
		t.Fatalf("server failed before ready: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("server did not become ready")
	}
	if info.AgentEndpoint == "" || info.TCPAddress == "" {
		t.Fatalf("ready info = %+v", info)
	}
	runtimeState, err := endpoint.ReadRuntime(srv.opts.Cfg.AgentStateDir)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeState.Endpoint != info.AgentEndpoint {
		t.Fatalf("runtime endpoint = %q, ready endpoint = %q", runtimeState.Endpoint, info.AgentEndpoint)
	}
	target, err := endpoint.Parse(info.AgentEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	client := target.NewClient()
	for path, want := range map[string]int{"/healthz": http.StatusOK, "/": http.StatusNotFound} {
		response, err := client.Get(target.URL(path))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, want)
		}
	}
	if err := endpoint.Probe(context.Background(), runtimeState); err != nil {
		t.Fatalf("probe exact runtime identity: %v", err)
	}
	for path, want := range map[string]int{"/healthz": http.StatusOK, "/": http.StatusNotFound} {
		response, err := http.Get("http://" + info.TCPAddress + path)
		if err != nil {
			t.Fatalf("GET TCP API %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("GET TCP API %s status = %d, want %d", path, response.StatusCode, want)
		}
	}
	mismatch := runtimeState
	mismatch.InstanceID = "different-instance"
	if err := endpoint.RequestShutdown(context.Background(), mismatch); err == nil {
		t.Fatal("shutdown accepted mismatched runtime identity")
	}
	if err := endpoint.Probe(context.Background(), runtimeState); err != nil {
		t.Fatalf("server stopped after mismatched shutdown: %v", err)
	}
	if err := endpoint.RequestShutdown(context.Background(), runtimeState); err != nil {
		t.Fatalf("request exact runtime shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run after shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("server did not stop after exact shutdown")
	}
	cancel()
	if _, err := os.Stat(filepath.Join(srv.opts.Cfg.AgentStateDir, "runtime.json")); !os.IsNotExist(err) {
		t.Fatalf("runtime.json remains after shutdown: %v", err)
	}
}

func TestWebEventsDeliveryFollowsJournalCommit(t *testing.T) {
	srv := newTestServer(t)
	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sub := as.bcast.subscribe()
	defer sub.unsubscribe()

	as.app.Bus.Emit(events.Event{ID: "evt-committed", Type: "turn.started"})

	select {
	case got := <-sub.ch:
		if got.ID != "evt-committed" {
			t.Fatalf("delivered event id = %q, want evt-committed", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
	}
	data, err := os.ReadFile(filepath.Join(as.app.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"id":"evt-committed"`) {
		t.Fatalf("events.jsonl does not contain committed event:\n%s", data)
	}
}

func TestWebEventsSkipLiveDeliveryWhenJournalCommitFails(t *testing.T) {
	srv := newTestServer(t)
	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sub := as.bcast.subscribe()
	defer sub.unsubscribe()

	if err := as.app.Session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(as.app.Session.Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(as.app.Session.Dir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	as.app.Bus.Emit(events.Event{ID: "evt-uncommitted", Type: "turn.started"})

	select {
	case got := <-sub.ch:
		t.Fatalf("received uncommitted event: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

type cancelAwareProvider struct {
	started  chan struct{}
	canceled chan error
	release  chan struct{}
	once     sync.Once
}

func (p *cancelAwareProvider) Name() string { return "cancel-aware" }
func (p *cancelAwareProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-ctx.Done():
		p.canceled <- ctx.Err()
		return llm.Response{}, ctx.Err()
	case <-p.release:
		return llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "released"),
			StopReason: llm.StopEndTurn,
		}, nil
	}
}

func TestCloseCancelsMCPNotificationTurn(t *testing.T) {
	provider := &cancelAwareProvider{
		started:  make(chan struct{}),
		canceled: make(chan error, 1),
		release:  make(chan struct{}),
	}
	srv := NewServer(Options{
		Cfg: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    t.TempDir(),
			Compaction: config.DefaultCompactionConfig(),
		},
		Provider: provider,
	})
	defer srv.Close()

	if _, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary); err != nil {
		t.Fatalf("open session: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.handleMCPNotification(context.Background(), mcp.Notification{
			ServerName: "test",
			Method:     "notifications/message",
			EventType:  "demo",
			Content:    "trigger a turn",
		})
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		close(provider.release)
		t.Fatal("provider did not start")
	}

	closed := make(chan struct{})
	go func() {
		srv.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		close(provider.release)
		<-closed
		t.Fatal("server close did not cancel MCP notification turn")
	}
	select {
	case err := <-provider.canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("provider cancel err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not observe context cancellation")
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, cancellation.ErrUserCancelled) {
			t.Fatalf("notification err = %v, want ErrUserCancelled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MCP notification handler did not return")
	}
}

func stopRunServer(t *testing.T, cancel context.CancelFunc, errCh <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("server returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("server did not stop after context cancellation")
	}
}
