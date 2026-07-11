package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestWriteSSEFrame_FormatsExpectedFields(t *testing.T) {
	var buf bytes.Buffer
	err := writeSSEFrame(&buf, events.Event{
		ID:     "evt-1",
		Type:   "turn.started",
		TurnID: "t-7",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"id: evt-1\n",
		"data: ",
		`"type":"turn.started"`,
		`"turn_id":"t-7"`,
		"\n\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestWriteSSEFrame_DataIsOneLine(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSSEFrame(&buf, events.Event{ID: "x1", Type: "hook.trace", Payload: map[string]any{"text": "line1\nline2"}}); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	dataLines := 0
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if strings.HasPrefix(line, "data:") {
			dataLines++
		}
	}
	if dataLines != 1 {
		t.Fatalf("expected exactly one data line, got %d in:\n%s", dataLines, body)
	}
}

func TestWriteSSEFrame_TransientEventOmitsReplayCursor(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSSEFrame(&buf, events.Event{
		ID:        "transient-1",
		Type:      "llm.output_delta",
		Transient: true,
		Payload:   map[string]any{"kind": "text", "text": "hello"},
	}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "id:") || !strings.Contains(got, `"type":"llm.output_delta"`) {
		t.Fatalf("transient SSE frame must omit replay cursor:\n%s", got)
	}
}

func TestWriteSSEFrame_SkipsRuntimeOnlyEvents(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSSEFrame(&buf, events.Event{ID: "internal-1", Type: "tool.failure.recorded", Payload: map[string]any{"name": "exec_command"}}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "" {
		t.Fatalf("runtime-only event wrote frame:\n%s", got)
	}
}

func TestWriteSSEFrame_MarshalsTypedPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSSEFrame(&buf, events.Event{
		ID:   "tool-1",
		Type: toolevents.CompletedType,
		Payload: toolevents.CompletedPayload{
			Name:           "shell",
			ToolUseID:      "tu1",
			TimeoutSeconds: 2,
			Len:            12,
			Preview:        "hello",
		},
	}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		`"payload":{"name":"shell"`,
		`"tool_use_id":"tu1"`,
		`"timeout_seconds":2`,
		`"len":12`,
		`"preview":"hello"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
