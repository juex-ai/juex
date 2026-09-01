package app

import (
	"context"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
)

func TestParseSlashCommand(t *testing.T) {
	tests := []struct {
		input   string
		name    string
		args    string
		handled bool
		err     bool
	}{
		{input: "/new", name: SlashNew, handled: true},
		{input: "/compact keep decisions", name: SlashCompact, args: "keep decisions", handled: true},
		{input: "/goal ship it", name: SlashGoal, args: "ship it", handled: true},
		{input: "/status", name: SlashStatus, handled: true},
		{input: "/new extra", handled: true, err: true},
		{input: "/unknown"},
	}
	for _, test := range tests {
		command, handled, err := ParseSlashCommand(test.input)
		if handled != test.handled || (err != nil) != test.err || command.Name != test.name || command.Args != test.args {
			t.Fatalf("ParseSlashCommand(%q) = %+v, %t, %v", test.input, command, handled, err)
		}
	}
}

func TestNewSlashCreatesGenerationWithoutProviderTurn(t *testing.T) {
	app, provider := newStubApp(t)
	if err := app.Thread.Append(llm.TextMessage(llm.RoleUser, "old")); err != nil {
		t.Fatal(err)
	}
	contextUsage := &llm.ContextUsage{ContextWindow: 100, TotalTokens: 95}
	app.Thread.RecordResponseUsage(llm.Usage{InputTokens: 10, OutputTokens: 2}, contextUsage)
	app.Status.Publish(events.Event{ID: "usage-before-new", Type: "llm.responded", Payload: runtime.LLMRespondedPayload{
		TokenUsage: llm.Usage{InputTokens: 10, OutputTokens: 2}, ContextUsage: contextUsage,
	}})
	result := app.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: SlashNew})
	if result.Kind != TurnAdmissionCommandCompleted || result.Command == nil {
		t.Fatalf("admission = %+v", result)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
	replay := app.Thread.ReplaySnapshot()
	if replay.Projection.CurrentGeneration.ID != "g000002" || len(app.Thread.History) != 0 {
		t.Fatalf("new generation = %s history=%+v", replay.Projection.CurrentGeneration.ID, app.Thread.History)
	}
	if len(replay.Activities) != 1 || replay.Activities[0].Type != thread.FactContextRenewed || replay.Activities[0].Summary != nil {
		t.Fatalf("renew activity = %+v", replay.Activities)
	}
	if replay.Projection.ContextUsage != nil || app.Thread.ContextUsageSnapshot() != nil || app.Status.Snapshot().ContextUsage != nil {
		t.Fatalf("new generation retained Context Usage: projection=%+v Thread=%+v status=%+v",
			replay.Projection.ContextUsage, app.Thread.ContextUsageSnapshot(), app.Status.Snapshot().ContextUsage)
	}
	status := app.Status.Snapshot()
	if status.Cursor != "usage-before-new" {
		t.Fatalf("new generation changed durable status cursor: %q", status.Cursor)
	}
	if got := status.TokenUsage; got.InputTokens != 10 || got.OutputTokens != 2 {
		t.Fatalf("new generation lost cumulative status Token Usage: %+v", got)
	}
}

func TestStatusSlashReportsThreadAndGeneration(t *testing.T) {
	app, _ := newStubApp(t)
	result, err := app.ExecuteParsedSlashCommand(context.Background(), SlashCommand{Name: SlashStatus})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status == nil || result.Status.ThreadID != thread.MainID || result.Status.GenerationID != thread.InitialGeneration {
		t.Fatalf("status = %+v", result.Status)
	}
	if !strings.Contains(result.Text, "thread: 0 (main)") || !strings.Contains(result.Text, "generation: g000001") {
		t.Fatalf("status text = %q", result.Text)
	}
}

func TestCompactionStatusUsesCumulativeReplayCount(t *testing.T) {
	summary := llm.TextMessage(llm.RoleUser, "compact memory")
	status := compactionStatusFromReplay(thread.ReplayState{
		CompactionCount: 3,
		Activities: []thread.Activity{{
			Type:    thread.FactContextCompacted,
			Summary: &summary,
		}},
	})
	if status.Count != 3 || status.MemoryTokens == 0 {
		t.Fatalf("compaction status = %+v", status)
	}
}

func TestCompactSlashStartsCompactedGeneration(t *testing.T) {
	app, _ := newStubApp(t, llm.Response{
		Message: llm.TextMessage(llm.RoleAssistant, "summary"), StopReason: llm.StopEndTurn,
	})
	app.Engine.Compaction = runtime.DefaultCompactionPolicy()
	app.Engine.Compaction.KeepRecentTokens = 100
	for i := 0; i < 6; i++ {
		if err := app.Thread.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 1500))); err != nil {
			t.Fatal(err)
		}
		if err := app.Thread.Append(llm.TextMessage(llm.RoleAssistant, "working")); err != nil {
			t.Fatal(err)
		}
	}
	result, err := app.ExecuteParsedSlashCommand(context.Background(), SlashCommand{Name: SlashCompact})
	if err != nil {
		t.Fatal(err)
	}
	if result.Compact == nil || result.Compact.MessageID == "" {
		t.Fatalf("compact result = %+v", result)
	}
	replay := app.Thread.ReplaySnapshot()
	if replay.Projection.CurrentGeneration.ID != "g000002" || len(replay.Activities) != 1 || replay.Activities[0].Type != thread.FactContextCompacted {
		t.Fatalf("compact replay = %+v", replay)
	}
}
