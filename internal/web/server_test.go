package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
)

type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }
func (stubProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	return llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "ack"),
		StopReason: llm.StopEndTurn,
	}, nil
}

// newTestServer builds a Server bound to a tempdir + stub provider.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	work := t.TempDir()
	cfg := config.Config{ProviderType: "openai", APIKey: "x", Model: "m", WorkDir: work}
	srv := NewServer(Options{
		Cfg:      cfg,
		Provider: stubProvider{},
	})
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
