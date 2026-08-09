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

func TestSanitizeOutputPreservesPrefixAndTailBinaryDetectionCoverage(t *testing.T) {
	middleBinary := strings.Repeat("a", 4<<10) + strings.Repeat("\x00", 4<<10) + strings.Repeat("z", 4<<10)
	tailBinary := strings.Repeat("text", 5<<10) + "\x00tail"
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "binary in former prefix window", data: middleBinary},
		{name: "binary in tail window", data: tailBinary},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := SanitizeOutputBytes([]byte(test.data)); !got.Binary.Omitted {
				t.Fatalf("byte output exposed binary payload")
			}
			if got := SanitizeOutputText(test.data); !got.Binary.Omitted {
				t.Fatalf("text output exposed binary payload")
			}
		})
	}
}

func TestSanitizeOutputDetectsInvalidUTF8AcrossLargeSamples(t *testing.T) {
	const size = 3 * binaryOutputDetectionBytes
	for _, test := range []struct {
		name  string
		index int
	}{
		{name: "head interior", index: 100},
		{name: "head boundary", index: binaryOutputDetectionBytes - 1},
		{name: "tail boundary", index: size - binaryOutputDetectionBytes},
		{name: "tail interior", index: size - 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := []byte(strings.Repeat("a", size))
			data[test.index] = 0xff
			if got := SanitizeOutputBytes(data); !got.Binary.Omitted {
				t.Fatalf("byte output exposed invalid UTF-8 at %d", test.index)
			}
			if got := SanitizeOutputText(string(data)); !got.Binary.Omitted {
				t.Fatalf("text output exposed invalid UTF-8 at %d", test.index)
			}
		})
	}
}

func TestSanitizeOutputPreservesTextWhenSampleCutsUTF8Rune(t *testing.T) {
	data := []byte(strings.Repeat("a", binaryOutputDetectionBytes-1) + "中文" + strings.Repeat("z", 2*binaryOutputDetectionBytes))
	if got := SanitizeOutputBytes(data); got.Binary.Omitted || got.Text != string(data) {
		t.Fatalf("byte output was not preserved: %+v", got.Binary)
	}
	if got := SanitizeOutputText(string(data)); got.Binary.Omitted || got.Text != string(data) {
		t.Fatalf("text output was not preserved: %+v", got.Binary)
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
