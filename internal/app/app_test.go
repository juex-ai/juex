package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/session"
)

type stubProvider struct {
	replies []llm.Response
	calls   int
}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	if s.calls >= len(s.replies) {
		return llm.Response{}, errors.New("stub exhausted")
	}
	r := s.replies[s.calls]
	s.calls++
	return r, nil
}

func newStubApp(t *testing.T, replies ...llm.Response) (*App, *stubProvider) {
	t.Helper()
	dir := t.TempDir()
	prov := &stubProvider{replies: replies}
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: prov,
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a, prov
}

func TestApp_RunSingleTurn(t *testing.T) {
	a, _ := newStubApp(t, llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "hello back"),
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{InputTokens: 8, OutputTokens: 3},
	})
	out, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello back" {
		t.Fatalf("out = %q", out)
	}
	if got := a.TokenUsage(); got != (llm.Usage{InputTokens: 8, OutputTokens: 3}) {
		t.Fatalf("usage = %+v", got)
	}
}

func TestApp_MCPNotificationRunsAgentTurn(t *testing.T) {
	a, prov := newStubApp(t, llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "handled event"),
		StopReason: llm.StopEndTurn,
	})

	err := a.handleMCPNotification(context.Background(), mcp.Notification{
		ServerName: "local",
		Method:     "notifications/claude/channel",
		EventType:  "message",
		Content:    "[realtime] hello alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prov.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.calls)
	}
	if len(a.Session.History) != 2 {
		t.Fatalf("history len = %d, want user and assistant", len(a.Session.History))
	}
	user := a.Session.History[0]
	if user.Kind != llm.MessageKindMCPEvent {
		t.Fatalf("user kind = %q", user.Kind)
	}
	if got := user.FirstText(); got != "local:message:[realtime] hello alice" {
		t.Fatalf("user text = %q", got)
	}
}

type blockingAppProvider struct {
	started chan struct{}
	release chan struct{}

	mu        sync.Mutex
	calls     int
	histories [][]llm.Message
}

func newBlockingAppProvider() *blockingAppProvider {
	return &blockingAppProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *blockingAppProvider) Name() string { return "blocking" }

func (p *blockingAppProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.histories = append(p.histories, append([]llm.Message(nil), h...))
	p.mu.Unlock()
	if idx == 0 {
		close(p.started)
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn}, nil
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "handled queued event"), StopReason: llm.StopEndTurn}, nil
}

func TestApp_MCPNotificationQueuesDuringActiveTurn(t *testing.T) {
	dir := t.TempDir()
	prov := newBlockingAppProvider()
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: prov,
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })

	done := make(chan error, 1)
	go func() {
		out, err := a.Run(context.Background(), "active")
		if err == nil && out != "handled queued event" {
			err = errors.New("unexpected output: " + out)
		}
		done <- err
	}()
	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	if err := a.handleMCPNotification(context.Background(), mcp.Notification{
		ServerName: "local",
		EventType:  "message",
		Content:    "queued",
	}); err != nil {
		t.Fatal(err)
	}
	close(prov.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(a.Session.History) != 4 {
		t.Fatalf("history len = %d, want active user, first assistant, queued event, final assistant", len(a.Session.History))
	}
	if a.Session.History[2].Kind != llm.MessageKindMCPEvent {
		t.Fatalf("queued message kind = %q", a.Session.History[2].Kind)
	}
	if got := a.Session.History[2].FirstText(); got != "local:message:queued" {
		t.Fatalf("queued text = %q", got)
	}
}

func TestApp_MCPNotificationUsesLifecycleContext(t *testing.T) {
	a, prov := newStubApp(t, llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "handled event"),
		StopReason: llm.StopEndTurn,
	})
	a.cancel()

	err := a.handleMCPNotification(a.ctx, mcp.Notification{
		ServerName: "local",
		Method:     "notifications/claude/channel",
		EventType:  "message",
		Content:    "ignored",
	})
	if err == nil {
		t.Fatal("expected cancelled context error")
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
	}
	if len(a.Session.History) != 0 {
		t.Fatalf("history len = %d, want 0", len(a.Session.History))
	}
}

func TestFormatTokenUsage(t *testing.T) {
	got := FormatTokenUsage(llm.Usage{InputTokens: 12, OutputTokens: 5})
	want := "tokens: 17 total (input 12, output 5)"
	if got != want {
		t.Fatalf("FormatTokenUsage() = %q, want %q", got, want)
	}
}

func TestApp_REPLProcessesMultipleLines(t *testing.T) {
	a, prov := newStubApp(t,
		llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "one"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 1, OutputTokens: 2},
		},
		llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "two"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 3, OutputTokens: 4},
		},
	)

	in := strings.NewReader("first\n\nsecond\n") // blank line is ignored
	var out bytes.Buffer
	if err := a.REPL(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, "one") || !strings.Contains(body, "two") {
		t.Fatalf("repl output = %q", body)
	}
	for _, want := range []string{
		"tokens: 3 total (input 1, output 2)",
		"tokens: 10 total (input 4, output 6)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("repl output missing %q in:\n%s", want, body)
		}
	}
	if prov.calls != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", prov.calls)
	}
}

func TestApp_REPLContinuesAfterTurnError(t *testing.T) {
	// First call errors (stub exhausted on call 0 if we provide nothing) ->
	// REPL should print "error:" and keep reading. Use a single-reply stub
	// and feed two lines.
	a, _ := newStubApp(t,
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	)
	in := strings.NewReader("first\nsecond\n")
	var out bytes.Buffer
	if err := a.REPL(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, "ok") {
		t.Fatalf("first turn missing: %q", body)
	}
	if !strings.Contains(body, "error:") {
		t.Fatalf("second turn should have errored: %q", body)
	}
}

func TestApp_VerboseEmitsToStderr(t *testing.T) {
	dir := t.TempDir()
	prov := &stubProvider{replies: []llm.Response{
		{
			Message:    llm.TextMessage(llm.RoleAssistant, "ok"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 5, OutputTokens: 2},
		},
	}}
	var stderr bytes.Buffer
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: prov,
		WorkDir:  dir,
		Verbose:  true,
		Stderr:   &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	body := stderr.String()
	for _, want := range []string{"› user: x", "[turn 1]", "assistant: ok", "tokens: 7 total", "✓ done in"} {
		if !strings.Contains(body, want) {
			t.Errorf("verbose stderr missing %q in:\n%s", want, body)
		}
	}
}

func TestApp_SessionWritesIntoWorkDirJuex(t *testing.T) {
	dir := t.TempDir()
	prov := &stubProvider{replies: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: prov,
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	// Session must live under <WorkDir>/.juex/sessions/<id>/.
	sessRoot := filepath.Join(dir, ".juex", "sessions")
	if !strings.HasPrefix(a.Session.Dir, sessRoot) {
		t.Fatalf("session dir %s not under %s", a.Session.Dir, sessRoot)
	}
}

func TestApp_WritesSessionHistoryWithAlias(t *testing.T) {
	dir := t.TempDir()
	prov := &stubProvider{replies: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: prov,
		WorkDir:  dir,
		Alias:    "daily",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	h, err := session.LoadHistory(filepath.Join(dir, ".juex", "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if h.Last == nil {
		t.Fatal("history last is nil")
	}
	if h.Last.ID != a.Session.ID || h.Last.Alias != "daily" {
		t.Fatalf("last = %+v, want id %s alias daily", h.Last, a.Session.ID)
	}
	if len(h.Sessions) != 1 || h.Sessions[0].ID != a.Session.ID {
		t.Fatalf("sessions = %+v", h.Sessions)
	}
}

func TestApp_NewWithoutKeyFails(t *testing.T) {
	_, err := New(Options{
		Config:  config.Config{ProviderID: "openai" /* no key */, Model: "m", WorkDir: t.TempDir()},
		WorkDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestApp_NewSoftFailsOptionalMCPStartup(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".agents", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"alpha":{"command":""}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: &stubProvider{},
		WorkDir:  dir,
		Stderr:   &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	status := a.MCPStatus()
	if status.Configured != 1 || status.Connected != 0 || status.Errors != 1 {
		t.Fatalf("mcp status = %+v", status)
	}
	if len(status.Servers) != 1 || status.Servers[0].Status != "error" || !strings.Contains(status.Servers[0].Error, "missing command") {
		t.Fatalf("mcp servers = %+v", status.Servers)
	}
	if !strings.Contains(stderr.String(), `optional MCP server "alpha" is unavailable`) {
		t.Fatalf("stderr missing warning:\n%s", stderr.String())
	}
}

func TestNew_ResumeDirReusesExistingSession(t *testing.T) {
	work := t.TempDir()
	sessionsRoot := filepath.Join(work, ".juex", "sessions")
	id := "20260506T103500-resume001"
	dir := filepath.Join(sessionsRoot, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := New(Options{
		Config:    config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider:  &stubProvider{},
		WorkDir:   work,
		ResumeDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if a.Session.ID != id {
		t.Errorf("session id = %s, want %s", a.Session.ID, id)
	}
	if a.Session.Dir != dir {
		t.Errorf("session dir = %s, want %s", a.Session.Dir, dir)
	}
	if len(a.Session.History) != 2 {
		t.Errorf("history len = %d, want 2", len(a.Session.History))
	}
}

func TestApp_NewDefaultsWorkDirToCwd(t *testing.T) {
	// Switch into a fresh tempdir for this test so the default-cwd path
	// does not leak files into the package directory.
	prev, _ := os.Getwd()
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	prov := &stubProvider{replies: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m"},
		Provider: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Session == nil {
		t.Fatal("session not built")
	}
	// Resolve both sides to canonical paths before comparing:
	// - macOS rewrites /var/... -> /private/var/...
	// - Windows can return 8.3 short names (RUNNER~1) where the long form
	//   is "runneradmin"; EvalSymlinks normalises to the long form.
	resolveSessionParent := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			return p
		}
		return r
	}
	wantParent := resolveSessionParent(filepath.Join(dir, ".juex", "sessions"))
	gotParent := resolveSessionParent(filepath.Dir(a.Session.Dir))
	if !strings.HasPrefix(gotParent, wantParent) {
		t.Fatalf("session dir %q (resolved parent %q) not under %q",
			a.Session.Dir, gotParent, wantParent)
	}
}
