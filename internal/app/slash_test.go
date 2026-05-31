package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
)

func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		input   string
		handled bool
		name    string
		wantErr bool
	}{
		{input: "hello", handled: false},
		{input: " /status ", handled: true, name: SlashStatus},
		{input: "/compact", handled: true, name: SlashCompact},
		{input: "/new", handled: true, name: SlashNew},
		{input: "/status now", handled: true, wantErr: true},
		{input: "/status\tnow", handled: true, wantErr: true},
		{input: "/unknown", handled: false},
		{input: "/foo bar", handled: false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			cmd, handled, err := ParseSlashCommand(tc.input)
			if handled != tc.handled {
				t.Fatalf("handled = %v, want %v", handled, tc.handled)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if cmd.Name != tc.name {
				t.Fatalf("name = %q, want %q", cmd.Name, tc.name)
			}
		})
	}
}

func TestParseSlashCommandRejectsArgumentsExplicitly(t *testing.T) {
	_, handled, err := ParseSlashCommand("/status verbose")
	if !handled {
		t.Fatal("handled = false, want true")
	}
	var argsErr *SlashCommandArgumentsError
	if !errors.As(err, &argsErr) {
		t.Fatalf("err = %T, want SlashCommandArgumentsError", err)
	}
	if argsErr.Name != SlashStatus || argsErr.Args != "verbose" {
		t.Fatalf("argsErr = %+v", argsErr)
	}
}

func TestStatusSnapshotNilApp(t *testing.T) {
	var a *App
	text := a.StatusSnapshot(time.Time{}).Text()
	for _, want := range []string{"Juex status", "provider: not configured", "skills: 0", "turn: idle", "queued input: 0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
}

func TestStatusSnapshotTextSeparatesTurnAndQueue(t *testing.T) {
	text := (StatusSnapshot{
		PendingInput: runtime.PendingInputStatus{
			TurnID:           "turn-1",
			PendingCount:     2,
			MaxPendingInputs: 5,
		},
	}).Text()
	for _, want := range []string{"turn: running", "queued input: 2/5"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
}

func TestStatusSnapshotTextIncludesEmojiLabels(t *testing.T) {
	text := (StatusSnapshot{}).Text()
	for _, want := range []string{"📊 Juex status", "🤖 provider:", "⚙️ turn:", "📥 queued input:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
}

func TestApp_RunStatusSlashSkipsProvider(t *testing.T) {
	a, prov := newStubApp(t)
	out, err := a.Run(context.Background(), "/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Juex status", "session:", "provider:", "tokens:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q in:\n%s", want, out)
		}
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
	}
	if len(a.Session.History) != 0 {
		t.Fatalf("history len = %d, want 0", len(a.Session.History))
	}
}

func TestApp_RunNewSlashSwitchesActivePrimary(t *testing.T) {
	a, prov := newStubApp(t)
	oldID := a.Session.ID
	out, err := a.Run(context.Background(), "/new")
	if err != nil {
		t.Fatal(err)
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
	}
	if a.Session.ID == oldID {
		t.Fatalf("session id did not change: %s", oldID)
	}
	if a.Session.Kind != session.KindPrimary || !a.Session.Active {
		t.Fatalf("session kind/active = %q/%v, want primary active", a.Session.Kind, a.Session.Active)
	}
	if !strings.Contains(out, "New primary session: "+a.Session.ID) {
		t.Fatalf("output = %q", out)
	}
	h, err := session.LoadHistory(filepath.Join(a.cfg.WorkDir, ".juex", "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != a.Session.ID {
		t.Fatalf("history active = %+v, want %s", h.Active, a.Session.ID)
	}
}

func TestApp_RunNewSlashFailsInSideSession(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{
		Config:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider:    &stubProvider{replies: []llm.Response{}},
		WorkDir:     dir,
		SessionMode: SessionModeNewSide,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	_, err = a.Run(context.Background(), "/new")
	if err == nil || !strings.Contains(err.Error(), "side sessions cannot switch workspace active session") {
		t.Fatalf("err = %v", err)
	}
}

func TestApp_RunUnknownSlashReachesProvider(t *testing.T) {
	a, prov := newStubApp(t, llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "handled slash-like prompt"),
		StopReason: llm.StopEndTurn,
	})
	out, err := a.Run(context.Background(), "/bogus")
	if err != nil {
		t.Fatal(err)
	}
	if out != "handled slash-like prompt" {
		t.Fatalf("out = %q", out)
	}
	if prov.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.calls)
	}
	if len(a.Session.History) < 1 || a.Session.History[0].FirstText() != "/bogus" {
		t.Fatalf("history first message = %+v", a.Session.History)
	}
}

func TestApp_REPLProcessesStatusSlash(t *testing.T) {
	a, prov := newStubApp(t)
	var out bytes.Buffer
	if err := a.REPL(context.Background(), strings.NewReader("/status\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Juex status") {
		t.Fatalf("repl output = %q", out.String())
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
	}
}
