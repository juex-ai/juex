package agenthttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestManagedWorkerReuseReconnectsStreamsAndStatus(t *testing.T) {
	server := newTestServer(t)
	main, err := server.openThread(t.Context(), thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	manager := main.agent.Workers()
	status, err := manager.Create(t.Context(), "first", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for status.State != agent.WorkerThreadStateIdle {
		if time.Now().After(deadline) {
			t.Fatal("first Worker did not settle")
		}
		time.Sleep(10 * time.Millisecond)
		status, err = manager.Status(status.ThreadID)
		if err != nil {
			t.Fatal(err)
		}
	}
	old, err := server.openThread(t.Context(), status.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if old.ownsAgent {
		t.Fatal("HTTP owns managed Worker")
	}
	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var closed []chan struct{}
	for _, suffix := range []string{"/events", "/status/events"} {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/threads/"+status.ThreadID+suffix, nil)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("SSE status %d", response.StatusCode)
		}
		done := make(chan struct{})
		closed = append(closed, done)
		go func() { _, _ = io.Copy(io.Discard, response.Body); _ = response.Body.Close(); close(done) }()
	}
	provider := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	defer close(provider.release)
	prepared := agent.PreparedChild{Model: "openai:m", Open: func(request agent.ChildRequest) (*agent.Agent, error) {
		child, err := app.New(app.Options{Config: server.opts.Cfg, Provider: provider, DisableMCP: true, ThreadID: request.ThreadID})
		if child == nil {
			return nil, err
		}
		return child.Agent, err
	}}
	next, err := manager.ReusePrepared(ctx, status.ThreadID, "second", prepared)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, done := range closed {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("old Worker SSE remained connected after runtime replacement")
		}
	}
	snapshot, err := server.statusSnapshotForThread(next.ThreadID)
	if err != nil || !snapshot.Thread.State.IsWorking() {
		t.Fatalf("stale cached status: %+v %v", snapshot, err)
	}
	rebound, err := server.openThread(ctx, next.ThreadID)
	if err != nil || rebound.agent == old.agent || rebound.ownsAgent {
		t.Fatalf("Worker binding was not refreshed: %v", err)
	}
}
