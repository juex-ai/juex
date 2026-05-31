package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
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
	cfg := config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work}
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

func TestRunEnsuresActivePrimarySession(t *testing.T) {
	srv := newTestServer(t)
	srv.opts.Addr = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()

	var h session.History
	var open bool
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for h.Active == nil || !open {
		select {
		case <-deadline:
			cancel()
			if h.Active == nil {
				t.Fatal("server did not create an active primary session")
			}
			t.Fatalf("session %q not open in server", h.Active.ID)
		case <-tick.C:
			var err error
			h, err = session.LoadHistory(srv.opts.Cfg.HistoryPath())
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			if h.Active != nil {
				_, open = srv.sessions.Load(h.Active.ID)
			}
		}
	}
	if h.Active.Kind != session.KindPrimary || !h.Active.Active {
		cancel()
		t.Fatalf("active session = %+v, want active primary", h.Active)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after context cancellation")
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

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server returned before cancel: %v", err)
		}
		t.Fatal("server returned before cancel")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
}
