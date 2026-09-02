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

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
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
	cfg := config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, AgentStateDir: filepath.Join(work, ".juex"), Compaction: config.DefaultCompactionConfig()}
	if err := os.MkdirAll(cfg.AgentStateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{
		Cfg:      cfg,
		Provider: stubProvider{},
	})
	if err := app.EnsureMainThread(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func setTestAgentAddress(t *testing.T, cfg *config.Config) agentstate.AgentAddress {
	t.Helper()
	address, err := agentstate.NewAgentAddress(t.TempDir(), "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.AgentID = address.ID()
	cfg.AgentStateDir = address.StateDir()
	cfg.AgentAddress = address
	return address
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

func TestServerHandlersSeparateAgentEndpointFromTCPPointer(t *testing.T) {
	srv := newTestServer(t)
	apiServer := httptest.NewServer(srv.APIHandler())
	defer apiServer.Close()
	tcpServer := httptest.NewServer(srv.Handler())
	defer tcpServer.Close()

	for _, path := range []string{"/", "/threads/0"} {
		response, err := http.Get(apiServer.URL + path)
		if err != nil {
			t.Fatalf("GET endpoint %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("GET endpoint %s status = %d, want %d", path, response.StatusCode, http.StatusNotFound)
		}

		response, err = http.Get(tcpServer.URL + path)
		if err != nil {
			t.Fatalf("GET TCP %s: %v", path, err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET TCP %s status = %d, want %d", path, response.StatusCode, http.StatusOK)
		}
		if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
			t.Fatalf("GET TCP %s content type = %q, want text/plain", path, contentType)
		}
		for _, want := range []string{
			"one agent",
			"agent JSON/SSE API",
			"no web UI",
			"all registered agents",
			"juex fleet serve",
			"http://127.0.0.1:5839",
		} {
			if !strings.Contains(string(body), want) {
				t.Fatalf("GET TCP %s body missing %q:\n%s", path, want, body)
			}
		}
	}

	request, err := http.NewRequest(http.MethodHead, tcpServer.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("HEAD TCP root status = %d body = %q, want 200 with empty body", response.StatusCode, body)
	}

	request, err = http.NewRequest(http.MethodPost, tcpServer.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST TCP root status = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
	}

	for _, baseURL := range []string{apiServer.URL, tcpServer.URL} {
		response, err := http.Get(baseURL + "/api/not-a-route")
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s/api/not-a-route status = %d, want %d", baseURL, response.StatusCode, http.StatusNotFound)
		}
	}
}

func TestServerTCPPointerUsesConfiguredFleetAddress(t *testing.T) {
	srv := newTestServer(t)
	srv.opts.Cfg.Fleet.Addr = "127.0.0.1:6843"
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	response, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(body), "http://127.0.0.1:6843/") {
		t.Fatalf("pointer does not use configured fleet address:\n%s", body)
	}
}

func TestServerThreadsShareProcessModelHealth(t *testing.T) {
	srv := newTestServer(t)
	first, err := srv.openThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := thread.NewStore(srv.opts.Cfg.RuntimePaths().StateDir).CreateWorker(thread.MainID, "model-health")
	if err != nil {
		t.Fatal(err)
	}
	workerID := worker.ID
	_ = worker.Close()
	second, err := srv.openThread(context.Background(), workerID)
	if err != nil {
		t.Fatal(err)
	}
	if first.app.Engine.ModelHealth == nil || first.app.Engine.ModelHealth != second.app.Engine.ModelHealth || first.app.Engine.ModelHealth != srv.modelHealth {
		t.Fatalf("model health is not process-shared: first=%p second=%p server=%p", first.app.Engine.ModelHealth, second.app.Engine.ModelHealth, srv.modelHealth)
	}
}

func TestRunEnsuresMainThread(t *testing.T) {
	srv := newTestServer(t)
	setTestAgentAddress(t, &srv.opts.Cfg)
	srv.opts.Addr = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer stopRunServer(t, cancel, errCh)

	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		target, err := thread.NewStore(srv.opts.Cfg.RuntimePaths().StateDir).OpenActive(thread.MainID)
		if err == nil {
			if target.Alias != thread.MainAlias {
				t.Fatalf("Main alias = %q", target.Alias)
			}
			_ = target.Close()
			break
		}
		select {
		case <-deadline:
			t.Fatal("server did not create Main Thread")
		case <-tick.C:
		}
	}
}

func TestRunDoesNotRequireProviderConfigAtStartup(t *testing.T) {
	srv := NewServer(Options{
		Cfg: config.Config{WorkDir: t.TempDir()},
	})
	setTestAgentAddress(t, &srv.opts.Cfg)
	srv.opts.Addr = "127.0.0.1:0"
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer stopRunServer(t, cancel, errCh)

	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		target, err := thread.NewStore(srv.opts.Cfg.RuntimePaths().StateDir).OpenActive(thread.MainID)
		if err == nil {
			_ = target.Close()
			break
		}
		select {
		case <-deadline:
			t.Fatal("Main Thread was not persisted")
		case <-tick.C:
		}
	}
	if _, ok := srv.threads.Load(thread.MainID); ok {
		t.Fatal("server opened Main runtime without provider config")
	}
}

func TestRunPublishesAPIOnlyAgentEndpointByDefault(t *testing.T) {
	srv := newTestServer(t)
	address := setTestAgentAddress(t, &srv.opts.Cfg)
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
	if info.AgentEndpoint == "" {
		t.Fatalf("ready info missing agent endpoint: %+v", info)
	}
	if info.TCPAddress != "" {
		t.Fatalf("default ready info has TCP address: %+v", info)
	}
	runtimeState, err := endpoint.ReadRuntime(address)
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

func TestRestartShutdownAcknowledgesAndPersistsRuntimeRestartCause(t *testing.T) {
	provider := &blockingProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	srv := newTestServer(t)
	srv.opts.Provider = provider
	as, err := srv.openThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.APIHandler())
	defer ts.Close()
	expected := endpoint.Runtime{
		AgentID:    "abcdef",
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://" + strings.TrimPrefix(ts.URL, "http://"),
		StartedAt:  time.Now().UTC(),
	}
	shutdown := srv.setEndpointControl(expected)
	defer srv.clearEndpointControl(expected)

	response, err := http.Post(
		ts.URL+"/api/threads/"+as.app.Thread.ID+"/inputs",
		"application/json",
		strings.NewReader(`{"prompt":"work until restart"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}

	acknowledged, err := endpoint.RequestRestart(context.Background(), expected)
	if err != nil {
		t.Fatal(err)
	}
	if !acknowledged {
		t.Fatal("restart intent was not acknowledged")
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("restart did not request shutdown")
	}
	as.turns.wait()

	snapshot := as.app.Status.Snapshot()
	if snapshot.Turn == nil ||
		snapshot.Turn.State != runtime.TurnLifecycleCancelled ||
		snapshot.Turn.Error == nil ||
		snapshot.Turn.Error.Kind != runtime.StatusErrorRuntimeRestart {
		t.Fatalf("restart status = %+v", snapshot)
	}
}

func TestRunPublishesExplicitTCPAPI(t *testing.T) {
	// This test covers listener publication and routing, not session startup.
	// Keeping the server provider-free avoids coupling shutdown to asynchronous
	// active-session creation after OnReady has already fired.
	srv := NewServer(Options{Cfg: config.Config{WorkDir: t.TempDir()}})
	t.Cleanup(srv.Close)
	srv.opts.Addr = "127.0.0.1:0"
	setTestAgentAddress(t, &srv.opts.Cfg)
	ready := make(chan ReadyInfo, 1)
	srv.opts.OnReady = func(info ReadyInfo) { ready <- info }

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer stopRunServer(t, cancel, errCh)

	var info ReadyInfo
	select {
	case info = <-ready:
	case err := <-errCh:
		t.Fatalf("server failed before ready: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not become ready")
	}
	if info.AgentEndpoint == "" || info.TCPAddress == "" {
		t.Fatalf("ready info = %+v, want agent and TCP endpoints", info)
	}
	for path, want := range map[string]int{
		"/healthz":         http.StatusOK,
		"/":                http.StatusOK,
		"/api/not-a-route": http.StatusNotFound,
	} {
		response, err := http.Get("http://" + info.TCPAddress + path)
		if err != nil {
			t.Fatalf("GET TCP API %s: %v", path, err)
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			_ = response.Body.Close()
			t.Fatalf("read TCP API %s: %v", path, err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatalf("close TCP API %s: %v", path, err)
		}
		if response.StatusCode != want {
			t.Fatalf("GET TCP API %s status = %d, want %d", path, response.StatusCode, want)
		}
	}
}

func TestValidLoopbackAcceptsTheFullLoopbackRange(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{addr: "127.0.0.1:8080", want: true},
		{addr: "127.42.0.99:8080", want: true},
		{addr: "[::1]:8080", want: true},
		{addr: "localhost:8080", want: true},
		{addr: "0.0.0.0:8080"},
		{addr: "192.168.1.5:8080"},
		{addr: "127.0.0.1"},
		{addr: "localhost"},
		{addr: ""},
	}
	for _, test := range tests {
		t.Run(test.addr, func(t *testing.T) {
			if got := validLoopback(test.addr); got != test.want {
				t.Fatalf("validLoopback(%q) = %v, want %v", test.addr, got, test.want)
			}
		})
	}
}

func TestWebEventsDeliveryFollowsJournalCommit(t *testing.T) {
	srv := newTestServer(t)
	as, err := srv.openThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sub := as.bcast.subscribe()
	defer sub.unsubscribe()

	if err := as.app.Bus.Emit(events.Event{
		ID:      "evt-committed",
		Type:    "turn.started",
		Payload: runtime.TurnStartedPayload{},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-sub.ch:
		if got.ID != "evt-committed" {
			t.Fatalf("delivered event id = %q, want evt-committed", got.ID)
		}
		if got.SchemaVersion != 1 {
			t.Fatalf("delivered schema version = %d, want 1", got.SchemaVersion)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
	}
	data, err := os.ReadFile(filepath.Join(as.app.Thread.Dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"id":"evt-committed"`) {
		t.Fatalf("journal.jsonl does not contain committed event:\n%s", data)
	}
	if !strings.Contains(string(data), `"schema_version":1`) ||
		!strings.Contains(string(data), `"replay_policy":"required"`) {
		t.Fatalf("journal.jsonl does not contain the Catalog replay contract:\n%s", data)
	}
}

func TestWebEventsSkipLiveDeliveryWhenJournalCommitFails(t *testing.T) {
	srv := newTestServer(t)
	as, err := srv.openThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	sub := as.bcast.subscribe()
	defer sub.unsubscribe()

	if err := as.app.Thread.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(as.app.Thread.Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(as.app.Thread.Dir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := as.app.Bus.Emit(events.Event{
		ID:      "evt-uncommitted",
		Type:    "turn.started",
		Payload: runtime.TurnStartedPayload{},
	}); err == nil {
		t.Fatal("Emit() error = nil, want journal failure")
	}

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
	const notificationTimeout = 30 * time.Second

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
	if err := app.EnsureMainThread(srv.opts.Cfg); err != nil {
		t.Fatal(err)
	}

	if _, err := srv.openThread(context.Background(), thread.MainID); err != nil {
		t.Fatalf("open Thread: %v", err)
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
	case err := <-errCh:
		t.Fatalf("MCP notification returned before provider start: %v", err)
	case <-time.After(notificationTimeout):
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
	case <-time.After(notificationTimeout):
		close(provider.release)
		<-closed
		t.Fatal("server close did not cancel MCP notification turn")
	}
	select {
	case err := <-provider.canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("provider cancel err = %v, want context.Canceled", err)
		}
	case <-time.After(notificationTimeout):
		t.Fatal("provider did not observe context cancellation")
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, cancellation.ErrUserCancelled) {
			t.Fatalf("notification err = %v, want ErrUserCancelled", err)
		}
	case <-time.After(notificationTimeout):
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
