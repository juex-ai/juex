package runtime

import "strings"

// Summary sections use Markdown headings, but literal code/text blocks may
// contain identical lines. Share this state between normalization and parsing.
type compactionSummarySyntax struct {
	fence  byte
	length int
}

func (s *compactionSummarySyntax) literal(line string) bool {
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
