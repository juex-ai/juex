package managedruntime

import (
	"encoding/json"
	"strings"
)

// Only authoritative state replaces these sections. Historical messages and
// model prose cannot resurrect completed tasks or drop an unfinished note.
func (d CompactionDraft) Reconcile(text string) string {
	if d.TasksEnabled && len(d.ThreadState.Tasks) > 0 {
		state := d.ThreadState.RenewContext(false, true)
		encoded, _ := json.Marshal(map[string]any{"tasks": append([]ThreadTask{}, state.Tasks...)})
		longest, run := 0, 0
		for _, c := range string(encoded) {
			if c == '`' {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
		fence := strings.Repeat("`", max(3, longest+1))
		text = reconcileSummarySection(text, "Tasks", func(string) string { return fence + "json\n" + string(encoded) + "\n" + fence })
	}
	if d.NotesEnabled && strings.TrimSpace(d.ThreadState.Notes.Content) != "" {
		text = reconcileSummarySection(text, "Next Steps", func(candidate string) string { return reconcileNextSteps(candidate, d.ThreadState.Notes.Content) })
	}
	return text
}

func reconcileSummarySection(text, title string, reconcile func(string) string) string {
	lines := strings.Split(text, "\n")
	start, end := -1, len(lines)
	var syntax threadLiterals
	for i, line := range lines {
		if syntax.Literal(line) {
			continue
		}
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "#") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimLeft(trim, "#"))
		if start >= 0 {
			end = i
			break
		}
		if strings.EqualFold(heading, title) {
			start = i
		}
	}
	if start < 0 {
		return strings.TrimSpace(text) + "\n\n## " + title + "\n" + reconcile("")
	}
	before := strings.Join(lines[:start+1], "\n")
	after := strings.Join(lines[end:], "\n")
	return strings.TrimSpace(before + "\n" + reconcile(strings.Join(lines[start+1:end], "\n")) + "\n\n" + after)
}

func reconcileNextSteps(candidate, notes string) string {
	pending := map[string]string{}
	completed := map[string]bool{}
	var order []string
	var notesSyntax threadListLiterals
	for _, line := range strings.Split(notes, "\n") {
		if notesSyntax.Literal(line) {
			continue
		}
		line = strings.TrimSpace(line)
		key, _ := nextStepKey(line)
		if key == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "- [ ] "), strings.HasPrefix(line, "- [ ]\t"):
			if _, exists := pending[key]; !exists {
				order = append(order, key)
				pending[key] = line
			}
		case strings.HasPrefix(strings.ToLower(line), "- [x] "), strings.HasPrefix(strings.ToLower(line), "- [x]\t"):
			completed[key] = true
		}
	}
	seen := map[string]bool{}
	var lines []string
	var candidateSyntax threadListLiterals
	for _, line := range strings.Split(candidate, "\n") {
		if candidateSyntax.Literal(line) {
			lines = append(lines, line)
			continue
		}
		key, listEntry := nextStepKey(line)
		if original, ok := pending[key]; ok {
			if !seen[key] {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				lines = append(lines, indent+original)
				seen[key] = true
			}
		} else if !listEntry || !completed[key] {
			lines = append(lines, line)
		}
	}
	for _, key := range order {
		if !seen[key] {
			lines = append(lines, pending[key])
		}
	}
	return strings.Trim(strings.Join(lines, "\n"), "\r\n")
}

func nextStepKey(line string) (string, bool) {
	line = strings.ToLower(strings.TrimSpace(line))
	listEntry := false
	if len(line) > 1 && strings.ContainsRune("-*+", rune(line[0])) && (line[1] == ' ' || line[1] == '\t') {
		listEntry = true
		line = strings.TrimSpace(line[1:])
	} else {
		end := 0
		for end < len(line) && line[end] >= '0' && line[end] <= '9' {
			end++
		}
		if end > 0 && len(line) > end+1 && (line[end] == '.' || line[end] == ')') && (line[end+1] == ' ' || line[end+1] == '\t') {
			listEntry = true
			line = strings.TrimSpace(line[end+1:])
		}
	}
	for _, prefix := range []string{"[ ]", "[x]"} {
		if strings.HasPrefix(line, prefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			break
		}
	}
	return strings.Join(strings.Fields(line), " "), listEntry
}
