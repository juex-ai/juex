package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/runtime"
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
		{input: "/status now", handled: true, wantErr: true},
		{input: "/status\tnow", handled: true, wantErr: true},
		{input: "/unknown", handled: true, wantErr: true},
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

func TestApp_RunUnknownSlashSkipsProvider(t *testing.T) {
	a, prov := newStubApp(t)
	_, err := a.Run(context.Background(), "/bogus")
	if err == nil {
		t.Fatal("expected unknown slash error")
	}
	var slashErr *UnknownSlashCommandError
	if !errors.As(err, &slashErr) {
		t.Fatalf("err = %T, want UnknownSlashCommandError", err)
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
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
