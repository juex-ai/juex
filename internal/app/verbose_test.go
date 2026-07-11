package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	runtimeevents "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/toolevents"
)

// emitAll feeds a sequence of events through a verbosePrinter and returns
// the captured stderr text (with ANSI control codes stripped so assertions
// can match the visible content).
func emitAll(events []events.Event) string {
	var buf bytes.Buffer
	vp := newVerbosePrinter(&buf)
	for _, e := range events {
		vp.handle(e)
	}
	return stripANSI(buf.String())
}

func stripANSI(s string) string {
	// Tiny ANSI stripper: drop ESC [ <bytes> <letter>.
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j < len(s) {
				j++ // consume the final letter
			}
			i = j
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func TestVerbose_TurnLifecycle(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "turn.started", Payload: map[string]any{"input": "list .go files"}},
		{Type: "llm.requested", Payload: map[string]any{"iter": 0}},
		{Type: "llm.responded", Payload: map[string]any{
			"text":       "I'll find them.",
			"thinking":   "Need to use grep or find.",
			"tool_calls": []toolevents.ToolCallPayload{{ToolUseID: "call_shell", Name: "shell", Input: map[string]any{"cmd": "find . -name '*.go'"}}},
		}},
		{Type: toolevents.RequestedType, Payload: map[string]any{"name": "shell", "tool_use_id": "call_shell", "input": map[string]any{"cmd": "find . -name '*.go'"}}},
		{Type: toolevents.CompletedType, Payload: map[string]any{"name": "shell", "tool_use_id": "call_shell", "len": 1234}},
		{Type: "llm.requested", Payload: map[string]any{"iter": 1}},
		{Type: "llm.responded", Payload: map[string]any{"text": "Found 14 files."}},
		{Type: "turn.completed", Payload: map[string]any{}},
	})

	for _, want := range []string{
		"› user: list .go files",
		"[turn 1]",
		"thinking: Need to use grep or find.",
		"assistant: I'll find them.",
		"… 1 shell",
		"● 1 shell",
		"[turn 2]",
		"assistant: Found 14 files.",
		"✓ done in",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in transcript:\n%s", want, out)
		}
	}
}

func TestVerbose_TypedPayloads(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "turn.started", Payload: runtimeevents.TurnStartedPayload{Input: "inspect typed events"}},
		{Type: "llm.requested", Payload: runtimeevents.LLMRequestedPayload{Iter: 0, HistoryLen: 1, ToolCount: 1}},
		{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
			Blocks: []llm.Block{
				{Type: llm.BlockReasoning, Text: "typed thought"},
				{Type: llm.BlockText, Text: "typed answer"},
			},
			ToolCalls:  []toolevents.ToolCallPayload{{ToolUseID: "call_read", Name: "read", Input: map[string]any{"path": "README.md"}}},
			TokenUsage: llm.Usage{InputTokens: 9, OutputTokens: 3},
		}},
		{Type: toolevents.RequestedType, Payload: toolevents.RequestedPayload{Name: "read", ToolUseID: "call_read", Input: map[string]any{"path": "README.md"}}},
		{Type: toolevents.CompletedType, Payload: toolevents.CompletedPayload{Name: "read", ToolUseID: "call_read", Len: 42}},
		{Type: "pending_input.queued", Payload: runtimeevents.PendingInputQueuedPayload{PendingCount: 1, MaxPendingInputs: 4}},
		{Type: "pending_input.drained", Payload: runtimeevents.PendingInputDrainedPayload{Count: 1}},
		{Type: "turn.errored", Payload: runtimeevents.TurnErroredPayload{Error: "typed failure"}},
	})

	for _, want := range []string{
		"› user: inspect typed events",
		"thinking: typed thought",
		"assistant: typed answer",
		"tokens: 12 total (input 9, output 3)",
		"… 1 read",
		"● 1 read",
		"+ pending input (1/4)",
		"+ drained 1 pending input(s)",
		"✗ typed failure",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in transcript:\n%s", want, out)
		}
	}
}

func TestVerbose_TurnStartedObservationUsesEventLabel(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "turn.started", Payload: runtimeevents.TurnStartedPayload{
			Input: `{"kind":"observation","content":"deploy finished: build 42"}`,
			Kind:  llm.MessageKindObservation,
		}},
	})

	if !strings.Contains(out, "› event: deploy finished: build 42") {
		t.Fatalf("missing event label in transcript:\n%s", out)
	}
	if strings.Contains(out, "› user:") {
		t.Fatalf("observation should not be printed as user input:\n%s", out)
	}
}

func TestVerbose_TurnStartedMCPEventUsesEventLabel(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "turn.started", Payload: runtimeevents.TurnStartedPayload{
			Input: `alpha:message:{"content":"hello from mcp"}`,
			Kind:  llm.MessageKindMCPEvent,
		}},
	})

	if !strings.Contains(out, "› event: hello from mcp") {
		t.Fatalf("missing MCP event label in transcript:\n%s", out)
	}
	if strings.Contains(out, "› user:") {
		t.Fatalf("MCP event should not be printed as user input:\n%s", out)
	}
}

func TestVerbose_TurnStartedEventUsesGoldTTYColor(t *testing.T) {
	var buf bytes.Buffer
	vp := newVerbosePrinter(&buf)
	vp.isTTY = true

	vp.handle(events.Event{Type: "turn.started", Payload: runtimeevents.TurnStartedPayload{
		Input: `{"content":"release done"}`,
		Kind:  llm.MessageKindObservation,
	}})

	out := buf.String()
	if !strings.Contains(out, "\x1b[33m› event: release done\x1b[0m") {
		t.Fatalf("event line should use gold TTY color, got %q", out)
	}
}

func TestVerbose_ToolError(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: toolevents.RequestedType, Payload: map[string]any{"name": "read", "tool_use_id": "call_read", "input": map[string]any{"path": "/no/such"}}},
		{Type: toolevents.ErroredType, Payload: map[string]any{"name": "read", "tool_use_id": "call_read", "error": "open /no/such: no such file or directory"}},
	})
	if !strings.Contains(out, "● failed 1 read") {
		t.Fatalf("missing error line in:\n%s", out)
	}
}

func TestVerbose_TurnError(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "turn.errored", Payload: map[string]any{"error": "llm: rate limited"}},
	})
	if !strings.Contains(out, "✗ llm: rate limited") {
		t.Fatalf("missing turn error line in:\n%s", out)
	}
}

func TestVerbose_TurnErrorHaltsSpinner(t *testing.T) {
	var buf bytes.Buffer
	vp := newVerbosePrinter(&buf)
	vp.isTTY = true
	vp.spin.isTTY = true

	vp.handle(events.Event{Type: "llm.requested", Payload: map[string]any{"iter": 0}})
	if vp.spin.stop == nil {
		t.Fatal("spinner was not started")
	}
	vp.handle(events.Event{Type: "turn.errored", Payload: map[string]any{"error": "llm failed"}})
	if vp.spin.stop != nil {
		t.Fatal("spinner remained active after turn.errored")
	}
}

func TestVerbose_MultilineThinkingAndText(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "llm.requested", Payload: map[string]any{}},
		{Type: "llm.responded", Payload: map[string]any{
			"thinking": "First idea.\nSecond idea.",
			"text":     "Final answer line one.\nFinal answer line two.",
		}},
	})
	for _, want := range []string{
		"thinking: First idea.",
		"          Second idea.", // continuation lines aligned
		"assistant: Final answer line one.",
		"           Final answer line two.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestVerbose_PrintsResponseBlocksInOrder(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "llm.requested", Payload: map[string]any{}},
		{Type: "llm.responded", Payload: map[string]any{
			"blocks": []llm.Block{
				{Type: llm.BlockText, Text: "lead"},
				{Type: llm.BlockReasoning, Text: "think"},
				{Type: llm.BlockText, Text: "tail"},
			},
		}},
	})

	lead := strings.Index(out, "assistant: lead")
	think := strings.Index(out, "thinking: think")
	tail := strings.Index(out, "assistant: tail")
	if lead < 0 || think < 0 || tail < 0 {
		t.Fatalf("missing ordered block output in:\n%s", out)
	}
	if lead >= think || think >= tail {
		t.Fatalf("blocks printed out of order:\n%s", out)
	}
}

func TestVerbose_PrintsImageBlocks(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "llm.requested", Payload: map[string]any{}},
		{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
			Blocks: []llm.Block{
				{
					Type: llm.BlockImage,
					Media: &llm.MediaRef{
						ArtifactPath:  ".juex/artifacts/media/s/chart.png",
						MediaType:     "image/png",
						OriginalBytes: 2048,
						Width:         640,
						Height:        480,
					},
				},
			},
		}},
	})

	if !strings.Contains(out, "assistant: [图片: chart.png (640x480, 2.0 KB)]") {
		t.Fatalf("missing image placeholder in:\n%s", out)
	}
}

func TestVerbose_PrintsJSONDecodedResponseBlocksInOrder(t *testing.T) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(`{
		"blocks": [
			{"type": "text", "text": "lead"},
			{"type": "reasoning", "text": "think"},
			{"type": "text", "text": "tail"}
		],
		"text": "legacy fallback",
		"thinking": "legacy thinking"
	}`), &payload); err != nil {
		t.Fatal(err)
	}
	out := emitAll([]events.Event{
		{Type: "llm.requested", Payload: map[string]any{}},
		{Type: "llm.responded", Payload: payload},
	})

	lead := strings.Index(out, "assistant: lead")
	think := strings.Index(out, "thinking: think")
	tail := strings.Index(out, "assistant: tail")
	if lead < 0 || think < 0 || tail < 0 {
		t.Fatalf("missing ordered block output in:\n%s", out)
	}
	if lead >= think || think >= tail {
		t.Fatalf("JSON-decoded blocks printed out of order:\n%s", out)
	}
	if strings.Contains(out, "legacy fallback") || strings.Contains(out, "legacy thinking") {
		t.Fatalf("used legacy fallback despite decoded blocks:\n%s", out)
	}
}

func TestVerbose_EmptyOptionalFieldsSkipped(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "llm.requested", Payload: map[string]any{}},
		{Type: "llm.responded", Payload: map[string]any{"text": "just text", "thinking": ""}},
	})
	if strings.Contains(out, "thinking:") {
		t.Errorf("empty thinking should not be printed:\n%s", out)
	}
	if !strings.Contains(out, "assistant: just text") {
		t.Errorf("missing text line in:\n%s", out)
	}
}

func TestVerbose_TruncOneLine(t *testing.T) {
	got := truncOneLine("line one\nline two\n", 30)
	if got != "line one line two" {
		t.Fatalf("want collapsed newlines, got %q", got)
	}
	got = truncOneLine(strings.Repeat("a", 100), 10)
	if got != strings.Repeat("a", 10)+"..." {
		t.Fatalf("want truncated, got %q", got)
	}
}

func TestSpinner_NonTTYIsNoop(t *testing.T) {
	// A non-TTY writer (bytes.Buffer) should never receive spinner frames.
	var buf bytes.Buffer
	s := newSpinner(&buf, false)
	s.start("loading")
	s.halt()
	if buf.Len() != 0 {
		t.Fatalf("non-TTY spinner wrote %q", buf.String())
	}
}

func TestVerbose_ToolBatchAggregatesSuccessfulTools(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
			ToolCalls: []toolevents.ToolCallPayload{
				{ToolUseID: "call_1", Name: "memory_write"},
				{ToolUseID: "call_2", Name: "memory_write"},
				{ToolUseID: "call_3", Name: "update_goal"},
			},
		}},
		{Type: toolevents.CompletedType, Payload: toolevents.CompletedPayload{Name: "memory_write", ToolUseID: "call_2"}},
		{Type: toolevents.CompletedType, Payload: toolevents.CompletedPayload{Name: "update_goal", ToolUseID: "call_3"}},
		{Type: toolevents.CompletedType, Payload: toolevents.CompletedPayload{Name: "memory_write", ToolUseID: "call_1"}},
	})
	for _, want := range []string{"… 2 memory_write, 1 update_goal", "● 2 memory_write, 1 update_goal"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "call_1") || strings.Contains(out, "call_2") || strings.Contains(out, "call_3") {
		t.Fatalf("compact output leaked individual tool ids:\n%s", out)
	}
}

func TestVerbose_ToolBatchStatusPriority(t *testing.T) {
	tests := []struct {
		name string
		ops  func(*verboseToolBatch)
		want verboseToolState
	}{
		{
			name: "one running child keeps batch running",
			ops: func(batch *verboseToolBatch) {
				batch.upsert("call_1", "read", verboseToolDone)
			},
			want: verboseToolRunning,
		},
		{
			name: "failed child with no running child makes batch failed",
			ops: func(batch *verboseToolBatch) {
				batch.upsert("call_1", "read", verboseToolFailed)
				batch.upsert("call_2", "grep", verboseToolDone)
			},
			want: verboseToolFailed,
		},
		{
			name: "running beats failed",
			ops: func(batch *verboseToolBatch) {
				batch.upsert("call_1", "read", verboseToolFailed)
			},
			want: verboseToolRunning,
		},
		{
			name: "all done",
			ops: func(batch *verboseToolBatch) {
				batch.upsert("call_1", "read", verboseToolDone)
				batch.upsert("call_2", "grep", verboseToolDone)
			},
			want: verboseToolDone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch := newVerboseToolBatch([]toolevents.ToolCallPayload{
				{ToolUseID: "call_1", Name: "read"},
				{ToolUseID: "call_2", Name: "grep"},
			})
			tt.ops(batch)
			if got := batch.status(); got != tt.want {
				t.Fatalf("status = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestVerbose_ToolBatchMatchesOutOfOrderByID(t *testing.T) {
	batch := newVerboseToolBatch([]toolevents.ToolCallPayload{
		{ToolUseID: "call_read", Name: "read"},
		{ToolUseID: "call_grep", Name: "grep"},
	})
	batch.upsert("call_grep", "grep", verboseToolDone)
	if got := batch.status(); got != verboseToolRunning {
		t.Fatalf("status after out-of-order grep completion = %s, want running", got)
	}
	batch.upsert("call_read", "read", verboseToolDone)
	if got := batch.status(); got != verboseToolDone {
		t.Fatalf("status after read completion = %s, want done", got)
	}
	if got, want := batch.summary(), "1 read, 1 grep"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestVerbose_NamelessToolDoesNotReviveCompletedTool(t *testing.T) {
	batch := newVerboseToolBatch(nil)
	batch.upsert("", "read", verboseToolRunning)
	firstID := batch.order[0]
	batch.upsert("", "read", verboseToolDone)
	if got := batch.byID[firstID].Status; got != verboseToolDone {
		t.Fatalf("first tool status = %s, want done", got)
	}

	batch.upsert("", "read", verboseToolRunning)
	if got := batch.byID[firstID].Status; got != verboseToolDone {
		t.Fatalf("new nameless running event revived completed tool: %s", got)
	}
	if got, want := len(batch.order), 2; got != want {
		t.Fatalf("tool count = %d, want %d", got, want)
	}
	if got := batch.status(); got != verboseToolRunning {
		t.Fatalf("batch status = %s, want running for new nameless tool", got)
	}
}

func TestVerbose_ToolOutputDeltaDoesNotCompleteTool(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
			ToolCalls: []toolevents.ToolCallPayload{{ToolUseID: "call_exec", Name: "exec_command"}},
		}},
		{Type: toolevents.OutputDeltaType, Payload: toolevents.OutputDeltaPayload{Name: "exec_command", ToolUseID: "call_exec", Text: "partial"}},
	})
	if !strings.Contains(out, "… 1 exec_command") {
		t.Fatalf("missing running line in:\n%s", out)
	}
	if strings.Contains(out, "● 1 exec_command") {
		t.Fatalf("output delta should not complete tool:\n%s", out)
	}
}

func TestVerbose_PostCompletionToolOutputDeltaIsIgnored(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
			ToolCalls: []toolevents.ToolCallPayload{{ToolUseID: "call_exec", Name: "exec_command"}},
		}},
		{Type: toolevents.CompletedType, Payload: toolevents.CompletedPayload{Name: "exec_command", ToolUseID: "call_exec"}},
		{Type: toolevents.OutputDeltaType, Payload: toolevents.OutputDeltaPayload{Name: "exec_command", ToolUseID: "call_exec", Text: "late chunk"}},
	})
	if got := strings.Count(out, "… 1 exec_command"); got != 1 {
		t.Fatalf("running line count = %d, want 1 after post-completion delta:\n%s", got, out)
	}
	if got := strings.Count(out, "● 1 exec_command"); got != 1 {
		t.Fatalf("done line count = %d, want 1 after post-completion delta:\n%s", got, out)
	}

	var buf bytes.Buffer
	vp := newVerbosePrinter(&buf)
	vp.isTTY = true
	vp.spin.isTTY = true
	vp.handle(events.Event{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
		ToolCalls: []toolevents.ToolCallPayload{{ToolUseID: "call_exec", Name: "exec_command"}},
	}})
	vp.handle(events.Event{Type: toolevents.CompletedType, Payload: toolevents.CompletedPayload{Name: "exec_command", ToolUseID: "call_exec"}})
	if vp.spin.stop != nil {
		t.Fatal("spinner should stop after completion")
	}
	vp.handle(events.Event{Type: toolevents.OutputDeltaType, Payload: toolevents.OutputDeltaPayload{Name: "exec_command", ToolUseID: "call_exec", Text: "late chunk"}})
	if vp.spin.stop != nil {
		t.Fatal("post-completion delta restarted spinner")
	}
}

func TestVerbose_ToolFinalEventsMoveSpecificTool(t *testing.T) {
	done := emitAll([]events.Event{
		{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
			ToolCalls: []toolevents.ToolCallPayload{{ToolUseID: "call_exec", Name: "exec_command"}},
		}},
		{Type: toolevents.OutputDeltaType, Payload: toolevents.OutputDeltaPayload{Name: "exec_command", ToolUseID: "call_exec", Text: "partial"}},
		{Type: toolevents.CompletedType, Payload: toolevents.CompletedPayload{Name: "exec_command", ToolUseID: "call_exec"}},
	})
	if !strings.Contains(done, "● 1 exec_command") {
		t.Fatalf("completion did not render done:\n%s", done)
	}

	failed := emitAll([]events.Event{
		{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
			ToolCalls: []toolevents.ToolCallPayload{{ToolUseID: "call_exec", Name: "exec_command"}},
		}},
		{Type: toolevents.OutputDeltaType, Payload: toolevents.OutputDeltaPayload{Name: "exec_command", ToolUseID: "call_exec", Text: "partial"}},
		{Type: toolevents.ErroredType, Payload: toolevents.ErroredPayload{Name: "exec_command", ToolUseID: "call_exec", Error: "boom"}},
	})
	if !strings.Contains(failed, "● failed 1 exec_command") {
		t.Fatalf("error did not render failed:\n%s", failed)
	}
}

func TestVerbose_ToolBatchTTYUsesSpinnerAndColoredFinalDots(t *testing.T) {
	var buf bytes.Buffer
	vp := newVerbosePrinter(&buf)
	vp.isTTY = true
	vp.spin.isTTY = true
	vp.handle(events.Event{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
		ToolCalls: []toolevents.ToolCallPayload{{ToolUseID: "call_read", Name: "read"}},
	}})
	if vp.spin.stop == nil {
		t.Fatal("TTY running tool batch did not start spinner")
	}
	vp.handle(events.Event{Type: toolevents.CompletedType, Payload: toolevents.CompletedPayload{Name: "read", ToolUseID: "call_read"}})
	if vp.spin.stop != nil {
		t.Fatal("TTY completed tool batch left spinner running")
	}
	if !strings.Contains(buf.String(), "\x1b[32m  ● 1 read\x1b[0m") {
		t.Fatalf("done line missing green ANSI dot:\n%q", buf.String())
	}

	buf.Reset()
	vp = newVerbosePrinter(&buf)
	vp.isTTY = true
	vp.spin.isTTY = true
	vp.handle(events.Event{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
		ToolCalls: []toolevents.ToolCallPayload{{ToolUseID: "call_read", Name: "read"}},
	}})
	vp.handle(events.Event{Type: toolevents.ErroredType, Payload: toolevents.ErroredPayload{Name: "read", ToolUseID: "call_read", Error: "boom"}})
	if !strings.Contains(buf.String(), "\x1b[31m  ● 1 read\x1b[0m") {
		t.Fatalf("failed line missing red ANSI dot:\n%q", buf.String())
	}
}

func TestVerbose_ToolBatchNonTTYHasNoANSIOrSpinnerControl(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
			ToolCalls: []toolevents.ToolCallPayload{{ToolUseID: "call_read", Name: "read"}},
		}},
		{Type: toolevents.CompletedType, Payload: toolevents.CompletedPayload{Name: "read", ToolUseID: "call_read"}},
	})
	if strings.Contains(out, "\x1b[") || strings.Contains(out, "\r") {
		t.Fatalf("non-TTY output contains terminal control artifacts:\n%q", out)
	}
}

func TestVerbose_ToolBatchNonTTYFailureIsExplicit(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "llm.responded", Payload: runtimeevents.LLMRespondedPayload{
			ToolCalls: []toolevents.ToolCallPayload{{ToolUseID: "call_read", Name: "read"}},
		}},
		{Type: toolevents.ErroredType, Payload: toolevents.ErroredPayload{Name: "read", ToolUseID: "call_read", Error: "boom"}},
	})
	if !strings.Contains(out, "● failed 1 read") {
		t.Fatalf("non-TTY failure should be explicit:\n%s", out)
	}
}
