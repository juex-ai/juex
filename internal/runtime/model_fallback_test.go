package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/provenance"
	"github.com/juex-ai/juex/internal/tools"
)

type fallbackProviderResult struct {
	response llm.Response
	err      error
}

func TestTurnMultiLevelFallbackUsesRealTransitionsAndFinalServingNotice(t *testing.T) {
	primary := &fallbackProvider{name: "a:model", results: []fallbackProviderResult{{err: errors.New("status 503")}}}
	middle := &fallbackProvider{name: "b:model", results: []fallbackProviderResult{{err: errors.New("status 401")}}}
	last := &fallbackProvider{name: "c:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "from c"),
		StopReason: llm.StopEndTurn,
	}}}}
	eng, bus := newEngine(t, primary, false)
	eng.NotifyModelChanges = true
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "a:model", Provider: primary, ContextWindow: 128000},
		{Ref: "b:model", Provider: middle, ContextWindow: 128000},
		{Ref: "c:model", Provider: last, ContextWindow: 128000},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	previous := llm.TextMessage(llm.RoleAssistant, "from a")
	previous.Model = "a:model"
	if err := eng.Session.Append(previous); err != nil {
		t.Fatal(err)
	}
	var sequence []string
	bus.Subscribe("llm.requested", func(event events.Event) {
		sequence = append(sequence, "request:"+event.Payload.(LLMRequestedPayload).Model)
	})
	bus.Subscribe("llm.fallback", func(event events.Event) {
		payload := event.Payload.(LLMFallbackPayload)
		sequence = append(sequence, "fallback:"+payload.From+">"+payload.To)
	})

	if out, err := eng.Turn(context.Background(), "continue"); err != nil || out != "from c" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	wantSequence := "request:a:model,fallback:a:model>b:model,request:b:model,fallback:b:model>c:model,request:c:model"
	if got := strings.Join(sequence, ","); got != wantSequence {
		t.Fatalf("sequence = %q, want %q", got, wantSequence)
	}
	history := eng.Session.History
	notices := 0
	for _, message := range history {
		if message.Kind == llm.MessageKindModelChange {
			notices++
			if strings.Contains(message.FirstText(), "b:model") || !strings.Contains(message.FirstText(), "a:model") || !strings.Contains(message.FirstText(), "c:model") {
				t.Fatalf("final notice = %q", message.FirstText())
			}
		}
	}
	if notices != 1 || history[len(history)-1].Model != "c:model" {
		t.Fatalf("history notices=%d tail=%+v", notices, history[len(history)-1])
	}
}

func TestTurnFallbackExcludesContextOverflow(t *testing.T) {
	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{
		{err: errors.New("status 400: context_length_exceeded")},
		{err: errors.New("status 400: context_length_exceeded")},
	}}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "unexpected"),
		StopReason: llm.StopEndTurn,
	}}}}
	eng, _ := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	eng.Compaction.Enabled = false

	if _, err := eng.Turn(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "context_length_exceeded") {
		t.Fatalf("err = %v", err)
	}
	if backup.calls != 0 {
		t.Fatalf("backup calls = %d, want 0", backup.calls)
	}
}

func TestTurnRecoversHigherPriorityModelWithPersistedNotice(t *testing.T) {
	now := time.Unix(10_000, 0)
	health := llm.NewModelHealth(llm.ModelHealthOptions{Now: func() time.Time { return now }})
	refs := []string{"primary:model", "backup:model"}
	failed, ok := health.Acquire(refs, nil)
	if !ok {
		t.Fatal("missing initial primary ticket")
	}
	health.Complete(failed.Ticket, llm.ModelHealthEligibleFailure, "transient")
	now = now.Add(30 * time.Second)

	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "primary restored"),
		StopReason: llm.StopEndTurn,
	}}}}
	backup := &fallbackProvider{name: "backup:model"}
	eng, _ := newEngine(t, primary, false)
	eng.NotifyModelChanges = true
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = health
	previous := llm.TextMessage(llm.RoleAssistant, "backup response")
	previous.Model = "backup:model"
	if err := eng.Session.Append(previous); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "try again"); err != nil {
		t.Fatal(err)
	}
	history := eng.Session.History
	notice := history[len(history)-2]
	if notice.Kind != llm.MessageKindModelChange || !strings.Contains(notice.FirstText(), "healthy again") {
		t.Fatalf("recovery notice = %+v", notice)
	}
	if backup.calls != 0 || history[len(history)-1].Model != "primary:model" {
		t.Fatalf("backup calls=%d tail=%+v", backup.calls, history[len(history)-1])
	}
}

func TestTurnRecoveryDoesNotNotifyModelChangesByDefault(t *testing.T) {
	now := time.Unix(15_000, 0)
	health := llm.NewModelHealth(llm.ModelHealthOptions{Now: func() time.Time { return now }})
	refs := []string{"primary:model", "backup:model"}
	failed, ok := health.Acquire(refs, nil)
	if !ok {
		t.Fatal("missing initial primary ticket")
	}
	health.Complete(failed.Ticket, llm.ModelHealthEligibleFailure, "transient")
	now = now.Add(30 * time.Second)

	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "primary restored"),
		StopReason: llm.StopEndTurn,
	}}}}
	backup := &fallbackProvider{name: "backup:model"}
	eng, _ := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = health
	previous := llm.TextMessage(llm.RoleAssistant, "backup response")
	previous.Model = "backup:model"
	if err := eng.Session.Append(previous); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "try again"); err != nil {
		t.Fatal(err)
	}
	for _, message := range primary.histories[0] {
		if message.Kind == llm.MessageKindModelChange {
			t.Fatalf("provider-visible recovery notice = %+v", message)
		}
	}
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindModelChange {
			t.Fatalf("persisted recovery notice = %+v", message)
		}
	}
	if backup.calls != 0 || eng.Session.History[len(eng.Session.History)-1].Model != "primary:model" {
		t.Fatalf("backup calls=%d tail=%+v", backup.calls, eng.Session.History[len(eng.Session.History)-1])
	}
}

func TestTurnFailedRecoveryProbeDoesNotPersistFalseNotice(t *testing.T) {
	now := time.Unix(20_000, 0)
	health := llm.NewModelHealth(llm.ModelHealthOptions{Now: func() time.Time { return now }})
	refs := []string{"primary:model", "backup:model"}
	failed, _ := health.Acquire(refs, nil)
	health.Complete(failed.Ticket, llm.ModelHealthEligibleFailure, "transient")
	now = now.Add(30 * time.Second)

	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{{err: errors.New("status 503")}}}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "still backup"),
		StopReason: llm.StopEndTurn,
	}}}}
	eng, bus := newEngine(t, primary, false)
	eng.NotifyModelChanges = true
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = health
	previous := llm.TextMessage(llm.RoleAssistant, "backup response")
	previous.Model = "backup:model"
	if err := eng.Session.Append(previous); err != nil {
		t.Fatal(err)
	}
	var fallbackEvent LLMFallbackPayload
	bus.Subscribe("llm.fallback", func(event events.Event) { fallbackEvent = event.Payload.(LLMFallbackPayload) })

	if _, err := eng.Turn(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindModelChange {
			t.Fatalf("false recovery notice persisted: %+v", message)
		}
	}
	if !fallbackEvent.Probe || fallbackEvent.From != "primary:model" || fallbackEvent.To != "backup:model" {
		t.Fatalf("fallback event = %+v", fallbackEvent)
	}
}

func TestTurnFallbackBatchFailureLeavesNoNoticeUsageOrRespondedEvent(t *testing.T) {
	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{{err: errors.New("status 503")}}}
	invalid := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
		Type:  llm.BlockToolUse,
		Input: map[string]any{"invalid": func() {}},
	}}}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{response: llm.Response{
		Message: invalid, StopReason: llm.StopToolUse, Usage: llm.Usage{InputTokens: 10, OutputTokens: 2},
	}}}}
	eng, bus := newEngine(t, primary, false)
	eng.NotifyModelChanges = true
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	previous := llm.TextMessage(llm.RoleAssistant, "primary response")
	previous.Model = "primary:model"
	if err := eng.Session.Append(previous); err != nil {
		t.Fatal(err)
	}
	responded := 0
	bus.Subscribe("llm.responded", func(events.Event) { responded++ })

	if _, err := eng.Turn(context.Background(), "continue"); err == nil {
		t.Fatal("Turn err = nil, want batch marshal failure")
	}
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindModelChange {
			t.Fatalf("orphan notice = %+v", message)
		}
	}
	if responded != 0 || !eng.Session.TokenUsage.IsZero() {
		t.Fatalf("responded=%d usage=%+v", responded, eng.Session.TokenUsage)
	}
}

func TestTurnFallbackChainExhaustionEmitsEmptyDestination(t *testing.T) {
	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{{err: errors.New("status 503")}}}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{err: errors.New("status 403")}}}
	eng, bus := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	var got []LLMFallbackPayload
	bus.Subscribe("llm.fallback", func(event events.Event) { got = append(got, event.Payload.(LLMFallbackPayload)) })

	_, err := eng.Turn(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "primary:model") || !strings.Contains(err.Error(), "backup:model") {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 2 || got[1].From != "backup:model" || got[1].To != "" {
		t.Fatalf("events = %+v", got)
	}
}

func TestSharedModelHealthSkipsOpenPrimaryAcrossEngines(t *testing.T) {
	health := llm.NewModelHealth(llm.ModelHealthOptions{})
	primary1 := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{{err: errors.New("status 503")}}}
	backup1 := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "first fallback"),
		StopReason: llm.StopEndTurn,
	}}}}
	eng1, _ := newEngine(t, primary1, false)
	eng1.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary1, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup1, ContextWindow: 128000},
	}
	eng1.ModelHealth = health
	if _, err := eng1.Turn(context.Background(), "first session"); err != nil {
		t.Fatal(err)
	}

	primary2 := &fallbackProvider{name: "primary:model"}
	backup2 := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "second fallback"),
		StopReason: llm.StopEndTurn,
	}}}}
	eng2, bus2 := newEngine(t, primary2, false)
	eng2.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary2, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup2, ContextWindow: 128000},
	}
	eng2.ModelHealth = health
	var got []LLMFallbackPayload
	bus2.Subscribe("llm.fallback", func(event events.Event) { got = append(got, event.Payload.(LLMFallbackPayload)) })

	if out, err := eng2.Turn(context.Background(), "second session"); err != nil || out != "second fallback" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if primary2.calls != 0 || backup2.calls != 1 {
		t.Fatalf("second engine calls primary=%d backup=%d", primary2.calls, backup2.calls)
	}
	if len(got) != 1 || got[0].From != "primary:model" || got[0].To != "backup:model" || got[0].Reason != "transient" {
		t.Fatalf("fallback events = %+v", got)
	}
}

func TestTurnReportsBreakerSkipOnlyOnceWhileContinuingChain(t *testing.T) {
	health := llm.NewModelHealth(llm.ModelHealthOptions{})
	opened, ok := health.Acquire([]string{"a:model"}, nil)
	if !ok {
		t.Fatal("missing circuit ticket")
	}
	health.Complete(opened.Ticket, llm.ModelHealthEligibleFailure, "transient")

	a := &fallbackProvider{name: "a:model"}
	b := &fallbackProvider{name: "b:model", results: []fallbackProviderResult{{err: errors.New("status 503")}}}
	c := &fallbackProvider{name: "c:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "from c"),
		StopReason: llm.StopEndTurn,
	}}}}
	eng, bus := newEngine(t, a, false)
	eng.NotifyModelChanges = true
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "a:model", Provider: a, ContextWindow: 128000},
		{Ref: "b:model", Provider: b, ContextWindow: 128000},
		{Ref: "c:model", Provider: c, ContextWindow: 128000},
	}
	eng.ModelHealth = health
	previous := llm.TextMessage(llm.RoleAssistant, "from a")
	previous.Model = "a:model"
	if err := eng.Session.Append(previous); err != nil {
		t.Fatal(err)
	}
	var got []string
	bus.Subscribe("llm.fallback", func(event events.Event) {
		payload := event.Payload.(LLMFallbackPayload)
		got = append(got, payload.From+">"+payload.To)
	})

	if _, err := eng.Turn(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	if sequence := strings.Join(got, ","); sequence != "a:model>b:model,b:model>c:model" {
		t.Fatalf("fallback sequence = %q", sequence)
	}
	if a.calls != 0 || b.calls != 1 || c.calls != 1 {
		t.Fatalf("calls a=%d b=%d c=%d", a.calls, b.calls, c.calls)
	}
	var notices []llm.Message
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindModelChange {
			notices = append(notices, message)
		}
	}
	if len(notices) != 1 || !strings.Contains(notices[0].FirstText(), "a:model") || strings.Contains(notices[0].FirstText(), "b:model") || !strings.Contains(notices[0].FirstText(), "c:model") {
		t.Fatalf("persisted notices = %+v", notices)
	}
}

func TestTurnExhaustionErrorIncludesEarlierBreakerSkip(t *testing.T) {
	health := llm.NewModelHealth(llm.ModelHealthOptions{})
	opened, ok := health.Acquire([]string{"primary:model"}, nil)
	if !ok {
		t.Fatal("missing circuit ticket")
	}
	health.Complete(opened.Ticket, llm.ModelHealthEligibleFailure, "transient")

	primary := &fallbackProvider{name: "primary:model"}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{err: errors.New("status 403")}}}
	eng, _ := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = health

	_, err := eng.Turn(context.Background(), "continue")
	if err == nil || !strings.Contains(err.Error(), "primary:model: unavailable (transient") || !strings.Contains(err.Error(), "backup:model: status 403") {
		t.Fatalf("exhaustion error = %v", err)
	}
	if primary.calls != 0 || backup.calls != 1 {
		t.Fatalf("calls primary=%d backup=%d", primary.calls, backup.calls)
	}
}

func TestTurnFallbackAfterToolResultDoesNotRerunTool(t *testing.T) {
	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{
		{response: llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type: llm.BlockToolUse, ToolUseID: "call-once", ToolName: "once", Input: map[string]any{},
			}}},
			StopReason: llm.StopToolUse,
		}},
		{err: errors.New("status 503")},
	}}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "continued after tool"),
		StopReason: llm.StopEndTurn,
	}}}}
	eng, _ := newEngine(t, primary, false)
	eng.NotifyModelChanges = true
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	toolCalls := 0
	if err := eng.Tools.Register(tools.Tool{
		Name: "once",
		Handler: func(context.Context, map[string]any) (string, error) {
			toolCalls++
			return "tool result", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if out, err := eng.Turn(context.Background(), "use the tool"); err != nil || out != "continued after tool" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if toolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", toolCalls)
	}
	if len(backup.histories) != 1 {
		t.Fatalf("backup histories = %d", len(backup.histories))
	}
	text := messagesText(backup.histories[0])
	if !strings.Contains(text, "tool result") || !strings.Contains(text, "primary:model") || !strings.Contains(text, "backup:model") {
		t.Fatalf("backup context missing tool result or notice:\n%s", text)
	}
}

func TestTurnSmallerWindowFallbackCompactsBeforeProviderCall(t *testing.T) {
	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{
		{err: errors.New("status 503")},
	}}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{
		{response: llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "short fallback summary"),
			StopReason: llm.StopEndTurn,
		}},
		{response: llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "served in small window"),
			StopReason: llm.StopEndTurn,
		}},
	}}
	eng, _ := newEngine(t, primary, false)
	eng.ContextWindow = 10_000
	eng.Compaction = DefaultCompactionPolicy()
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 10_000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 120},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPostCompact: {{Stdout: "Use the refreshed fallback context now."}},
	}})
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("large history ", 300))); err != nil {
		t.Fatal(err)
	}
	previous := llm.TextMessage(llm.RoleAssistant, "primary before failure")
	previous.Model = "primary:model"
	if err := eng.Session.Append(previous); err != nil {
		t.Fatal(err)
	}

	if out, err := eng.Turn(context.Background(), "continue"); err != nil || out != "served in small window" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if primary.calls != 1 || backup.calls != 2 {
		t.Fatalf("calls primary=%d backup=%d", primary.calls, backup.calls)
	}
	foundCompact := false
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindCompact {
			foundCompact = true
		}
	}
	if !foundCompact {
		t.Fatalf("history missing fallback preflight compaction: %+v", eng.Session.History)
	}
	if strings.Contains(messagesText(backup.histories[1]), strings.Repeat("large history ", 20)) {
		t.Fatal("backup received unbounded pre-compaction history")
	}
	if got := messagesText(backup.histories[1]); !strings.Contains(got, "Use the refreshed fallback context now.") {
		t.Fatalf("backup history missing post-compact policy context:\n%s", got)
	}
	if remaining := eng.pendingPolicyRuntimeContextSnapshot(); len(remaining) != 0 {
		t.Fatalf("post-compact policy context leaked after fallback request: %+v", remaining)
	}
}

func TestTurnPreflightFailureNeutrallyReleasesHalfOpenCandidate(t *testing.T) {
	now := time.Unix(30_000, 0)
	health := llm.NewModelHealth(llm.ModelHealthOptions{Now: func() time.Time { return now }})
	primaryOnly := []string{"primary:model"}
	backupOnly := []string{"backup:model"}
	primaryFailure, _ := health.Acquire(primaryOnly, nil)
	health.Complete(primaryFailure.Ticket, llm.ModelHealthEligibleFailure, "transient")
	now = now.Add(30 * time.Second)
	primaryProbe, _ := health.Acquire(primaryOnly, nil)
	health.Complete(primaryProbe.Ticket, llm.ModelHealthEligibleFailure, "transient")
	backupFailure, _ := health.Acquire(backupOnly, nil)
	health.Complete(backupFailure.Ticket, llm.ModelHealthEligibleFailure, "transient")
	now = now.Add(30 * time.Second)

	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{{err: errors.New("summary unavailable")}}}
	backup := &fallbackProvider{name: "backup:model"}
	eng, _ := newEngine(t, primary, false)
	eng.ContextWindow = 10_000
	eng.Compaction = DefaultCompactionPolicy()
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 10_000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 120},
	}
	eng.ModelHealth = health
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("large history ", 300))); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "continue"); err == nil || !strings.Contains(err.Error(), "primary:model") || !strings.Contains(err.Error(), "backup:model") {
		t.Fatalf("Turn err = %v, want exhausted compaction candidates", err)
	}
	retry, ok := health.Acquire(backupOnly, nil)
	if !ok || retry.Ticket.Ref != "backup:model" || !retry.Ticket.Probe {
		t.Fatalf("half-open candidate remained reserved after preflight failure: %+v, %v", retry, ok)
	}
}

type fallbackProvider struct {
	name      string
	results   []fallbackProviderResult
	calls     int
	histories [][]llm.Message
	opts      []llm.CompleteOptions
	emitDelta bool
}

func (p *fallbackProvider) Name() string { return p.name }

func (p *fallbackProvider) Complete(ctx context.Context, system string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, system, history, tools, llm.CompleteOptions{})
}

func (p *fallbackProvider) CompleteWithOptions(ctx context.Context, system string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	p.histories = append(p.histories, append([]llm.Message(nil), history...))
	p.opts = append(p.opts, opts)
	if p.emitDelta && opts.OnDelta != nil {
		opts.OnDelta(llm.StreamDelta{Kind: "text", Text: "partial"})
	}
	if p.calls >= len(p.results) {
		return llm.Response{}, errors.New("fallbackProvider: exhausted")
	}
	result := p.results[p.calls]
	p.calls++
	return result.response, result.err
}

func TestTurnFallsBackAndPersistsNoticeWithActualModel(t *testing.T) {
	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{{err: errors.New("status 503: unavailable")}}}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "served by backup"),
		StopReason: llm.StopEndTurn,
	}}}}
	eng, bus := newEngine(t, primary, false)
	eng.NotifyModelChanges = true
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000, MaxOutputTokens: 4096},
		{Ref: "backup:model", Provider: backup, ContextWindow: 64000, MaxOutputTokens: 2048},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	if err := eng.queuePolicyRuntimeContextFromHookResults([]hooks.Result{{Hook: hooks.CommandHook{Name: "fallback"}, Stdout: "one-shot fallback context"}}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, "earlier")); err != nil {
		t.Fatal(err)
	}
	previous := llm.TextMessage(llm.RoleAssistant, "primary response")
	previous.Model = "primary:model"
	if err := eng.Session.Append(previous); err != nil {
		t.Fatal(err)
	}
	var fallbackEvents []LLMFallbackPayload
	var erroredEvents []LLMErroredPayload
	var epochs []provenance.RequestEpoch
	bus.Subscribe("llm.fallback", func(event events.Event) {
		if payload, ok := event.Payload.(LLMFallbackPayload); ok {
			fallbackEvents = append(fallbackEvents, payload)
		}
	})
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) {
		if payload, ok := event.Payload.(provenance.RequestEpochPayload); ok {
			epochs = append(epochs, payload.Epoch)
		}
	})
	bus.Subscribe("llm.errored", func(event events.Event) {
		if payload, ok := event.Payload.(LLMErroredPayload); ok {
			erroredEvents = append(erroredEvents, payload)
		}
	})

	out, err := eng.Turn(context.Background(), "continue")
	if err != nil || out != "served by backup" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("calls primary=%d backup=%d", primary.calls, backup.calls)
	}
	if len(epochs) != 2 || epochs[0].Attempt != 1 || epochs[1].Attempt != 2 || epochs[0].EpochID == epochs[1].EpochID {
		t.Fatalf("fallback epochs = %+v", epochs)
	}
	if len(erroredEvents) != 1 || erroredEvents[0].EpochID != epochs[0].EpochID || erroredEvents[0].RequestDigest != epochs[0].RequestDigest || erroredEvents[0].Model != "primary:model" || !strings.Contains(erroredEvents[0].Error, "503") {
		t.Fatalf("errored events = %+v, failed epoch = %+v", erroredEvents, epochs[0])
	}
	if got := messagesText(primary.histories[0]); !strings.Contains(got, "one-shot fallback context") {
		t.Fatalf("primary request missing policy context:\n%s", got)
	}
	if got := messagesText(backup.histories[0]); strings.Contains(got, "one-shot fallback context") {
		t.Fatalf("backup request repeated checkpointed policy context:\n%s", got)
	}
	if len(backup.opts) != 1 || backup.opts[0].MaxOutputTokens != 2048 {
		t.Fatalf("backup options = %+v", backup.opts)
	}
	if len(backup.histories) != 1 || len(backup.histories[0]) == 0 {
		t.Fatalf("backup histories = %+v", backup.histories)
	}
	ephemeral := backup.histories[0][len(backup.histories[0])-1]
	if ephemeral.Kind != llm.MessageKindModelChange || !strings.Contains(ephemeral.FirstText(), "primary:model") || !strings.Contains(ephemeral.FirstText(), "backup:model") {
		t.Fatalf("ephemeral notice = %+v", ephemeral)
	}
	history := eng.Session.History
	if len(history) < 2 {
		t.Fatalf("history = %+v", history)
	}
	notice, assistant := history[len(history)-2], history[len(history)-1]
	if notice.Kind != llm.MessageKindModelChange || assistant.Model != "backup:model" {
		t.Fatalf("persisted tail = %+v / %+v", notice, assistant)
	}
	if len(fallbackEvents) != 1 || fallbackEvents[0].From != "primary:model" || fallbackEvents[0].To != "backup:model" || fallbackEvents[0].Reason != "transient" {
		t.Fatalf("fallback events = %+v", fallbackEvents)
	}
}

func TestTurnFallbackDoesNotNotifyModelChangesByDefault(t *testing.T) {
	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{{err: errors.New("status 503: unavailable")}}}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "served by backup"),
		StopReason: llm.StopEndTurn,
	}}}}
	eng, bus := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 64000},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	previous := llm.TextMessage(llm.RoleAssistant, "primary response")
	previous.Model = "primary:model"
	if err := eng.Session.Append(previous); err != nil {
		t.Fatal(err)
	}
	var fallbackEvents []LLMFallbackPayload
	bus.Subscribe("llm.fallback", func(event events.Event) {
		fallbackEvents = append(fallbackEvents, event.Payload.(LLMFallbackPayload))
	})

	if out, err := eng.Turn(context.Background(), "continue"); err != nil || out != "served by backup" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	for _, message := range backup.histories[0] {
		if message.Kind == llm.MessageKindModelChange {
			t.Fatalf("provider-visible fallback notice = %+v", message)
		}
	}
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindModelChange {
			t.Fatalf("persisted fallback notice = %+v", message)
		}
	}
	if len(fallbackEvents) != 1 || fallbackEvents[0].From != "primary:model" || fallbackEvents[0].To != "backup:model" {
		t.Fatalf("fallback events = %+v", fallbackEvents)
	}
	if eng.Session.History[len(eng.Session.History)-1].Model != "backup:model" {
		t.Fatalf("serving model attribution = %+v", eng.Session.History[len(eng.Session.History)-1])
	}
}

func TestTurnFallsBackAfterStreamedDelta(t *testing.T) {
	primary := &fallbackProvider{
		name:      "primary:model",
		emitDelta: true,
		results:   []fallbackProviderResult{{err: errors.New("status 503: unavailable")}},
	}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "recovered"),
		StopReason: llm.StopEndTurn,
	}}}}
	eng, bus := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	var modelEvents []string
	for _, eventType := range []string{"llm.requested", "llm.output_delta", "llm.errored", "llm.fallback", "llm.responded"} {
		bus.Subscribe(eventType, func(event events.Event) {
			modelEvents = append(modelEvents, event.Type)
		})
	}

	out, err := eng.Turn(context.Background(), "hello")
	if err != nil || out != "recovered" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("calls primary=%d backup=%d", primary.calls, backup.calls)
	}
	wantEvents := []string{"llm.requested", "llm.output_delta", "llm.errored", "llm.fallback", "llm.requested", "llm.responded"}
	if !slices.Equal(modelEvents, wantEvents) {
		t.Fatalf("model events = %v, want %v", modelEvents, wantEvents)
	}
	for _, message := range eng.Session.History {
		if strings.Contains(message.FirstText(), "partial") {
			t.Fatalf("abandoned provisional output persisted: %+v", eng.Session.History)
		}
	}
}

func TestTurnDoesNotFallbackWhenErrorOutcomeCannotCommit(t *testing.T) {
	primary := &fallbackProvider{name: "primary:model", results: []fallbackProviderResult{{err: errors.New("status 503: unavailable")}}}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{{response: llm.Response{
		Message: llm.TextMessage(llm.RoleAssistant, "must not run"), StopReason: llm.StopEndTurn,
	}}}}
	eng, bus := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	want := errors.New("outcome sync failed")
	bus.SetCommitter(selectiveSessionCommitter{session: eng.Session, eventType: "llm.errored", err: want})

	if _, err := eng.Turn(context.Background(), "hello"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if primary.calls != 1 || backup.calls != 0 {
		t.Fatalf("provider calls primary=%d backup=%d, want 1/0", primary.calls, backup.calls)
	}
}

func TestTurnFallbackAfterStreamedDeltaExecutesRecoveredToolOnce(t *testing.T) {
	primary := &fallbackProvider{
		name:      "primary:model",
		emitDelta: true,
		results:   []fallbackProviderResult{{err: errors.New("status 503: unavailable")}},
	}
	backup := &fallbackProvider{name: "backup:model", results: []fallbackProviderResult{
		{response: llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type: llm.BlockToolUse, ToolUseID: "recovered-call", ToolName: "once", Input: map[string]any{},
			}}},
			StopReason: llm.StopToolUse,
		}},
		{response: llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "finished"),
			StopReason: llm.StopEndTurn,
		}},
	}}
	eng, _ := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 128000},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	toolCalls := 0
	if err := eng.Tools.Register(tools.Tool{
		Name: "once",
		Handler: func(context.Context, map[string]any) (string, error) {
			toolCalls++
			return "tool result", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := eng.Turn(context.Background(), "use the recovered tool")
	if err != nil || out != "finished" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if primary.calls != 1 || backup.calls != 2 || toolCalls != 1 {
		t.Fatalf("calls primary=%d backup=%d tool=%d, want 1, 2, 1", primary.calls, backup.calls, toolCalls)
	}
	toolUses, toolResults := 0, 0
	for _, message := range eng.Session.History {
		if strings.Contains(message.FirstText(), "partial") {
			t.Fatalf("abandoned provisional output persisted: %+v", eng.Session.History)
		}
		for _, block := range message.Blocks {
			switch block.Type {
			case llm.BlockToolUse:
				toolUses++
			case llm.BlockToolResult:
				toolResults++
			}
		}
	}
	if toolUses != 1 || toolResults != 1 {
		t.Fatalf("persisted tool uses=%d results=%d, want one matched pair", toolUses, toolResults)
	}
}
