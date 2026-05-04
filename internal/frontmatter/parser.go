// Package frontmatter parses a tiny subset of YAML frontmatter that v0.1
// memory entries and skill files use.
//
// Supported:
//   - leading `---` line, terminating `---` line
//   - top-level `key: value` pairs (string values, optionally quoted)
//
// Anything more elaborate (lists, nested maps) lives in the body — v0.1 has
// no need to parse it.
package frontmatter

import (
	"bufio"
	"fmt"
	"strings"
)

type Parsed struct {
	Fields map[string]string
	Body   string
}

// Parse splits raw into frontmatter fields and body. The body is taken
// verbatim — every byte after the closing `---` line (including any
// trailing newlines) is preserved so a Format → Parse round trip is exact.
//
// If there is no frontmatter, Fields is empty and Body is raw verbatim.
func Parse(raw string) (Parsed, error) {
	p := Parsed{Fields: map[string]string{}, Body: raw}

	// Quick reject: must start with the opening fence at byte 0.
	if !strings.HasPrefix(raw, "---\n") && raw != "---" && !strings.HasPrefix(raw, "---\r\n") {
		return p, nil
	}

	// Locate the closing fence. We accept either "\n---\n" or "\n---" at EOF.
	rest := raw
	// Skip the opening fence line (handle CRLF too).
	if strings.HasPrefix(rest, "---\r\n") {
		rest = rest[5:]
	} else if strings.HasPrefix(rest, "---\n") {
		rest = rest[4:]
	} else {
		return p, fmt.Errorf("frontmatter: missing closing ---")
	}

	closeIdx := indexClosingFence(rest)
	if closeIdx < 0 {
		return p, fmt.Errorf("frontmatter: missing closing ---")
	}
	fmBody := rest[:closeIdx]
	body := ""
	// Skip the closing fence + the newline that follows it (if any).
	tail := rest[closeIdx:]
	switch {
	case strings.HasPrefix(tail, "---\n"):
		body = tail[4:]
	case strings.HasPrefix(tail, "---\r\n"):
		body = tail[5:]
	case tail == "---":
		body = ""
	default:
		return p, fmt.Errorf("frontmatter: missing closing ---")
	}

	scanner := bufio.NewScanner(strings.NewReader(fmBody))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if err := parseLine(scanner.Text(), p.Fields); err != nil {
			return p, err
		}
	}
	p.Body = body
	return p, nil
}

// indexClosingFence returns the byte index of the next "---" that sits on
// its own line (i.e. starts at a line boundary and is followed by EOL/EOF),
// or -1 if not found.
func indexClosingFence(s string) int {
	// Each iteration looks for "---" beginning at position 0 of the current
	// segment. We slice the string in line-sized chunks.
	pos := 0
	for pos < len(s) {
		end := strings.IndexByte(s[pos:], '\n')
		var line string
		if end < 0 {
			line = s[pos:]
		} else {
			line = s[pos : pos+end]
		}
		if strings.TrimRight(line, "\r") == "---" {
			return pos
		}
		if end < 0 {
			return -1
		}
		pos += end + 1
	}
	return -1
}

func parseLine(line string, into map[string]string) error {
	trim := strings.TrimSpace(line)
	if trim == "" || strings.HasPrefix(trim, "#") {
		return nil
	}
	colon := strings.Index(trim, ":")
	if colon < 0 {
		return fmt.Errorf("frontmatter: bad line: %q", line)
	}
	key := strings.TrimSpace(trim[:colon])
	val := strings.TrimSpace(trim[colon+1:])
	val = unquote(val)
	into[key] = val
	return nil
}

// unquote strips a single matching pair of outer quotes (single or double).
// Values that start with one quote but do not end with the same one — or
// that contain unbalanced internal quotes — are returned unchanged so the
// data round-trips faithfully.
func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if (first == '"' || first == '\'') && first == last {
		return s[1 : len(s)-1]
	}
	return s
}

// Format serialises fields and body back into a frontmatter document.
// Fields are emitted in the provided keyOrder; missing keys are skipped.
// Any field not in keyOrder is appended in insertion-iteration order.
func Format(fields map[string]string, keyOrder []string, body string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	seen := map[string]bool{}
	for _, k := range keyOrder {
		if v, ok := fields[k]; ok {
			fmt.Fprintf(&sb, "%s: %s\n", k, v)
			seen[k] = true
		}
	}
	for k, v := range fields {
		if seen[k] {
			continue
		}
		fmt.Fprintf(&sb, "%s: %s\n", k, v)
	}
	sb.WriteString("---\n")
	sb.WriteString(body)
	return sb.String()
}
