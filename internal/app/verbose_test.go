package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
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
			"text":     "I'll find them.",
			"thinking": "Need to use grep or find.",
		}},
		{Type: "tool.requested", Payload: map[string]any{"name": "bash", "input": map[string]any{"cmd": "find . -name '*.go'"}}},
		{Type: "tool.completed", Payload: map[string]any{"name": "bash", "len": 1234}},
		{Type: "llm.requested", Payload: map[string]any{"iter": 1}},
		{Type: "llm.responded", Payload: map[string]any{"text": "Found 14 files."}},
		{Type: "turn.completed", Payload: map[string]any{}},
	})

	for _, want := range []string{
		"› user: list .go files",
		"[turn 1]",
		"thinking: Need to use grep or find.",
		"assistant: I'll find them.",
		"→ bash(",
		`"cmd":"find . -name '*.go'"`, // oneLineJSON of input
		"← bash: ok (1234 bytes)",
		"[turn 2]",
		"assistant: Found 14 files.",
		"✓ done in",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in transcript:\n%s", want, out)
		}
	}
}

func TestVerbose_ToolError(t *testing.T) {
	out := emitAll([]events.Event{
		{Type: "tool.requested", Payload: map[string]any{"name": "read", "input": map[string]any{"path": "/no/such"}}},
		{Type: "tool.errored", Payload: map[string]any{"name": "read", "error": "open /no/such: no such file or directory"}},
	})
	if !strings.Contains(out, "← read: ERROR open /no/such") {
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

func TestVerbose_OneLineJSONTruncates(t *testing.T) {
	long := strings.Repeat("a", 500)
	got := oneLineJSON(map[string]any{"text": long})
	if len(got) > 210 {
		t.Fatalf("oneLineJSON should truncate, len=%d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncation marker, got %q", got)
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
