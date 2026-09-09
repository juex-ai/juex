// Package markdown provides syntax guards for line-oriented text transforms.
package markdown

import "strings"

// LiteralBlocks identifies fenced and indented literal lines so callers can
// transform surrounding headings or lists without rewriting examples.
type LiteralBlocks struct {
	fence  byte
	length int
}

// Literal consumes one line and reports whether it belongs to a literal block,
// including the opening and closing fences.
func (s *LiteralBlocks) Literal(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) >= 4 || strings.HasPrefix(trimmed, "\t") {
		return true
	}
	marker, count := byte(0), 0
	if len(trimmed) > 0 && (trimmed[0] == '`' || trimmed[0] == '~') {
		marker = trimmed[0]
		for count < len(trimmed) && trimmed[count] == marker {
			count++
		}
	}
	if s.fence != 0 {
		if marker == s.fence && count >= s.length && strings.TrimSpace(trimmed[count:]) == "" {
			s.fence, s.length = 0, 0
		}
		return true
	}
	if count >= 3 {
		s.fence, s.length = marker, count
		return true
	}
	return false
}

// InFence reports whether a fenced block still needs a closing delimiter.
func (s *LiteralBlocks) InFence() bool { return s.fence != 0 }
