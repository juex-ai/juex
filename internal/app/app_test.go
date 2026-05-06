package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
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
		Config:   config.Config{ProviderType: "openai", APIKey: "x", Model: "m", WorkDir: dir},
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
	})
	out, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello back" {
		t.Fatalf("out = %q", out)
	}
}

func TestApp_REPLProcessesMultipleLines(t *testing.T) {
	a, prov := newStubApp(t,
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "one"), StopReason: llm.StopEndTurn},
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "two"), StopReason: llm.StopEndTurn},
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
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	var stderr bytes.Buffer
	a, err := New(Options{
		Config:   config.Config{ProviderType: "openai", APIKey: "x", Model: "m", WorkDir: dir},
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
	for _, want := range []string{"› user: x", "[turn 1]", "assistant: ok", "✓ done in"} {
		if !strings.Contains(body, want) {
			t.Errorf("verbose stderr missing %q in:\n%s", want, body)
		}
	}
}

func TestApp_SessionWritesIntoWorkDirAgents(t *testing.T) {
	dir := t.TempDir()
	prov := &stubProvider{replies: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	a, err := New(Options{
		Config:   config.Config{ProviderType: "openai", APIKey: "x", Model: "m", WorkDir: dir},
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
	// Session must live under <WorkDir>/.agents/sessions/<id>/
	sessRoot := filepath.Join(dir, ".agents", "sessions")
	if !strings.HasPrefix(a.Session.Dir, sessRoot) {
		t.Fatalf("session dir %s not under %s", a.Session.Dir, sessRoot)
	}
}

func TestApp_NewWithoutKeyFails(t *testing.T) {
	_, err := New(Options{
		Config:  config.Config{ProviderType: "openai" /* no key */, Model: "m", WorkDir: t.TempDir()},
		WorkDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestNew_ResumeDirReusesExistingSession(t *testing.T) {
	work := t.TempDir()
	sessionsRoot := filepath.Join(work, ".agents", "sessions")
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
		Config:    config.Config{ProviderType: "openai", APIKey: "x", Model: "m", WorkDir: work},
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
		Config:   config.Config{ProviderType: "openai", APIKey: "x", Model: "m"},
		Provider: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Session == nil {
		t.Fatal("session not built")
	}
	// macOS resolves /var/... -> /private/var/... so resolve before compare.
	resolved, _ := filepath.EvalSymlinks(dir)
	if resolved == "" {
		resolved = dir
	}
	if !strings.HasPrefix(a.Session.Dir, filepath.Join(resolved, ".agents", "sessions")) {
		t.Fatalf("session dir %q not under %q/.agents/sessions", a.Session.Dir, resolved)
	}
}
