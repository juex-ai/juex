package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestShellOutputBufferPreservesHeadAndTailWithExactMarker(t *testing.T) {
	var buffer shellOutputBuffer
	input := []byte(strings.Repeat("H", 32) + strings.Repeat("M", 36) + strings.Repeat("T", 32))
	buffer.Append(input, 64)

	snapshot := buffer.Snapshot(64, true)
	if snapshot.TotalBytes != int64(len(input)) {
		t.Fatalf("total bytes = %d, want %d", snapshot.TotalBytes, len(input))
	}
	if !snapshot.Truncated {
		t.Fatal("snapshot should be truncated")
	}
	want := strings.Repeat("H", 32) + "[output truncated: 36 bytes omitted]\n" + strings.Repeat("T", 32)
	if string(snapshot.Bytes) != want {
		t.Fatalf("snapshot = %q, want %q", snapshot.Bytes, want)
	}
}

func TestShellOutputBufferBoundsDefaultTextOutputOverOneMiB(t *testing.T) {
	var buffer shellOutputBuffer
	head := "HEAD-SENTINEL\n"
	tail := "\nTAIL-SENTINEL"
	omitted := 4096
	middleSize := defaultShellTranscriptBytes + omitted - len(head) - len(tail)
	input := []byte(head + strings.Repeat("m", middleSize) + tail)
	buffer.Append(input, defaultShellTranscriptBytes)

	snapshot := buffer.Snapshot(defaultShellTranscriptBytes, true)
	text := string(snapshot.Bytes)
	if !strings.HasPrefix(text, head) || !strings.HasSuffix(text, tail) {
		t.Fatalf("bounded output lost sentinels: prefix=%t suffix=%t", strings.HasPrefix(text, head), strings.HasSuffix(text, tail))
	}
	marker := fmt.Sprintf("[output truncated: %d bytes omitted]\n", omitted)
	if strings.Count(text, marker) != 1 {
		t.Fatalf("bounded output marker count = %d, want one %q", strings.Count(text, marker), marker)
	}
	if len(snapshot.Bytes) != defaultShellTranscriptBytes+len(marker) {
		t.Fatalf("bounded output bytes = %d, want %d raw bytes plus marker", len(snapshot.Bytes), defaultShellTranscriptBytes+len(marker))
	}
}

func TestShellOutputBufferAppliesSmallerProjectionToBothEnds(t *testing.T) {
	var buffer shellOutputBuffer
	input := []byte("HEAD-" + strings.Repeat("middle", 20) + "-TAIL")
	buffer.Append(input, 128)

	snapshot := buffer.Snapshot(32, true)
	if !snapshot.Truncated {
		t.Fatal("snapshot should be truncated by projection limit")
	}
	if !strings.HasPrefix(string(snapshot.Bytes), "HEAD-") || !strings.HasSuffix(string(snapshot.Bytes), "-TAIL") {
		t.Fatalf("snapshot did not preserve both ends: %q", snapshot.Bytes)
	}
	wantMarker := fmt.Sprintf("[output truncated: %d bytes omitted]", len(input)-32)
	if strings.Count(string(snapshot.Bytes), wantMarker) != 1 {
		t.Fatalf("snapshot marker = %q, want exactly one %q", snapshot.Bytes, wantMarker)
	}
}

func TestShellOutputBufferSmallerProjectionUsesTailBeforeRetentionHeadFills(t *testing.T) {
	var buffer shellOutputBuffer
	head := "HEAD-SENTINEL\n"
	tail := "\nTAIL-SENTINEL"
	input := []byte(head + strings.Repeat("m", 100<<10-len(head)-len(tail)) + tail)
	buffer.Append(input, defaultShellTranscriptBytes)

	snapshot := buffer.Snapshot(16<<10, true)
	if !snapshot.Truncated {
		t.Fatal("snapshot should be truncated by the smaller projection")
	}
	text := string(snapshot.Bytes)
	if !strings.HasPrefix(text, head) || !strings.HasSuffix(text, tail) {
		t.Fatalf("smaller projection lost head/tail sentinels: prefix=%t suffix=%t", strings.HasPrefix(text, head), strings.HasSuffix(text, tail))
	}
	wantMarker := fmt.Sprintf("[output truncated: %d bytes omitted]", len(input)-(16<<10))
	if strings.Count(text, wantMarker) != 1 {
		t.Fatalf("snapshot marker = %q, want one %q", text, wantMarker)
	}
}

func TestShellOutputBufferKeepsUTF8Boundaries(t *testing.T) {
	var buffer shellOutputBuffer
	input := []byte(strings.Repeat("开", 30) + strings.Repeat("中", 40) + strings.Repeat("结", 30))
	buffer.Append(input, 127)

	snapshot := buffer.Snapshot(61, true)
	if !utf8.Valid(snapshot.Bytes) {
		t.Fatalf("snapshot is not valid UTF-8: %x", snapshot.Bytes)
	}
	if !strings.HasPrefix(string(snapshot.Bytes), "开") || !strings.HasSuffix(string(snapshot.Bytes), "结") {
		t.Fatalf("snapshot lost localized sentinels: %q", snapshot.Bytes)
	}
}

func TestShellOutputBufferReportsFullBinaryMetadataAfterTruncation(t *testing.T) {
	var buffer shellOutputBuffer
	input := make([]byte, defaultShellTranscriptBytes+4096)
	for index := range input {
		input[index] = byte(index % 251)
	}
	input[0] = 0
	buffer.Append(input, defaultShellTranscriptBytes)

	snapshot := buffer.Snapshot(defaultShellTranscriptBytes, true)
	wantSum := sha256.Sum256(input)
	if !snapshot.Binary.Omitted || snapshot.Binary.Bytes != len(input) {
		t.Fatalf("binary metadata = %+v", snapshot.Binary)
	}
	if snapshot.Binary.SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("binary sha = %q, want full logical output hash", snapshot.Binary.SHA256)
	}
	if strings.Contains(string(snapshot.Bytes), string(input[:16])) {
		t.Fatalf("binary snapshot exposed raw bytes: %q", snapshot.Bytes)
	}
}

func TestShellOutputBufferDetectsBinaryOnlyInOmittedMiddle(t *testing.T) {
	var buffer shellOutputBuffer
	input := []byte(strings.Repeat("h", defaultShellTranscriptBytes/2+1024) + "\x00" + strings.Repeat("t", defaultShellTranscriptBytes/2+1024))
	buffer.Append(input, defaultShellTranscriptBytes)

	snapshot := buffer.Snapshot(defaultShellTranscriptBytes, true)
	wantSum := sha256.Sum256(input)
	if !snapshot.Binary.Omitted || snapshot.Binary.Bytes != len(input) {
		t.Fatalf("binary metadata = %+v", snapshot.Binary)
	}
	if snapshot.Binary.SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("binary sha = %q, want full logical output hash", snapshot.Binary.SHA256)
	}
	if strings.Contains(string(snapshot.Bytes), strings.Repeat("h", 32)) {
		t.Fatalf("binary snapshot exposed retained text: %q", snapshot.Bytes)
	}
}

func TestShellOutputBufferClassifiesANSIAndLocalizedTextAcrossAppends(t *testing.T) {
	var buffer shellOutputBuffer
	parts := [][]byte{
		[]byte("\x1b[31"),
		[]byte("m中文\x1b[0m\x1b]0;标"),
		[]byte("题\x07正文"),
	}
	var input []byte
	for _, part := range parts {
		input = append(input, part...)
		buffer.Append(part, defaultShellTranscriptBytes)
	}

	snapshot := buffer.Snapshot(defaultShellTranscriptBytes, true)
	if snapshot.Binary.Omitted {
		t.Fatalf("ANSI text was classified as binary: %+v", snapshot.Binary)
	}
	if string(snapshot.Bytes) != string(input) {
		t.Fatalf("snapshot = %q, want %q", snapshot.Bytes, input)
	}
}

func TestShellOutputBufferDetectsBinaryInsideANSIEscape(t *testing.T) {
	tests := []struct {
		name  string
		parts [][]byte
	}{
		{
			name:  "OSC NUL",
			parts: [][]byte{[]byte("\x1b]0;title"), {0, 'r', 'e', 's', 't', 0x07}},
		},
		{
			name:  "OSC invalid UTF-8",
			parts: [][]byte{[]byte("\x1b]0;title"), {0xff, 0x07}},
		},
		{
			name:  "CSI invalid UTF-8",
			parts: [][]byte{[]byte("\x1b[3"), {0xff, 'm'}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var buffer shellOutputBuffer
			for _, part := range test.parts {
				buffer.Append(part, defaultShellTranscriptBytes)
			}
			snapshot := buffer.Snapshot(defaultShellTranscriptBytes, true)
			if !snapshot.Binary.Omitted {
				t.Fatalf("ANSI payload binary was exposed as text: %q", snapshot.Bytes)
			}
		})
	}
}

func TestShellOutputBufferConsumesPendingUTF8WhenControlRatioIsBinary(t *testing.T) {
	var buffer shellOutputBuffer
	input := append([]byte(strings.Repeat("\x01", 16)), []byte("中")[:2]...)
	buffer.Append(input, defaultShellTranscriptBytes)

	snapshot := buffer.Snapshot(defaultShellTranscriptBytes, false)
	wantSum := sha256.Sum256(input)
	if !snapshot.Binary.Omitted || snapshot.Binary.Bytes != len(input) {
		t.Fatalf("binary metadata = %+v, want all %d bytes", snapshot.Binary, len(input))
	}
	if snapshot.Binary.SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("binary sha = %q, want full window hash", snapshot.Binary.SHA256)
	}
	if carry := buffer.PendingUTF8(); len(carry) != 0 {
		t.Fatalf("binary window retained UTF-8 carry: %x", carry)
	}
}

func TestBoundShellContentReboundsFinalizedHookOutput(t *testing.T) {
	content := strings.Repeat("<", defaultShellTranscriptBytes+4096)
	bounded := BoundShellContent(content, defaultShellTranscriptBytes)
	if len(bounded) > defaultShellTranscriptBytes+64 {
		t.Fatalf("bounded content bytes = %d, want hard bound", len(bounded))
	}
	if !strings.HasPrefix(bounded, "<") || !strings.HasSuffix(bounded, "<") || !strings.Contains(bounded, "[output truncated:") {
		t.Fatalf("bounded content lost head/marker/tail: %q", bounded)
	}
}

func TestShellOutputBufferTinyLocalizedProjectionsRemainValidUTF8(t *testing.T) {
	input := []byte("中文🙂结尾")
	for _, limit := range []int{1, 2, 3, 4, 5, 7, 9, 11} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			var buffer shellOutputBuffer
			buffer.Append(input, defaultShellTranscriptBytes)
			snapshot := buffer.Snapshot(limit, true)
			if !utf8.Valid(snapshot.Bytes) {
				t.Fatalf("snapshot is invalid UTF-8: %x", snapshot.Bytes)
			}
			if !snapshot.Truncated || !strings.Contains(string(snapshot.Bytes), "[output truncated:") {
				t.Fatalf("snapshot = %q, want bounded marker", snapshot.Bytes)
			}
		})
	}
}

func TestShellSessionCapsLiveDeltasButKeepsDrainingIntoTerminalResult(t *testing.T) {
	var deltas []OutputDelta
	session := &shellSession{
		id:            1,
		started:       time.Now(),
		maxTranscript: 64,
		events: ToolCallEvents{
			Name:      "exec_command",
			ToolUseID: "tool-1",
			Emit: func(delta OutputDelta) {
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
	var deltas []OutputDelta
	session := &shellSession{
		id:            1,
		started:       time.Now(),
		maxTranscript: defaultShellTranscriptBytes,
		events: ToolCallEvents{Emit: func(delta OutputDelta) {
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
	var deltas []OutputDelta
	session := &shellSession{
		id:            1,
		started:       time.Now(),
		maxTranscript: defaultShellTranscriptBytes,
		events: ToolCallEvents{Emit: func(delta OutputDelta) {
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
		maxTranscript: defaultShellTranscriptBytes,
		events:        ToolCallEvents{Emit: func(OutputDelta) {}},
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

	session.setInvocationEvents(ToolCallEvents{})
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
	var deltas []OutputDelta
	session := &shellSession{
		id:            1,
		started:       time.Now(),
		maxTranscript: defaultShellTranscriptBytes,
	}
	encoded := []byte("始")
	session.appendOutput(encoded[:len(encoded)-1])
	session.setInvocationEvents(ToolCallEvents{
		Name:      "write_stdin",
		ToolUseID: "write-1",
		Emit: func(delta OutputDelta) {
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
