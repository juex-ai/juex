package markdown

import "strings"

// ListLiteralBlocks applies literal-block guards within bullet and ordered
// lists. Container indentation does not count toward code-block indentation.
type ListLiteralBlocks struct {
	literals LiteralBlocks
	indents  []int
}

// Literal consumes one line, retaining list context across blank lines.
func (s *ListLiteralBlocks) Literal(line string) bool {
	content := strings.TrimLeft(line, " \t")
	if strings.TrimSpace(content) == "" {
		return s.literals.Literal("")
	}
	indent := indentation(line[:len(line)-len(content)], 0)
	for len(s.indents) > 0 && indent < s.indents[len(s.indents)-1] {
		s.indents = s.indents[:len(s.indents)-1]
		// A fence inside a list ends when its containing item ends.
		s.literals = LiteralBlocks{}
	}
	base := 0
	if len(s.indents) > 0 {
		base = s.indents[len(s.indents)-1]
	}
	if s.literals.Literal(strings.Repeat(" ", indent-base) + content) {
		return true
	}
	width := listMarkerWidth(content)
	if width == 0 {
		return false
	}
	rest := content[width:]
	text := strings.TrimLeft(rest, " \t")
	spacing := indentation(rest[:len(rest)-len(text)], indent+width) - indent - width
	padding := spacing
	if padding == 0 || padding > 4 {
		padding = 1
	}
	s.indents = append(s.indents, indent+width+padding)
	return s.literals.Literal(strings.Repeat(" ", max(0, spacing-padding)) + text)
}

func indentation(space string, column int) int {
	for _, char := range space {
		if char == '\t' {
			column += 4 - column%4
		} else {
			column++
		}
	}
	return column
}

func listMarkerWidth(line string) int {
	width := 0
	if strings.ContainsRune("-*+", rune(line[0])) {
		// Thematic breaks do not establish a list container.
		compact := strings.Join(strings.Fields(line), "")
		if len(compact) >= 3 && strings.Trim(compact, string(line[0])) == "" {
			return 0
		}
		width = 1
	} else {
		for width < len(line) && line[width] >= '0' && line[width] <= '9' {
			width++
		}
		if width == 0 || width > 9 || width == len(line) || (line[width] != '.' && line[width] != ')') {
			return 0
		}
		width++
	}
	if width == len(line) || line[width] == ' ' || line[width] == '\t' {
		return width
	}
	return 0
}
