package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	binaryOutputFirstBytes     = 16
	binaryOutputDetectionBytes = 8 << 10
)

type BinaryOutputInfo struct {
	Omitted       bool
	Bytes         int
	SHA256        string
	FirstBytesHex string
}

type SanitizedOutput struct {
	Text   string
	Binary BinaryOutputInfo
}

func SanitizeOutputBytes(data []byte) SanitizedOutput {
	if len(data) == 0 {
		return SanitizedOutput{}
	}
	if !isBinaryOutput(data) {
		return SanitizedOutput{Text: string(data)}
	}
	info := newBinaryOutputInfo(data)
	return SanitizedOutput{
		Text:   info.Placeholder(),
		Binary: info,
	}
}

func SanitizeOutputText(text string) SanitizedOutput {
	if len(text) == 0 {
		return SanitizedOutput{}
	}
	if !isBinaryOutputText(text) {
		return SanitizedOutput{Text: text}
	}
	info := newBinaryOutputInfo([]byte(text))
	return SanitizedOutput{
		Text:   info.Placeholder(),
		Binary: info,
	}
}

func (i BinaryOutputInfo) Placeholder() string {
	if !i.Omitted {
		return ""
	}
	return fmt.Sprintf("[binary output omitted: bytes=%d sha256=%s first_bytes_hex=%s]", i.Bytes, i.SHA256, i.FirstBytesHex)
}

func newBinaryOutputInfo(data []byte) BinaryOutputInfo {
	sum := sha256.Sum256(data)
	sampleLen := len(data)
	if sampleLen > binaryOutputFirstBytes {
		sampleLen = binaryOutputFirstBytes
	}
	return BinaryOutputInfo{
		Omitted:       true,
		Bytes:         len(data),
		SHA256:        hex.EncodeToString(sum[:]),
		FirstBytesHex: hex.EncodeToString(data[:sampleLen]),
	}
}

func isBinaryOutput(data []byte) bool {
	return isBinaryOutputText(string(binaryDetectionSample(data)))
}

func isBinaryOutputText(text string) bool {
	text = textDetectionSample(text)
	if strings.IndexByte(text, 0) >= 0 {
		return true
	}
	if !utf8.ValidString(text) {
		return true
	}
	text = stripANSIControls(text)
	if text == "" {
		return false
	}
	var control int
	var total int
	for _, r := range text {
		total++
		if isTextRune(r) {
			continue
		}
		if unicode.IsControl(r) || r == utf8.RuneError {
			control++
		}
	}
	if total == 0 {
		return false
	}
	return total >= 16 && float64(control)/float64(total) > 0.30
}

func binaryDetectionSample(data []byte) []byte {
	if len(data) <= binaryOutputDetectionBytes {
		return data
	}
	end := binaryOutputDetectionBytes
	for end > binaryOutputDetectionBytes-utf8.UTFMax && !utf8.Valid(data[:end]) {
		end--
	}
	return data[:end]
}

func textDetectionSample(text string) string {
	if len(text) <= binaryOutputDetectionBytes {
		return text
	}
	end := binaryOutputDetectionBytes
	for end > binaryOutputDetectionBytes-utf8.UTFMax && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end]
}

func isTextRune(r rune) bool {
	switch r {
	case '\n', '\r', '\t', '\b', '\f':
		return true
	default:
		return unicode.IsPrint(r)
	}
}

func stripANSIControls(text string) string {
	if !strings.ContainsRune(text, '\x1b') {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] != 0x1b {
			b.WriteByte(text[i])
			i++
			continue
		}
		i++
		if i >= len(text) {
			break
		}
		switch text[i] {
		case '[':
			i++
			for i < len(text) {
				c := text[i]
				i++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
		case ']':
			i++
			for i < len(text) {
				if text[i] == 0x07 {
					i++
					break
				}
				if text[i] == 0x1b && i+1 < len(text) && text[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default:
			i++
		}
	}
	return b.String()
}
