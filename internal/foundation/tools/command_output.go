package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"unicode"
	"unicode/utf8"
)

type CommandResult struct {
	SessionID          int    `json:"session_id,omitempty"`
	Output             string `json:"output,omitempty"`
	ExitCode           *int   `json:"exit_code,omitempty"`
	Running            bool   `json:"running"`
	TimedOut           bool   `json:"timed_out,omitempty"`
	WallTimeMS         int64  `json:"wall_time_ms"`
	ChunkID            int    `json:"chunk_id,omitempty"`
	OriginalBytes      int    `json:"original_bytes"`
	OriginalTokenCount int    `json:"original_token_count"`
	Truncated          bool   `json:"truncated,omitempty"`
	BinaryOmitted      bool   `json:"binary_omitted,omitempty"`
	BinaryBytes        int    `json:"binary_bytes,omitempty"`
	BinarySHA256       string `json:"binary_sha256,omitempty"`
	FirstBytesHex      string `json:"first_bytes_hex,omitempty"`
}

func (r CommandResult) ToolCallTimedOut() bool {
	return r.TimedOut
}

func (r CommandResult) ToolCallExitCode() (int, bool) {
	if r.ExitCode == nil {
		return 0, false
	}
	return *r.ExitCode, true
}

type CommandOutputBuffer struct {
	limit      int
	head       []byte
	tail       []byte
	totalBytes int64
	digest     hash.Hash
	firstBytes []byte
	classifier commandOutputClassifier
}

type CommandOutputSnapshot struct {
	Bytes      []byte
	TotalBytes int64
	Truncated  bool
	Binary     BinaryOutputInfo
}

type commandOutputClassifier struct {
	binary          bool
	rawPendingUTF8  []byte
	textPendingUTF8 []byte
	ansiState       uint8
	runes           int64
	controls        int64
}

const (
	shellANSIText uint8 = iota
	shellANSIEscape
	shellANSICSI
	shellANSIOSC
	shellANSIOSCEscape
)

func (b *CommandOutputBuffer) Append(data []byte, limit int) {
	if len(data) == 0 {
		return
	}
	if limit <= 0 {
		limit = DefaultCommandOutputBytes
	}
	if b.limit == 0 {
		b.limit = limit
	}
	if b.digest == nil {
		b.digest = sha256.New()
	}
	b.classifier.Append(data)
	_, _ = b.digest.Write(data)
	if len(b.firstBytes) < binaryOutputFirstBytes {
		needed := binaryOutputFirstBytes - len(b.firstBytes)
		if needed > len(data) {
			needed = len(data)
		}
		b.firstBytes = append(b.firstBytes, data[:needed]...)
	}
	b.totalBytes += int64(len(data))

	headLimit := (b.limit + 1) / 2
	tailLimit := b.limit - headLimit
	if len(b.head) < headLimit {
		needed := headLimit - len(b.head)
		if needed > len(data) {
			needed = len(data)
		}
		b.head = append(b.head, data[:needed]...)
		data = data[needed:]
	}
	if tailLimit == 0 || len(data) == 0 {
		return
	}
	b.tail = append(b.tail, data...)
	if len(b.tail) > tailLimit {
		b.tail = append([]byte(nil), b.tail[len(b.tail)-tailLimit:]...)
	}
}

func (b *CommandOutputBuffer) Snapshot(limit int, final bool) CommandOutputSnapshot {
	if b == nil || b.totalBytes == 0 {
		return CommandOutputSnapshot{}
	}
	if limit <= 0 || limit > b.limit {
		limit = b.limit
	}
	if limit <= 0 {
		limit = DefaultCommandOutputBytes
	}

	head := b.head
	tail := b.tail
	totalBytes := b.totalBytes
	binary := b.classifier.IsBinary(final)
	if !final && !binary && len(b.classifier.rawPendingUTF8) > 0 {
		head, tail = trimShellOutputSuffix(head, tail, len(b.classifier.rawPendingUTF8))
		totalBytes -= int64(len(b.classifier.rawPendingUTF8))
	}
	if totalBytes == 0 {
		return CommandOutputSnapshot{}
	}

	var retained []byte
	truncated := totalBytes > int64(limit)
	if !truncated {
		retained = make([]byte, 0, len(head)+len(tail))
		retained = append(retained, head...)
		retained = append(retained, tail...)
	} else {
		headSource := head
		tailSource := tail
		if int64(len(head)+len(tail)) == totalBytes {
			full := make([]byte, 0, len(head)+len(tail))
			full = append(full, head...)
			full = append(full, tail...)
			headSource = full
			tailSource = full
		}
		headLimit := (limit + 1) / 2
		tailLimit := limit - headLimit
		headLen := min(headLimit, len(headSource))
		tailLen := min(tailLimit, len(tailSource))
		headLen = ValidUTF8PrefixLength(headSource, headLen)
		tailStart := validUTF8SuffixStart(tailSource, len(tailSource)-tailLen)
		tailLen = len(tailSource) - tailStart
		omitted := totalBytes - int64(headLen) - int64(tailLen)
		retained = make([]byte, 0, headLen+tailLen+64)
		retained = append(retained, headSource[:headLen]...)
		retained = fmt.Appendf(retained, "[output truncated: %d bytes omitted]\n", omitted)
		retained = append(retained, tailSource[tailStart:]...)
	}

	if !binary {
		return CommandOutputSnapshot{
			Bytes:      retained,
			TotalBytes: totalBytes,
			Truncated:  truncated,
		}
	}
	info := BinaryOutputInfo{
		Omitted:       true,
		Bytes:         boundedByteCount(totalBytes),
		FirstBytesHex: hex.EncodeToString(b.firstBytes),
	}
	if b.digest != nil {
		info.SHA256 = hex.EncodeToString(b.digest.Sum(nil))
	}
	return CommandOutputSnapshot{
		Bytes:      []byte(info.Placeholder()),
		TotalBytes: totalBytes,
		Truncated:  truncated,
		Binary:     info,
	}
}

func (b *CommandOutputBuffer) PendingUTF8() []byte {
	if b == nil || b.classifier.IsBinary(false) || len(b.classifier.rawPendingUTF8) == 0 {
		return nil
	}
	return append([]byte(nil), b.classifier.rawPendingUTF8...)
}

func (b *CommandOutputBuffer) Reset() {
	if b == nil {
		return
	}
	*b = CommandOutputBuffer{}
}

func (b *CommandOutputBuffer) TotalBytes() int64 {
	if b == nil {
		return 0
	}
	return b.totalBytes
}

func (c *commandOutputClassifier) Append(data []byte) {
	c.appendRaw(data)
	if c.binary {
		return
	}
	for len(data) > 0 && !c.binary {
		switch c.ansiState {
		case shellANSIEscape:
			switch data[0] {
			case '[':
				c.ansiState = shellANSICSI
			case ']':
				c.ansiState = shellANSIOSC
			default:
				c.ansiState = shellANSIText
			}
			data = data[1:]
			continue
		case shellANSICSI:
			value := data[0]
			data = data[1:]
			if value >= 0x40 && value <= 0x7e {
				c.ansiState = shellANSIText
			}
			continue
		case shellANSIOSC:
			value := data[0]
			data = data[1:]
			switch value {
			case 0x07:
				c.ansiState = shellANSIText
			case 0x1b:
				c.ansiState = shellANSIOSCEscape
			}
			continue
		case shellANSIOSCEscape:
			if data[0] == '\\' {
				c.ansiState = shellANSIText
			} else {
				c.ansiState = shellANSIOSC
			}
			data = data[1:]
			continue
		}

		escape := bytes.IndexByte(data, 0x1b)
		if escape < 0 {
			c.appendText(data)
			return
		}
		c.appendText(data[:escape])
		if c.binary {
			return
		}
		c.ansiState = shellANSIEscape
		data = data[escape+1:]
	}
}

func (c *commandOutputClassifier) appendRaw(data []byte) {
	if len(c.rawPendingUTF8) > 0 {
		data = append(append([]byte(nil), c.rawPendingUTF8...), data...)
		c.rawPendingUTF8 = nil
	}
	for len(data) > 0 {
		if !utf8.FullRune(data) {
			c.rawPendingUTF8 = append([]byte(nil), data...)
			return
		}
		r, size := utf8.DecodeRune(data)
		if r == 0 || (r == utf8.RuneError && size == 1) {
			c.binary = true
			c.rawPendingUTF8 = nil
			return
		}
		data = data[size:]
	}
}

func (c *commandOutputClassifier) appendText(data []byte) {
	if len(c.textPendingUTF8) > 0 {
		data = append(append([]byte(nil), c.textPendingUTF8...), data...)
		c.textPendingUTF8 = nil
	}
	for len(data) > 0 {
		if !utf8.FullRune(data) {
			c.textPendingUTF8 = append([]byte(nil), data...)
			return
		}
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			c.binary = true
			return
		}
		c.runes++
		if r == 0 {
			c.binary = true
			return
		}
		if !isTextRune(r) && (r == utf8.RuneError || unicode.IsControl(r)) {
			c.controls++
		}
		data = data[size:]
	}
}

func (c *commandOutputClassifier) IsBinary(final bool) bool {
	if c == nil {
		return false
	}
	if c.binary || (final && len(c.rawPendingUTF8) > 0) {
		return true
	}
	return c.runes >= 16 && float64(c.controls)/float64(c.runes) > 0.30
}

func SanitizeCommandOutputBytes(data []byte) SanitizedOutput {
	var classifier commandOutputClassifier
	classifier.Append(data)
	if !classifier.IsBinary(true) {
		return SanitizedOutput{Text: string(data)}
	}
	info := newBinaryOutputInfo(data)
	return SanitizedOutput{Text: info.Placeholder(), Binary: info}
}

// BoundShellContent applies Shell head/tail retention and binary hygiene to
// runtime-added content without changing an already-bounded Shell result.
func BoundCommandContent(content string, limit int) string {
	const markerHeadroom = 64
	if limit <= 0 {
		limit = DefaultCommandOutputBytes
	}
	if len(content) <= limit+markerHeadroom {
		return SanitizeCommandOutputBytes([]byte(content)).Text
	}
	var buffer CommandOutputBuffer
	buffer.Append([]byte(content), limit)
	return string(buffer.Snapshot(limit, true).Bytes)
}

func trimShellOutputSuffix(head, tail []byte, count int) ([]byte, []byte) {
	if count <= 0 {
		return head, tail
	}
	if count <= len(tail) {
		return head, tail[:len(tail)-count]
	}
	count -= len(tail)
	tail = nil
	if count >= len(head) {
		return nil, nil
	}
	return head[:len(head)-count], tail
}

func ValidUTF8PrefixLength(data []byte, length int) int {
	if length <= 0 || length > len(data) {
		return max(0, min(length, len(data)))
	}
	for candidate, attempts := length, 0; candidate > 0 && attempts < utf8.UTFMax; candidate, attempts = candidate-1, attempts+1 {
		if utf8.Valid(data[:candidate]) {
			return candidate
		}
	}
	return 0
}

func validUTF8SuffixStart(data []byte, start int) int {
	if start < 0 {
		start = 0
	}
	if start >= len(data) {
		return len(data)
	}
	for candidate, attempts := start, 0; candidate < len(data) && attempts < utf8.UTFMax; candidate, attempts = candidate+1, attempts+1 {
		if utf8.Valid(data[candidate:]) {
			return candidate
		}
	}
	return len(data)
}

const DefaultCommandOutputBytes = 1 << 20

func boundedByteCount(value int64) int {
	if value <= 0 {
		return 0
	}
	return int(min(value, int64(^uint(0)>>1)))
}
