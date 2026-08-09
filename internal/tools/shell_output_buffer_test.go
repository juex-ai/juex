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

	snapshot := buffer.Snapshot(64)
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

	snapshot := buffer.Snapshot(defaultShellTranscriptBytes)
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

	snapshot := buffer.Snapshot(32)
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

func TestShellOutputBufferKeepsUTF8Boundaries(t *testing.T) {
	var buffer shellOutputBuffer
	input := []byte(strings.Repeat("开", 30) + strings.Repeat("中", 40) + strings.Repeat("结", 30))
	buffer.Append(input, 127)

	snapshot := buffer.Snapshot(61)
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

	snapshot := buffer.Snapshot(defaultShellTranscriptBytes)
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
