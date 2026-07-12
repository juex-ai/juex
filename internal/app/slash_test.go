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
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		input   string
		handled bool
		name    string
		args    string
		wantErr bool
	}{
		{input: "hello", handled: false},
		{input: " /status ", handled: true, name: SlashStatus},
		{input: "/compact", handled: true, name: SlashCompact},
		{input: "/compact focus on API changes", handled: true, name: SlashCompact, args: "focus on API changes"},
		{input: "/goal", handled: true, name: SlashGoal},
		{input: "/goal finish the PR", handled: true, name: SlashGoal, args: "finish the PR"},
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
			if cmd.Args != tc.args {
				t.Fatalf("args = %q, want %q", cmd.Args, tc.args)
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
	for _, want := range []string{"model: not configured", "observables: 0/0 running, 0 errors", "skills: 0", "compact: 0, memory: 0 tokens", "success: llm n/a, tools n/a", "turn: idle", "queued input: 0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Juex status") {
		t.Fatalf("status text should not include heading:\n%s", text)
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

func TestStatusSnapshotTextShowsStartedAtInLocalTime(t *testing.T) {
	originalLocal := time.Local
	loc := time.FixedZone("JST", 9*60*60)
	time.Local = loc
	t.Cleanup(func() { time.Local = originalLocal })

	text := (StatusSnapshot{
		SessionID: "20260707T180000-abc12345",
		Turns:     2,
		StartedAt: time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC),
	}).Text()
	want := "session: 20260707T180000-abc12345 (started 2026-07-08 03:00:00, 2 turns)"
	if !strings.Contains(text, want) {
		t.Fatalf("status text missing local started_at %q:\n%s", want, text)
	}
}

func TestStatusSnapshotTextIncludesObservableCounts(t *testing.T) {
	text := (StatusSnapshot{
		Observables: StatusObservablesSnapshot{
			Configured: 3,
			Running:    2,
			Errors:     1,
		},
	}).Text()
	want := statusLabel(statusIconObservable, "observables: 2/3 running, 1 errors")
	if !strings.Contains(text, want) {
		t.Fatalf("status text missing %q:\n%s", want, text)
	}
}

func TestStatusSnapshotTextUsesIntentionalIconLabels(t *testing.T) {
	text := (StatusSnapshot{
		SessionID:   "session-1",
		Turns:       3,
		SessionKind: "primary",
		Active:      true,
		WorkDir:     "/tmp/work",
		Provider: ProviderStatusSnapshot{
			ID:       "openai",
			Protocol: "openai/responses",
			Model:    "gpt-4.1",
		},
		MCP:        MCPStatus{Connected: 1, Configured: 2, Errors: 3},
		SkillCount: 4,
		TokenUsage: llm.Usage{InputTokens: 5, OutputTokens: 7},
		ContextUsage: &llm.ContextUsage{
			TotalTokens:   10,
			ContextWindow: 100,
			Model:         "gpt-4.1",
		},
		PendingInput: runtime.PendingInputStatus{
			TurnID:           "turn-1",
			PendingCount:     2,
			MaxPendingInputs: 5,
		},
	}).Text()
	for _, want := range []string{
		statusLabel(statusIconSession, "session:"),
		statusLabel(statusIconSessionKind, "session kind:"),
		statusLabel(statusIconWorkDir, "workdir:"),
		statusLabel(statusIconProvider, "model:"),
		statusLabel(statusIconMCP, "mcp:"),
		statusLabel(statusIconObservable, "observables:"),
		statusLabel(statusIconSkills, "skills:"),
		statusLabel(statusIconTokens, "tokens:"),
		statusLabel(statusIconContext, "context:"),
		statusLabel(statusIconTurn, "turn:"),
		statusLabel(statusIconQueuedInput, "queued input:"),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
}

func TestStatusSnapshotTextUsesCompactModelAndCacheHit(t *testing.T) {
	text := (StatusSnapshot{
		Provider: ProviderStatusSnapshot{
			ID:       "ark",
			Protocol: "openai/chat",
			Model:    "deepseek-v4-pro",
			BaseURL:  "https://ark.cn-beijing.volces.com/api/coding/v3",
		},
		ContextUsage: &llm.ContextUsage{
			Model:             "ark:deepseek-v4-pro",
			ContextWindow:     256000,
			InputTokens:       32000,
			OutputTokens:      47,
			CachedInputTokens: 12000,
			TotalTokens:       32047,
		},
	}).Text()
	for _, want := range []string{
		statusLabel(statusIconProvider, "model: ark:deepseek-v4-pro"),
		statusLabel(statusIconContext, "context: ~32k/256k tokens, cache hit 37.5%"),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"openai/chat", "https://ark.cn-beijing.volces.com"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("status text should not include %q:\n%s", notWant, text)
		}
	}
}

func TestStatusSnapshotContextCacheHitUsesCachedInputTokens(t *testing.T) {
	text := (StatusSnapshot{
		ContextUsage: &llm.ContextUsage{
			ContextWindow:     1_000_000,
			InputTokens:       120_000,
			OutputTokens:      500,
			CachedInputTokens: 0,
			TotalTokens:       120_500,
		},
	}).Text()
	want := statusLabel(statusIconContext, "context: ~120.5k/1m tokens, cache hit 0%")
	if !strings.Contains(text, want) {
		t.Fatalf("status text missing %q:\n%s", want, text)
	}
}

func TestFormatCompactTokenCountPromotesRoundedThresholds(t *testing.T) {
	cases := map[int]string{
		999_949:     "999.9k",
		999_950:     "1m",
		999_950_000: "1b",
	}
	for value, want := range cases {
		if got := FormatCompactTokenCount(value); got != want {
			t.Fatalf("FormatCompactTokenCount(%d) = %q, want %q", value, got, want)
		}
	}
}

func TestStatusSnapshotIncludesSessionCompactionAndSuccessRates(t *testing.T) {
	a, _ := newStubApp(t)
	appendCompactMessage(t, a.Session, "first compact summary", 80)
	appendCompactMessage(t, a.Session, "latest compact summary", 480)
	for i := 0; i < 4; i++ {
		if err := a.Session.AppendEvent(events.Event{Type: "llm.requested"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := a.Session.AppendEvent(events.Event{Type: "llm.responded"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := a.Session.AppendEvent(events.Event{Type: toolevents.RequestedType}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := a.Session.AppendEvent(events.Event{Type: toolevents.CompletedType}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Session.AppendEvent(events.Event{Type: toolevents.ErroredType}); err != nil {
		t.Fatal(err)
	}

	text := a.StatusSnapshot(time.Now().UTC()).Text()
	for _, want := range []string{
		statusLabel(statusIconCompact, "compact: 2, memory: ~120 tokens"),
		statusLabel(statusIconSuccess, "success: llm 3/4 (75%), tools 4/5 (80%)"),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
}

func appendCompactMessage(t *testing.T, sess *session.Session, summary string, summaryChars int) {
	t.Helper()
	msg := llm.TextMessage(llm.RoleUser, summary)
	msg.Kind = llm.MessageKindCompact
	msg.Compaction = &llm.CompactionMetadata{SummaryChars: summaryChars}
	if err := sess.Append(msg); err != nil {
		t.Fatal(err)
	}
}

func TestApp_RunStatusSlashSkipsProvider(t *testing.T) {
	a, prov := newStubApp(t)
	out, err := a.Run(context.Background(), "/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"session:", "model:", "observables:", "tokens:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Juex status") {
		t.Fatalf("status output should not include heading:\n%s", out)
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
	}
	if len(a.Session.History) != 0 {
		t.Fatalf("history len = %d, want 0", len(a.Session.History))
	}
}

func TestApp_RunNewSlashSwitchesActivePrimary(t *testing.T) {
	a, prov := newStubApp(t, llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "Hello, I can help with coding tasks. What would you like to do next?"),
		StopReason: llm.StopEndTurn,
	})
	oldID := a.Session.ID
	out, err := a.Run(context.Background(), "/new")
	if err != nil {
		t.Fatal(err)
	}
	if prov.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.calls)
	}
	if a.Session.ID == oldID {
		t.Fatalf("session id did not change: %s", oldID)
	}
	if a.Session.Kind != session.KindPrimary || !a.Session.Active {
		t.Fatalf("session kind/active = %q/%v, want primary active", a.Session.Kind, a.Session.Active)
	}
	if !strings.Contains(out, "What would you like to do next?") {
		t.Fatalf("output = %q", out)
	}
	if len(a.Session.History) < 2 {
		t.Fatalf("history len = %d, want at least 2", len(a.Session.History))
	}
	if got := a.Session.History[0].FirstText(); got != NewSessionGreetingPrompt() {
		t.Fatalf("first history text = %q, want greeting prompt", got)
	}
	if got := prov.histories[0][len(prov.histories[0])-1].FirstText(); got != NewSessionGreetingPrompt() {
		t.Fatalf("provider prompt = %q, want greeting prompt", got)
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
	if err := a.REPL(context.Background(), strings.NewReader("/status\n"), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "observables: 0/0 running, 0 errors") || strings.Contains(out.String(), "Juex status") {
		t.Fatalf("repl output = %q", out.String())
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
	}
}

func TestAppStatusIncludesGoalState(t *testing.T) {
	a, _ := newStubApp(t)
	if _, err := a.Engine.GoalState.Create("finish goal tools", "tests pass"); err != nil {
		t.Fatal(err)
	}

	status := a.StatusSnapshot(time.Now().UTC())
	if status.Goal == nil || status.Goal.Description != "finish goal tools" || status.Goal.Status != runtime.GoalStatusInProgress {
		t.Fatalf("goal status = %+v", status.Goal)
	}
	text := status.Text()
	for _, want := range []string{"goal: in_progress", "finish goal tools"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}
}
