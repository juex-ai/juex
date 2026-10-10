package native

import (
	"fmt"
	"strings"
)

const (
	patchHeader = "*** Begin Patch"
	patchFooter = "*** End Patch"
)

type patchOperationKind string

const (
	patchAdd    patchOperationKind = "add"
	patchUpdate patchOperationKind = "update"
	patchDelete patchOperationKind = "delete"
)

type patchOperation struct {
	kind   patchOperationKind
	path   string
	moveTo string
	lines  []patchLine
}

type patchLine struct {
	kind byte
	text string
}

type patchHunk struct {
	oldText   string
	newText   string
	additions int
	deletions int
}

func parsePatch(patchText string) ([]patchOperation, error) {
	text := strings.TrimSpace(strings.ReplaceAll(patchText, "\r\n", "\n"))
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || lines[0] != patchHeader || lines[len(lines)-1] != patchFooter {
		return nil, fmt.Errorf("apply_patch: patch must start with %q and end with %q", patchHeader, patchFooter)
	}
	var ops []patchOperation
	for i := 1; i < len(lines)-1; {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
			i++
			var content []patchLine
			for i < len(lines)-1 && !strings.HasPrefix(lines[i], "*** ") {
				if !strings.HasPrefix(lines[i], "+") {
					return nil, fmt.Errorf("apply_patch: add file %q expects + lines, got %q", path, lines[i])
				}
				content = append(content, patchLine{kind: '+', text: strings.TrimPrefix(lines[i], "+")})
				i++
			}
			ops = append(ops, patchOperation{kind: patchAdd, path: path, lines: content})
		case strings.HasPrefix(line, "*** Update File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
			i++
			op := patchOperation{kind: patchUpdate, path: path}
			if i < len(lines)-1 && strings.HasPrefix(lines[i], "*** Move to: ") {
				op.moveTo = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to: "))
				i++
			}
			for i < len(lines)-1 && !strings.HasPrefix(lines[i], "*** ") {
				if strings.HasPrefix(lines[i], "@@") {
					op.lines = append(op.lines, patchLine{kind: '@'})
					i++
					continue
				}
				if lines[i] == "" {
					if i+1 >= len(lines)-1 || strings.HasPrefix(lines[i+1], "*** ") {
						i++
						continue
					}
					op.lines = append(op.lines, patchLine{kind: ' ', text: ""})
					i++
					continue
				}
				prefix := lines[i][0]
				if prefix != ' ' && prefix != '+' && prefix != '-' {
					return nil, fmt.Errorf("apply_patch: update file %q expects context, +, -, or @@ lines, got %q", path, lines[i])
				}
				op.lines = append(op.lines, patchLine{kind: prefix, text: lines[i][1:]})
				i++
			}
			ops = append(ops, op)
		case strings.HasPrefix(line, "*** Delete File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
			ops = append(ops, patchOperation{kind: patchDelete, path: path})
			i++
		default:
			return nil, fmt.Errorf("apply_patch: unexpected line %q", line)
		}
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("apply_patch: empty patch")
	}
	return ops, nil
}

func patchLinesContent(lines []patchLine) (string, int) {
	var b strings.Builder
	additions := 0
	for _, line := range lines {
		b.WriteString(line.text)
		b.WriteByte('\n')
		if line.kind == '+' {
			additions++
		}
	}
	return b.String(), additions
}

func patchHunks(lines []patchLine) ([]patchHunk, error) {
	var hunks []patchHunk
	var current []patchLine
	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		hunk := buildPatchHunk(current)
		if hunk.oldText == "" {
			return fmt.Errorf("hunk has no context or deleted lines")
		}
		hunks = append(hunks, hunk)
		current = nil
		return nil
	}
	for _, line := range lines {
		if line.kind == '@' {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		current = append(current, line)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("no hunks")
	}
	return hunks, nil
}

func buildPatchHunk(lines []patchLine) patchHunk {
	var oldB, newB strings.Builder
	var h patchHunk
	for _, line := range lines {
		switch line.kind {
		case ' ':
			oldB.WriteString(line.text)
			oldB.WriteByte('\n')
			newB.WriteString(line.text)
			newB.WriteByte('\n')
		case '-':
			oldB.WriteString(line.text)
			oldB.WriteByte('\n')
			h.deletions++
		case '+':
			newB.WriteString(line.text)
			newB.WriteByte('\n')
			h.additions++
		}
	}
	h.oldText = oldB.String()
	h.newText = newB.String()
	return h
}

func applyPatchHunks(rel, content string, hunks []patchHunk) (string, int, int, error) {
	var additions, deletions int
	for _, hunk := range hunks {
		count := strings.Count(content, hunk.oldText)
		switch count {
		case 0:
			return "", 0, 0, fmt.Errorf("apply_patch: update file %s: context not found", rel)
		case 1:
			content = strings.Replace(content, hunk.oldText, hunk.newText, 1)
			additions += hunk.additions
			deletions += hunk.deletions
		default:
			return "", 0, 0, fmt.Errorf("apply_patch: update file %s: ambiguous context occurs %d times", rel, count)
		}
	}
	return content, additions, deletions, nil
}
