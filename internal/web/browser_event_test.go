package web

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/observable"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestBrowserEventTypesMatchGolden(t *testing.T) {
	assertGoldenJSON(t, "browser-event-types.golden.json", browserEventTypes())
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if visible {
		t.Fatalf("runtime-only event should be hidden from browser DTO: %+v", got)
	}
}

func TestBrowserEventFromRuntimeValidatesKnownPayload(t *testing.T) {
	_, visible, err := browserEventFromRuntime(events.Event{
		ID:      "bad-1",
		Type:    "turn.started",
		Payload: map[string]any{"input": 123},
	})
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
	})
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
			ID:        "evt-turn-started",
			Type:      "turn.started",
			Timestamp: ts,
			TurnID:    "turn-1",
			Payload:   juexruntime.TurnStartedPayload{Input: "run command", Kind: "user"},
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
				ContextUsage: &llm.ContextUsage{
					InputTokens:  10,
					OutputTokens: 5,
					TotalTokens:  15,
				},
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
			Payload:   juexruntime.HookTracePayload{Text: "hook extract-state allow UserPromptSubmit in 12ms"},
		},
		{
			ID:        "evt-pending-queued",
			Type:      "pending_input.queued",
			Timestamp: ts.Add(6 * time.Second),
			TurnID:    "turn-1",
			Payload: juexruntime.PendingInputQueuedPayload{
				Input:            "queued follow-up",
				Kind:             "user",
				PendingCount:     1,
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
				Description:        "ship the fix",
				VerificationMethod: "tests pass",
				ContinuationCount:  2,
				Status:             juexruntime.GoalStatusInProgress,
				UpdatedAt:          ts.Add(7200 * time.Millisecond),
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
	for _, event := range events {
		browserEvent, visible, err := browserEventFromRuntime(event)
		if err != nil {
			return nil, err
		}
		if visible {
			out = append(out, browserEvent)
		}
	}
	return out, nil
}
