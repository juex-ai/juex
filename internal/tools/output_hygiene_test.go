package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestSanitizeOutputBytesOmitsBinaryPayloads(t *testing.T) {
	data := []byte{0x00, 0x01, 'P', 'N', 'G', 0xff}
	got := SanitizeOutputBytes(data)
	if !got.Binary.Omitted {
		t.Fatalf("binary omitted = false")
	}
	sum := sha256.Sum256(data)
	wantSHA := hex.EncodeToString(sum[:])
	if got.Binary.Bytes != len(data) || got.Binary.SHA256 != wantSHA || got.Binary.FirstBytesHex != "0001504e47ff" {
		t.Fatalf("binary metadata = %+v", got.Binary)
	}
	for _, forbidden := range []string{"\x00", "\xff"} {
		if strings.Contains(got.Text, forbidden) {
			t.Fatalf("sanitized text contains raw byte %q: %q", forbidden, got.Text)
		}
	}
	for _, want := range []string{"[binary output omitted:", "bytes=6", "sha256=" + wantSHA, "first_bytes_hex=0001504e47ff"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("sanitized text missing %q: %q", want, got.Text)
		}
	}
}

func TestSanitizeOutputBytesPreservesTextLogs(t *testing.T) {
	text := "ok 中文\n\x1b[31mcolored pytest log\x1b[0m\n"
	got := SanitizeOutputBytes([]byte(text))
	if got.Binary.Omitted {
		t.Fatalf("text output was treated as binary: %+v", got.Binary)
	}
	if got.Text != text {
		t.Fatalf("text = %q, want %q", got.Text, text)
	}
}

func TestSanitizeOutputBytesOmitsControlHeavyText(t *testing.T) {
	data := []byte("abc\x01\x02\x03\x04\x05\x06\x07\x08\x0e\x0f\x10\x11\x12\x13\x14")
	got := SanitizeOutputBytes(data)
	if !got.Binary.Omitted {
		t.Fatalf("control-heavy text should be omitted: %q", got.Text)
	}
}

func TestRegistryCallWithInfoSanitizesHandlerBinaryOutput(t *testing.T) {
	r := NewRegistry()
	payload := []byte{0x00, 0x01, 'P', 'N', 'G'}
	if err := r.Register(Tool{
		Name:   "binary",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return string(payload), nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "binary", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[binary output omitted:") {
		t.Fatalf("output was not sanitized: %q", out)
	}
	if strings.Contains(out, string(payload)) {
		t.Fatalf("output contains raw payload: %q", out)
	}
	if info.Observation == nil || info.Observation.Content != out {
		t.Fatalf("observation content = %+v, want sanitized output", info.Observation)
	}
}
