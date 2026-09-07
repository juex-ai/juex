package shell

import (
	"strings"
	"testing"
	"time"

	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func TestShellSessionCapsLiveDeltasButKeepsDrainingIntoTerminalResult(t *testing.T) {
	var deltas []toolcore.OutputDelta
	session := &shellSession{
		id:            1,
		started:       time.Now(),
		maxTranscript: 64,
		events: toolcore.ToolCallEvents{
			Name:      "exec_command",
			ToolUseID: "tool-1",
			Emit: func(delta toolcore.OutputDelta) {
				deltas = append(deltas, delta)
			},
		},
	}
	for range maxShellDeltaCount + 5 {
		session.appendOutput([]byte("x"))
	}
	session.appendOutput([]byte("TAIL-SENTINEL"))

	if len(deltas) != maxShellDeltaCount {
		t.Fatalf("delta count = %d, want %d", len(deltas), maxShellDeltaCount)
	}
	for index, delta := range deltas {
		if len(delta.Text) > maxShellDeltaBytes {
			t.Fatalf("delta %d bytes = %d, want <= %d", index, len(delta.Text), maxShellDeltaBytes)
		}
	}
	result := session.snapshot(true, defaultShellMaxOutputTokens)
	if !strings.HasSuffix(result.Output, "TAIL-SENTINEL") {
		t.Fatalf("terminal output lost bytes after delta cap: %q", result.Output)
	}
	if result.OriginalBytes != maxShellDeltaCount+5+len("TAIL-SENTINEL") {
		t.Fatalf("original bytes = %d", result.OriginalBytes)
	}
}

func TestShellSessionSplitsLiveDeltaAtByteLimit(t *testing.T) {
	var deltas []toolcore.OutputDelta
	session := &shellSession{
		id:            1,
		started:       time.Now(),
		maxTranscript: toolcore.DefaultCommandOutputBytes,
		events: toolcore.ToolCallEvents{Emit: func(delta toolcore.OutputDelta) {
			deltas = append(deltas, delta)
		}},
	}
	session.appendOutput([]byte(strings.Repeat("z", maxShellDeltaBytes*2+17)))

	if len(deltas) != 3 {
		t.Fatalf("delta count = %d, want 3", len(deltas))
	}
	for index, delta := range deltas {
		if len(delta.Text) > maxShellDeltaBytes {
			t.Fatalf("delta %d bytes = %d, want <= %d", index, len(delta.Text), maxShellDeltaBytes)
		}
	}
}

func TestShellSessionCarriesUTF8RuneAcrossWriterCalls(t *testing.T) {
	var deltas []toolcore.OutputDelta
	session := &shellSession{
		id:            1,
		started:       time.Now(),
		maxTranscript: toolcore.DefaultCommandOutputBytes,
		events: toolcore.ToolCallEvents{Emit: func(delta toolcore.OutputDelta) {
			deltas = append(deltas, delta)
		}},
	}
	encoded := []byte("开始")
	session.appendOutput(encoded[:len(encoded)-1])
	session.appendOutput(encoded[len(encoded)-1:])

	var streamed strings.Builder
	for _, delta := range deltas {
		if delta.BinaryOmitted {
			t.Fatalf("split localized text was classified as binary: %+v", delta)
		}
		streamed.WriteString(delta.Text)
	}
	if streamed.String() != "开始" {
		t.Fatalf("streamed text = %q, want localized output", streamed.String())
	}
}

func TestShellSessionCarriesUTF8RuneAcrossObservationSnapshots(t *testing.T) {
	session := &shellSession{
		id:            1,
		started:       time.Now(),
		maxTranscript: toolcore.DefaultCommandOutputBytes,
		events:        toolcore.ToolCallEvents{Emit: func(toolcore.OutputDelta) {}},
	}
	encoded := []byte("开始")
	session.appendOutput(encoded[:len(encoded)-1])

	first := session.snapshot(true, defaultShellMaxOutputTokens)
	if first.BinaryOmitted || first.Output != "开" {
		t.Fatalf("first observation = %+v, want complete prefix only", first)
	}
	if first.OriginalBytes != len([]byte("开")) {
		t.Fatalf("first original bytes = %d, want complete prefix bytes", first.OriginalBytes)
	}

	session.setInvocationEvents(toolcore.ToolCallEvents{})
	session.appendOutput(encoded[len(encoded)-1:])
	second := session.snapshot(true, defaultShellMaxOutputTokens)
	if second.BinaryOmitted || second.Output != "始" {
		t.Fatalf("second observation = %+v, want reconstructed rune", second)
	}
	if second.OriginalBytes != len([]byte("始")) {
		t.Fatalf("second original bytes = %d, want reconstructed rune bytes", second.OriginalBytes)
	}
}

func TestShellSessionCarriesUnownedUTF8PrefixIntoActiveInvocation(t *testing.T) {
	var deltas []toolcore.OutputDelta
	session := &shellSession{
		id:            1,
		started:       time.Now(),
		maxTranscript: toolcore.DefaultCommandOutputBytes,
	}
	encoded := []byte("始")
	session.appendOutput(encoded[:len(encoded)-1])
	session.setInvocationEvents(toolcore.ToolCallEvents{
		Name:      "write_stdin",
		ToolUseID: "write-1",
		Emit: func(delta toolcore.OutputDelta) {
			deltas = append(deltas, delta)
		},
	})
	session.appendOutput(encoded[len(encoded)-1:])

	if len(deltas) != 1 {
		t.Fatalf("delta count = %d, want 1", len(deltas))
	}
	if deltas[0].BinaryOmitted || deltas[0].Text != "始" {
		t.Fatalf("delta = %+v, want reconstructed text rune", deltas[0])
	}
	if deltas[0].ToolUseID != "write-1" {
		t.Fatalf("tool_use_id = %q, want write-1", deltas[0].ToolUseID)
	}
	result := session.snapshot(true, defaultShellMaxOutputTokens)
	if result.BinaryOmitted || result.Output != "始" {
		t.Fatalf("terminal result = %+v, want reconstructed text rune", result)
	}
}
