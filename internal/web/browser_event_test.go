package web

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/eventmedia"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/observable"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/statusapi"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestBrowserEventTypesMatchGolden(t *testing.T) {
	assertGoldenJSON(t, "browser-event-types.golden.json", browserEventTypes())
}

func TestMessageKindsMatchGolden(t *testing.T) {
	assertGoldenJSON(t, "message-kinds.golden.json", []string{
		llm.MessageKindMCPEvent,
		llm.MessageKindObservation,
		llm.MessageKindHookEvent,
		llm.MessageKindCompact,
		llm.MessageKindRuntimeContext,
		llm.MessageKindModelFallback,
		llm.MessageKindSystemNotice,
	})
}

func TestBrowserEventFixturesMatchGolden(t *testing.T) {
	fixtures, err := browserEventFixtures()
	if err != nil {
		t.Fatal(err)
	}
	assertGoldenJSON(t, "browser-events.golden.json", fixtures)
}

func TestBrowserEventFromRuntimeSkipsRuntimeOnlyEvents(t *testing.T) {
	got, visible, err := browserEventFromRuntime(events.Event{
		ID:      "internal-1",
		Type:    "tool.failure.recorded",
		Payload: map[string]any{"name": "exec_command"},
	}, statusapi.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if visible {
		t.Fatalf("runtime-only event should be hidden from browser DTO: %+v", got)
	}
}

func TestBrowserEventFromRuntimeExposesPendingInputPromotion(t *testing.T) {
	got, visible, err := browserEventFromRuntime(events.Event{
		ID:     "promoted-1",
		Type:   juexruntime.PendingInputPromotedType,
		TurnID: "turn-2",
		Payload: juexruntime.PendingInputPromotedPayload{
			PendingCount:     0,
			MaxPendingInputs: 16,
		},
	}, statusapi.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !visible {
		t.Fatal("pending input promotion should be browser-visible")
	}
	if got.Type != juexruntime.PendingInputPromotedType || got.TurnID != "turn-2" {
		t.Fatalf("browser event = %+v", got)
	}
}

func TestBrowserEventFromRuntimeValidatesKnownPayload(t *testing.T) {
	_, visible, err := browserEventFromRuntime(events.Event{
		ID:      "bad-1",
		Type:    "turn.started",
		Payload: map[string]any{"input": 123},
	}, statusapi.Snapshot{})
	if !visible {
		t.Fatal("known event should be browser-visible")
	}
	if err == nil {
		t.Fatal("expected invalid payload type to fail")
	}
}

func TestBrowserPayloadJSONAcceptsTypedPayloadFastPath(t *testing.T) {
	raw, err := browserPayloadJSON("hook.trace", juexruntime.HookTracePayload{Text: "visible"}, func() any {
		return &juexruntime.HookTracePayload{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"text":"visible"}` {
		t.Fatalf("raw = %s", raw)
	}
}

func TestBrowserEventFromRuntimeNormalizesReplayPayload(t *testing.T) {
	got, visible, err := browserEventFromRuntime(events.Event{
		ID:   "turn-1",
		Type: "turn.started",
		Payload: map[string]any{
			"debug_only": "ignored",
			"input":      "hello",
			"kind":       "user",
		},
	}, statusapi.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !visible {
		t.Fatal("turn.started should be browser-visible")
	}
	var payload juexruntime.TurnStartedPayload
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Input != "hello" || payload.Kind != "user" {
		t.Fatalf("payload = %+v", payload)
	}
	if bytes.Contains(got.Payload, []byte("debug_only")) {
		t.Fatalf("replay payload was not normalized: %s", got.Payload)
	}
}

func TestBrowserEventProjectionCapturesEachResultingStatus(t *testing.T) {
	status := juexruntime.NewStatusStore(juexruntime.StatusSeed{
		SessionID:        "session-1",
		MaxPendingInputs: juexruntime.DefaultMaxPendingInput,
	})
	stream := newBroadcaster()
	t.Cleanup(stream.close)
	sub := stream.subscribe()
	t.Cleanup(sub.unsubscribe)

	sink := events.NewDurableSink(browserProjectionJournal{})
	t.Cleanup(sink.Close)
	sink.AddProjection(status)
	sink.AddProjection(browserEventProjection{status: status, stream: stream})

	for _, event := range []events.Event{
		{
			ID:      "evt-admitted",
			Type:    juexruntime.TurnAdmittedType,
			TurnID:  "turn-1",
			Payload: juexruntime.TurnAdmittedPayload{},
		},
		{
			ID:     "evt-phase",
			Type:   juexruntime.TurnPhaseType,
			TurnID: "turn-1",
			Payload: juexruntime.TurnPhasePayload{
				Phase: juexruntime.TurnPhaseToolBatch,
			},
		},
	} {
		if _, err := sink.Commit(event); err != nil {
			t.Fatal(err)
		}
	}

	first := receiveBrowserEvent(t, sub)
	second := receiveBrowserEvent(t, sub)
	if first.Status.Cursor != "evt-admitted" ||
		first.Status.Turn == nil ||
		first.Status.Turn.State != statusapi.TurnAdmitted {
		t.Fatalf("first projected status = %+v", first.Status)
	}
	if second.Status.Cursor != "evt-phase" ||
		second.Status.Turn == nil ||
		second.Status.Turn.Phase != statusapi.TurnPhaseToolBatch {
		t.Fatalf("second projected status = %+v", second.Status)
	}
}

func TestProjectBrowserEventsReplayMatchesUninterruptedProjection(t *testing.T) {
	seed := juexruntime.StatusSeed{
		SessionID:        "session-1",
		MaxPendingInputs: juexruntime.DefaultMaxPendingInput,
	}
	journal := browserEventFixtureEvents()
	all, err := projectBrowserEvents(seed, journal, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := projectBrowserEvents(seed, journal, "evt-tool-requested", nil)
	if err != nil {
		t.Fatal(err)
	}

	start := -1
	for index, event := range all {
		if event.ID == "evt-tool-requested" {
			start = index + 1
			break
		}
	}
	if start < 0 {
		t.Fatal("fixture cursor not found")
	}
	if !reflect.DeepEqual(replayed, all[start:]) {
		t.Fatalf("replayed browser projection diverged from uninterrupted suffix")
	}
}

func TestProjectBrowserEventsReplayEndsWithAuthoritativeRestartRecovery(t *testing.T) {
	seed := juexruntime.StatusSeed{
		SessionID:        "session-1",
		MaxPendingInputs: juexruntime.DefaultMaxPendingInput,
	}
	journal := []events.Event{
		{
			ID:      "evt-admitted",
			Type:    juexruntime.TurnAdmittedType,
			TurnID:  "turn-1",
			Payload: juexruntime.TurnAdmittedPayload{},
		},
		{
			ID:     "evt-started",
			Type:   "turn.started",
			TurnID: "turn-1",
			Payload: juexruntime.TurnStartedPayload{
				Input: "continue",
				Kind:  "user",
			},
		},
	}
	status := juexruntime.NewStatusStoreFromJournal(seed, journal)
	status.RecoverAfterRestart()
	authoritative := status.Snapshot()

	replayed, err := projectBrowserEvents(
		seed,
		journal,
		"evt-admitted",
		&authoritative,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed events = %d, want 1", len(replayed))
	}
	got := replayed[0].Status
	if got.Cursor != "evt-started" ||
		got.Turn == nil ||
		got.Turn.State != statusapi.TurnCancelled ||
		got.LastError == nil ||
		got.LastError.Kind != statusapi.StatusErrorRuntimeRestart {
		t.Fatalf("replayed recovered status = %+v", got)
	}
}

func TestProjectBrowserEventsDoesNotApplyMismatchedAuthoritativeStatus(t *testing.T) {
	seed := juexruntime.StatusSeed{
		SessionID:        "session-1",
		MaxPendingInputs: juexruntime.DefaultMaxPendingInput,
	}
	journal := []events.Event{{
		ID:      "evt-admitted",
		Type:    juexruntime.TurnAdmittedType,
		TurnID:  "turn-1",
		Payload: juexruntime.TurnAdmittedPayload{},
	}}
	authoritative := juexruntime.NewStatusStore(seed).Snapshot()
	authoritative.Cursor = "evt-newer"

	replayed, err := projectBrowserEvents(seed, journal, "", &authoritative)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed events = %d, want 1", len(replayed))
	}
	if replayed[0].Status.Cursor != "evt-admitted" ||
		replayed[0].Status.Turn == nil ||
		replayed[0].Status.Turn.State != statusapi.TurnAdmitted {
		t.Fatalf("mismatched authoritative status was applied: %+v", replayed[0].Status)
	}
}

func TestNormalizeGoldenJSONUsesUnixLineEndings(t *testing.T) {
	got := normalizeGoldenJSON([]byte("{\r\n  \"ok\": true\r\n}\r\n"))
	want := []byte("{\n  \"ok\": true\n}\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("normalized golden = %q", got)
	}
}

func assertGoldenJSON(t *testing.T, name string, value any) {
	t.Helper()
	got, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if os.Getenv("JUEX_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want = normalizeGoldenJSON(want)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s mismatch\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

func normalizeGoldenJSON(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}

func browserEventFixtureEvents() []events.Event {
	ts := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	return []events.Event{
		{
			ID:        "evt-turn-admitted",
			Type:      juexruntime.TurnAdmittedType,
			Timestamp: ts.Add(-time.Second),
			TurnID:    "turn-1",
			Payload:   juexruntime.TurnAdmittedPayload{},
		},
		{
			ID:        "evt-turn-started",
			Type:      "turn.started",
			Timestamp: ts,
			TurnID:    "turn-1",
			Payload: juexruntime.TurnStartedPayload{
				Input:     "run command",
				Kind:      "user",
				MessageID: "msg-user-1",
			},
		},
		{
			ID:        "evt-turn-provider-phase",
			Type:      juexruntime.TurnPhaseType,
			Timestamp: ts.Add(500 * time.Millisecond),
			TurnID:    "turn-1",
			Payload: juexruntime.TurnPhasePayload{
				Phase: juexruntime.TurnPhaseProviderIteration,
				Iter:  testIntPtr(0),
			},
		},
		{
			ID:        "evt-llm-responded",
			Type:      "llm.responded",
			Timestamp: ts.Add(time.Second),
			TurnID:    "turn-1",
			Payload: juexruntime.LLMRespondedPayload{
				StopReason: "tool_use",
				Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5},
				TokenUsage: llm.Usage{InputTokens: 10, OutputTokens: 5},
				Blocks:     []llm.Block{{Type: llm.BlockText, Text: "I'll run it."}},
				Text:       "I'll run it.",
				ToolCalls:  []toolevents.ToolCallPayload{},
				Model:      "gpt-test",
				MessageID:  "msg-assistant-1",
				ContextUsage: &llm.ContextUsage{
					InputTokens:  10,
					OutputTokens: 5,
					TotalTokens:  15,
				},
			},
		},
		{
			ID:        "evt-tool-running",
			Type:      toolevents.RunningType,
			Timestamp: ts.Add(2500 * time.Millisecond),
			TurnID:    "turn-1",
			Payload: toolevents.RunningPayload{
				Name:           "exec_command",
				ToolUseID:      "tool-1",
				TimeoutSeconds: 30,
			},
		},
		{
			ID:        "evt-llm-retry",
			Type:      "llm.retry",
			Timestamp: ts.Add(1500 * time.Millisecond),
			TurnID:    "turn-1",
			Payload: juexruntime.LLMRetryPayload{
				ProviderRetryDiagnostic: llm.ProviderRetryDiagnostic{
					Provider:    "openai-codex",
					Model:       "gpt-5.5",
					Protocol:    llm.ProtocolOpenAICodexResponses,
					Transport:   llm.CodexTransportSSE,
					Operation:   "responses.sse",
					Attempt:     1,
					MaxAttempts: 11,
					DelayMS:     100,
					RetryReason: "codex_sse_read",
					RawError:    "codex SSE read: stream error",
					WillRetry:   true,
				},
				Purpose: "turn",
				Iter:    testIntPtr(0),
			},
		},
		{
			ID:        "evt-llm-delta",
			Type:      "llm.output_delta",
			Timestamp: ts.Add(1750 * time.Millisecond),
			TurnID:    "turn-1",
			Payload: juexruntime.LLMOutputDeltaPayload{
				Iter:  0,
				Model: "gpt-test",
				Kind:  "text",
				Index: 0,
				Text:  "streaming ",
			},
		},
		{
			ID:        "evt-llm-fallback",
			Type:      "llm.fallback",
			Timestamp: ts.Add(1800 * time.Millisecond),
			TurnID:    "turn-1",
			Payload: juexruntime.LLMFallbackPayload{
				From:       "openai:gpt-primary",
				To:         "anthropic:claude-backup",
				Reason:     "transient",
				CooldownMS: 30000,
			},
		},
		{
			ID:        "evt-tool-requested",
			Type:      toolevents.RequestedType,
			Timestamp: ts.Add(2 * time.Second),
			TurnID:    "turn-1",
			Payload: toolevents.RequestedPayload{
				Name:           "exec_command",
				Input:          map[string]any{"cmd": "printf hi"},
				ToolUseID:      "tool-1",
				TimeoutSeconds: 30,
			},
		},
		{
			ID:        "evt-tool-delta",
			Type:      toolevents.OutputDeltaType,
			Timestamp: ts.Add(3 * time.Second),
			TurnID:    "turn-1",
			Payload: toolevents.OutputDeltaPayload{
				Name:      "exec_command",
				ToolUseID: "tool-1",
				SessionID: "shell-1",
				ChunkID:   1,
				Stream:    "stdout",
				Text:      "hi\n",
				Truncated: true,
			},
		},
		{
			ID:        "evt-tool-completed",
			Type:      toolevents.CompletedType,
			Timestamp: ts.Add(4 * time.Second),
			TurnID:    "turn-1",
			Payload: toolevents.CompletedPayload{
				Name:           "exec_command",
				ToolUseID:      "tool-1",
				TimeoutSeconds: 30,
				Len:            3,
				Preview:        "hi\n",
				Result:         map[string]any{"exit_code": 0},
			},
		},
		{
			ID:        "evt-hook-trace",
			Type:      "hook.trace",
			Timestamp: ts.Add(5 * time.Second),
			TurnID:    "turn-1",
			Payload: juexruntime.HookTracePayload{
				Text:      "hook extract-state allow UserPromptSubmit in 12ms",
				MessageID: "msg-hook-1",
			},
		},
		{
			ID:        "evt-pending-queued",
			Type:      "pending_input.queued",
			Timestamp: ts.Add(6 * time.Second),
			TurnID:    "turn-1",
			Payload: juexruntime.PendingInputQueuedPayload{
				Input:            "queued follow-up",
				Kind:             "user",
				MessageID:        "pending-message-1",
				PendingCount:     1,
				MaxPendingInputs: 4,
			},
		},
		{
			ID:        "evt-pending-draining",
			Type:      juexruntime.PendingInputDrainingType,
			Timestamp: ts.Add(6500 * time.Millisecond),
			TurnID:    "turn-1",
			Payload: juexruntime.PendingInputDrainingPayload{
				Count:            1,
				PendingCount:     0,
				MaxPendingInputs: 4,
			},
		},
		{
			ID:        "evt-pending-drained",
			Type:      "pending_input.drained",
			Timestamp: ts.Add(7 * time.Second),
			TurnID:    "turn-1",
			Payload: juexruntime.PendingInputDrainedPayload{
				Count:            1,
				PendingCount:     0,
				MaxPendingInputs: 4,
			},
		},
		{
			ID:        "evt-goal-updated",
			Type:      "goal.updated",
			Timestamp: ts.Add(7200 * time.Millisecond),
			TurnID:    "turn-1",
			Payload: juexruntime.GoalUpdatedPayload{
				Description:       "ship the fix",
				Acceptance:        "tests pass",
				ContinuationCount: 2,
				Status:            juexruntime.GoalStatusInProgress,
				UpdatedAt:         ts.Add(7200 * time.Millisecond),
			},
		},
		{
			ID:        "evt-notes-updated",
			Type:      "notes.updated",
			Timestamp: ts.Add(7300 * time.Millisecond),
			TurnID:    "turn-1",
			Payload: juexruntime.NotesUpdatedPayload{
				Content:   "- [x] inspect\n- [ ] verify",
				UpdatedAt: ts.Add(7300 * time.Millisecond),
			},
		},
		{
			ID:        "evt-notes-errored",
			Type:      "notes.errored",
			Timestamp: ts.Add(7400 * time.Millisecond),
			TurnID:    "turn-1",
			Payload: juexruntime.NotesErroredPayload{
				Error: "notes read: notes content must be valid UTF-8",
				Path:  "/workspace/.juex/sessions/session-1/notes.md",
			},
		},
		{
			ID:        "evt-observable-started",
			Type:      observable.EventObservableStarted,
			Timestamp: ts.Add(7500 * time.Millisecond),
			Payload: observable.ObservableEventPayload{
				ID:    "lark-events",
				State: observable.RunStateRunning,
				RunID: "run-1",
				PID:   1234,
			},
		},
		{
			ID:        "evt-observation-delivered",
			Type:      observable.EventObservationDelivered,
			Timestamp: ts.Add(8 * time.Second),
			Payload: observable.ObservationEventPayload{
				Observation: observable.ObservationRecord{
					ID:            "obs-1",
					ObservableID:  "lark-events",
					RunID:         "run-1",
					Kind:          "lark_notification",
					Severity:      "info",
					WindowStart:   ts,
					WindowEnd:     ts.Add(time.Second),
					Content:       "hello",
					OriginalChars: 5,
					State:         observable.ObservationStateDelivered,
					CreatedAt:     ts.Add(8 * time.Second),
					DeliveredAt:   ts.Add(8 * time.Second),
				},
			},
		},
		{
			ID:        "evt-observation-errored",
			Type:      observable.EventObservationErrored,
			Timestamp: ts.Add(8500 * time.Millisecond),
			Payload: observable.ObservationEventPayload{
				Observation: observable.ObservationRecord{
					ID:               "obs-attachment-error",
					ObservableID:     "lark-events",
					Kind:             "lark_notification",
					Severity:         "warning",
					WindowStart:      ts,
					WindowEnd:        ts.Add(time.Second),
					Content:          "attachment event",
					Attachments:      []eventmedia.AttachmentRef{{Path: ".juex/inbox/missing.png", MediaType: "image/png"}},
					AttachmentState:  observable.ObservationAttachmentStateError,
					AttachmentErrors: []string{"missing.png: attachment path does not exist"},
					OriginalChars:    16,
					State:            observable.ObservationStateDelivered,
					CreatedAt:        ts.Add(8 * time.Second),
					DeliveredAt:      ts.Add(8 * time.Second),
					Error:            "missing.png: attachment path does not exist",
				},
				Error: "missing.png: attachment path does not exist",
			},
		},
		{
			ID:        "evt-compact-started",
			Type:      "context.compact.started",
			Timestamp: ts.Add(9 * time.Second),
			TurnID:    "turn-1",
			Payload: juexruntime.ContextCompactStartedPayload{
				Reason:           "manual",
				Auto:             false,
				EstimatedTokens:  100,
				TokensBefore:     100,
				ContextWindow:    1000,
				ReserveTokens:    100,
				KeepRecentTokens: 100,
				TailTurns:        2,
			},
		},
		{
			ID:        "evt-compact-summary-retry",
			Type:      "context.compact.summary_retry",
			Timestamp: ts.Add(9500 * time.Millisecond),
			TurnID:    "turn-1",
			Payload: juexruntime.ContextCompactSummaryRetryPayload{
				Attempt:                 2,
				Reason:                  "empty_summary",
				StopReason:              llm.StopMaxTokens,
				ReasoningOnly:           true,
				PreviousMaxOutputTokens: 2048,
				MaxOutputTokens:         4096,
			},
		},
		{
			ID:        "evt-compact-completed",
			Type:      "context.compact.completed",
			Timestamp: ts.Add(10 * time.Second),
			TurnID:    "turn-1",
			Payload: juexruntime.ContextCompactCompletedPayload{
				MessageID:          "compact-1",
				Reason:             "manual",
				Auto:               false,
				EstimatedTokens:    100,
				TokensBefore:       100,
				TokensAfter:        40,
				SummaryChars:       20,
				SummaryModel:       "gpt-test",
				TailStartMessageID: "m-tail",
				ContextWindow:      1000,
				ReserveTokens:      100,
				KeepRecentTokens:   100,
				ContextUsage: &llm.ContextUsage{
					Model:         "gpt-test",
					ContextWindow: 1000,
					InputTokens:   40,
					TotalTokens:   40,
				},
			},
		},
		{
			ID:        "evt-projection-applied",
			Type:      "context.projection.applied",
			Timestamp: ts.Add(11 * time.Second),
			TurnID:    "turn-1",
			Payload: BrowserContextProjectionAppliedPayload{
				UserInputsExternalized:        1,
				ToolResultsExternalized:       2,
				BytesExternalized:             3000,
				ReasoningContentsStripped:     1,
				ReasoningContentBytesStripped: 200,
			},
		},
	}
}

func testIntPtr(v int) *int {
	return &v
}

func browserEventFixtures() ([]BrowserEvent, error) {
	events := browserEventFixtureEvents()
	out := make([]BrowserEvent, 0, len(events))
	status := juexruntime.NewStatusStore(juexruntime.StatusSeed{
		SessionID:        "session-1",
		MaxPendingInputs: juexruntime.DefaultMaxPendingInput,
	})
	for _, event := range events {
		status.Publish(event)
		browserEvent, visible, err := browserEventFromRuntime(
			event,
			statusapi.FromRuntime(status.Snapshot()),
		)
		if err != nil {
			return nil, err
		}
		if visible {
			out = append(out, browserEvent)
		}
	}
	return out, nil
}

type browserProjectionJournal struct{}

func (browserProjectionJournal) AppendEvent(events.Event) error {
	return nil
}

func receiveBrowserEvent(t *testing.T, sub *subscriber) BrowserEvent {
	t.Helper()
	select {
	case event := <-sub.ch:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for browser event")
		return BrowserEvent{}
	}
}
