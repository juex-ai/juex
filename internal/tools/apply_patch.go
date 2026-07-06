package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/sandbox"
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

type patchChange struct {
	kind      patchOperationKind
	rel       string
	abs       string
	moveRel   string
	moveAbs   string
	content   []byte
	mode      os.FileMode
	additions int
	deletions int
}

type patchWorkspace struct {
	root     string
	evalRoot string
}

type patchSummary struct {
	changes   []patchChange
	adds      int
	updates   int
	deletes   int
	moves     int
	additions int
	deletions int
}

type patchSnapshot struct {
	abs     string
	existed bool
	data    []byte
	mode    os.FileMode
}

func applyPatchTool(workDir string, guard sandbox.PathGuard) Tool {
	return Tool{
		Name:        "apply_patch",
		Description: "Apply a Codex-style patch inside the workspace. Supports add, update, delete, and move operations. Input is {patch_text: \"*** Begin Patch\\n...\\n*** End Patch\"}.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"patch_text": map[string]any{
					"type":        "string",
					"description": "Codex-style patch text with *** Begin Patch / *** End Patch envelope.",
				},
			},
			"required": []string{"patch_text"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			patchText, _ := in["patch_text"].(string)
			if strings.TrimSpace(patchText) == "" {
				return "", fmt.Errorf("apply_patch: missing patch_text")
			}
			summary, err := applyPatch(ctx, workDir, patchText, guard)
			if err != nil {
				return "", err
			}
			return formatPatchSummary(summary), nil
		},
	}
}

func applyPatch(ctx context.Context, workDir, patchText string, guard sandbox.PathGuard) (patchSummary, error) {
	ops, err := parsePatch(patchText)
	if err != nil {
		return patchSummary{}, err
	}
	ws, err := newPatchWorkspace(workDir)
	if err != nil {
		return patchSummary{}, err
	}
	summary, err := planPatch(ws, ops, guard)
	if err != nil {
		return patchSummary{}, err
	}
	if err := ctx.Err(); err != nil {
		return patchSummary{}, err
	}
	if err := applyPatchChanges(summary.changes); err != nil {
		return patchSummary{}, err
	}
	return summary, nil
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

func newPatchWorkspace(workDir string) (patchWorkspace, error) {
	root := workDir
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return patchWorkspace{}, err
		}
		root = cwd
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return patchWorkspace{}, err
	}
	evalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return patchWorkspace{}, err
	}
	return patchWorkspace{root: absRoot, evalRoot: evalRoot}, nil
}

func (w patchWorkspace) resolve(path string) (string, string, error) {
	if strings.TrimSpace(path) == "" {
		return "", "", fmt.Errorf("apply_patch: unsafe path %q", path)
	}
	if strings.Contains(path, ":") || strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return "", "", fmt.Errorf("apply_patch: unsafe path %q: colons and UNC paths are not allowed", path)
	}
	path = filepath.FromSlash(path)
	if filepath.IsAbs(path) {
		return "", "", fmt.Errorf("apply_patch: unsafe path %q: absolute paths are not allowed", path)
	}
	rel := filepath.Clean(path)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("apply_patch: unsafe path %q: path escapes workspace", path)
	}
	abs := filepath.Join(w.root, rel)
	if !pathWithin(w.root, abs) {
		return "", "", fmt.Errorf("apply_patch: unsafe path %q: path escapes workspace", path)
	}
	if err := w.checkSymlinkBoundary(abs, rel); err != nil {
		return "", "", err
	}
	return filepath.ToSlash(rel), abs, nil
}

func (w patchWorkspace) checkSymlinkBoundary(abs, rel string) error {
	checkPath := abs
	if _, err := os.Lstat(checkPath); err != nil {
		for {
			parent := filepath.Dir(checkPath)
			if parent == checkPath {
				return nil
			}
			if _, statErr := os.Lstat(parent); statErr == nil {
				checkPath = parent
				break
			}
			checkPath = parent
		}
	}
	evaluated, err := filepath.EvalSymlinks(checkPath)
	if err != nil {
		return err
	}
	if !pathWithin(w.evalRoot, evaluated) {
		return fmt.Errorf("apply_patch: unsafe path %q: symlink escapes workspace", filepath.ToSlash(rel))
	}
	return nil
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func planPatch(ws patchWorkspace, ops []patchOperation, guard sandbox.PathGuard) (patchSummary, error) {
	var summary patchSummary
	touched := map[string]bool{}
	for _, op := range ops {
		rel, abs, err := ws.resolve(op.path)
		if err != nil {
			return patchSummary{}, err
		}
		if err := guard.Check(abs); err != nil {
			return patchSummary{}, fmt.Errorf("apply_patch: %w", err)
		}
		if touched[rel] {
			return patchSummary{}, fmt.Errorf("apply_patch: duplicate operation for %s", rel)
		}
		touched[rel] = true
		switch op.kind {
		case patchAdd:
			change, err := planPatchAdd(rel, abs, op)
			if err != nil {
				return patchSummary{}, err
			}
			summary.adds++
			summary.additions += change.additions
			summary.changes = append(summary.changes, change)
		case patchUpdate:
			moveRel, moveAbs := "", ""
			if op.moveTo != "" {
				moveRel, moveAbs, err = ws.resolve(op.moveTo)
				if err != nil {
					return patchSummary{}, err
				}
				if err := guard.Check(moveAbs); err != nil {
					return patchSummary{}, fmt.Errorf("apply_patch: %w", err)
				}
				if moveRel == rel {
					return patchSummary{}, fmt.Errorf("apply_patch: move target matches source for %s", rel)
				}
				if touched[moveRel] {
					return patchSummary{}, fmt.Errorf("apply_patch: duplicate operation for %s", moveRel)
				}
				touched[moveRel] = true
			}
			change, err := planPatchUpdate(rel, abs, moveRel, moveAbs, op)
			if err != nil {
				return patchSummary{}, err
			}
			summary.updates++
			if change.moveRel != "" {
				summary.moves++
			}
			summary.additions += change.additions
			summary.deletions += change.deletions
			summary.changes = append(summary.changes, change)
		case patchDelete:
			change, err := planPatchDelete(rel, abs)
			if err != nil {
				return patchSummary{}, err
			}
			summary.deletes++
			summary.changes = append(summary.changes, change)
		}
	}
	return summary, nil
}

func planPatchAdd(rel, abs string, op patchOperation) (patchChange, error) {
	if _, err := os.Stat(abs); err == nil {
		return patchChange{}, fmt.Errorf("apply_patch: add file %s already exists", rel)
	} else if !os.IsNotExist(err) {
		return patchChange{}, err
	}
	content, additions := patchLinesContent(op.lines)
	return patchChange{kind: patchAdd, rel: rel, abs: abs, content: []byte(content), mode: 0o644, additions: additions}, nil
}

func planPatchUpdate(rel, abs, moveRel, moveAbs string, op patchOperation) (patchChange, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return patchChange{}, err
	}
	if info.IsDir() {
		return patchChange{}, fmt.Errorf("apply_patch: update file %s is a directory", rel)
	}
	if moveAbs != "" {
		if _, err := os.Stat(moveAbs); err == nil {
			return patchChange{}, fmt.Errorf("apply_patch: move target %s already exists", moveRel)
		} else if !os.IsNotExist(err) {
			return patchChange{}, err
		}
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return patchChange{}, err
	}
	content := string(data)
	var additions, deletions int
	if len(op.lines) == 0 {
		if moveAbs == "" {
			return patchChange{}, fmt.Errorf("apply_patch: update file %s has no hunks", rel)
		}
	} else {
		hunks, err := patchHunks(op.lines)
		if err != nil {
			return patchChange{}, fmt.Errorf("apply_patch: update file %s: %w", rel, err)
		}
		content, additions, deletions, err = applyPatchHunks(rel, content, hunks)
		if err != nil {
			return patchChange{}, err
		}
	}
	return patchChange{kind: patchUpdate, rel: rel, abs: abs, moveRel: moveRel, moveAbs: moveAbs, content: []byte(content), mode: info.Mode().Perm(), additions: additions, deletions: deletions}, nil
}

func planPatchDelete(rel, abs string) (patchChange, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return patchChange{}, err
	}
	if info.IsDir() {
		return patchChange{}, fmt.Errorf("apply_patch: delete file %s is a directory", rel)
	}
	return patchChange{kind: patchDelete, rel: rel, abs: abs}, nil
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

func applyPatchChanges(changes []patchChange) error {
	snapshots, err := snapshotPatchChanges(changes)
	if err != nil {
		return err
	}
	if err := writePatchChanges(changes); err != nil {
		if rollbackErr := rollbackPatchChanges(snapshots); rollbackErr != nil {
			return fmt.Errorf("apply_patch: write failed: %w (rollback also failed: %v)", err, rollbackErr)
		}
		return err
	}
	return nil
}

func snapshotPatchChanges(changes []patchChange) ([]patchSnapshot, error) {
	seen := map[string]bool{}
	var snapshots []patchSnapshot
	for _, change := range changes {
		for _, abs := range []string{change.abs, change.moveAbs} {
			if abs == "" || seen[abs] {
				continue
			}
			seen[abs] = true
			snapshot := patchSnapshot{abs: abs}
			if info, err := os.Stat(abs); err == nil {
				if info.IsDir() {
					return nil, fmt.Errorf("apply_patch: %s is a directory", abs)
				}
				data, err := os.ReadFile(abs)
				if err != nil {
					return nil, err
				}
				snapshot.existed = true
				snapshot.data = data
				snapshot.mode = info.Mode().Perm()
			} else if !os.IsNotExist(err) {
				return nil, err
			}
			snapshots = append(snapshots, snapshot)
		}
	}
	return snapshots, nil
}

func writePatchChanges(changes []patchChange) error {
	for _, change := range changes {
		switch change.kind {
		case patchAdd:
			if err := writePatchFile(change.abs, change.content, change.mode); err != nil {
				return err
			}
		case patchUpdate:
			if change.moveAbs != "" {
				if err := writePatchFile(change.moveAbs, change.content, change.mode); err != nil {
					return err
				}
				if err := os.Remove(change.abs); err != nil {
					return err
				}
				continue
			}
			if err := writePatchFile(change.abs, change.content, change.mode); err != nil {
				return err
			}
		case patchDelete:
			if err := os.Remove(change.abs); err != nil {
				return err
			}
		}
	}
	return nil
}

func writePatchFile(path string, content []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, mode)
}

func rollbackPatchChanges(snapshots []patchSnapshot) error {
	var firstErr error
	for i := len(snapshots) - 1; i >= 0; i-- {
		snapshot := snapshots[i]
		var err error
		if snapshot.existed {
			err = writePatchFile(snapshot.abs, snapshot.data, snapshot.mode)
		} else {
			err = os.Remove(snapshot.abs)
			if os.IsNotExist(err) {
				err = nil
			}
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func formatPatchSummary(summary patchSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "applied patch: %d files changed (add=%d update=%d delete=%d move=%d, +%d -%d)", len(summary.changes), summary.adds, summary.updates, summary.deletes, summary.moves, summary.additions, summary.deletions)
	for _, change := range summary.changes {
		switch {
		case change.kind == patchUpdate && change.moveRel != "":
			fmt.Fprintf(&b, "\n- move %s -> %s", change.rel, change.moveRel)
		default:
			fmt.Fprintf(&b, "\n- %s %s", change.kind, change.rel)
		}
	}
	return b.String()
}
