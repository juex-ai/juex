package managedruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

type ThreadNotes struct {
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type ThreadTask struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	Acceptance        string    `json:"acceptance"`
	Status            string    `json:"status"`
	StatusReason      string    `json:"status_reason"`
	Priority          string    `json:"priority"`
	ContinuationCount int       `json:"continuation_count"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ThreadState is current working context, separate from immutable history and
// a Turn's frozen configuration. Revision fences compaction snapshots.
type ThreadState struct {
	Notes    ThreadNotes  `json:"notes"`
	Tasks    []ThreadTask `json:"tasks"`
	Revision int64        `json:"revision"`
}

type ThreadStateAction struct {
	InputIDs     []string `json:"input_ids,omitempty"`
	Instructions *string  `json:"instructions,omitempty"`
	Kind         string   `json:"kind"`
	ID           string   `json:"id,omitempty"`
	Content      *string  `json:"content,omitempty"`
	Title        *string  `json:"title,omitempty"`
	Description  *string  `json:"description,omitempty"`
	Acceptance   *string  `json:"acceptance,omitempty"`
	Status       *string  `json:"status,omitempty"`
	StatusReason *string  `json:"status_reason,omitempty"`
	Priority     *string  `json:"priority,omitempty"`
}

type ThreadStateStore interface {
	ApplyThreadStateAction(context.Context, ToolWork, ThreadStateAction) (json.RawMessage, error)
}

func IsThreadStateTool(name string) bool {
	return slices.Contains(ThreadStateToolNames(), name)
}

func ThreadStateToolNames() []string {
	return []string{"check_inputs", "update_notes", "list_tasks", "create_task", "update_task", "delete_task", "context_new", "context_compact"}
}

func ParseThreadStateAction(call llm.Block) (ThreadStateAction, error) {
	var action ThreadStateAction
	if _, supplied := call.Input["kind"]; supplied {
		return action, ErrInvalid
	}
	encoded, err := json.Marshal(call.Input)
	if err != nil {
		return action, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&action); err != nil || action.Kind != "" {
		return action, ErrInvalid
	}
	action.Kind = call.ToolName
	return action, nil
}

func (s ThreadState) Validate() error {
	if s.Revision < 0 || !utf8.ValidString(s.Notes.Content) || utf8.RuneCountInString(s.Notes.Content) > 2048 || len(s.Tasks) > 64 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	reserved := slices.Clone(s.Tasks)
	for i, task := range s.Tasks {
		if strings.TrimSpace(task.ID) == "" || seen[task.ID] || strings.TrimSpace(task.Title) == "" || strings.TrimSpace(task.Description) == "" || task.UpdatedAt.IsZero() || task.ContinuationCount < 0 {
			return ErrInvalid
		}
		seen[task.ID] = true
		if !slices.Contains([]string{"todo", "doing", "done", "pending", "failed"}, task.Status) || !slices.Contains([]string{"p0", "p1", "p2"}, task.Priority) {
			return ErrInvalid
		}
		for _, value := range []string{task.ID, task.Title, task.Description, task.Acceptance, task.StatusReason} {
			if !utf8.ValidString(value) {
				return ErrInvalid
			}
		}
		// A task accepted now must leave room for later runtime-owned metadata.
		reserved[i].ContinuationCount = math.MaxInt
		reserved[i].UpdatedAt = time.Date(9999, 12, 31, 23, 59, 59, 999000000, time.UTC)
	}
	if len((ThreadState{Tasks: reserved}).TasksContext()) > 32<<10 {
		return fmt.Errorf("%w: tasks exceed 32 KiB; shorten or delete existing tasks", ErrInvalid)
	}
	return nil
}

func (s ThreadState) NotesContext() string {
	if strings.TrimSpace(s.Notes.Content) == "" {
		return ""
	}
	return "Current working notes (model-owned; rewrite with update_notes):\n" + strings.TrimSpace(s.Notes.Content)
}

func (s ThreadState) TasksContext() string {
	if len(s.Tasks) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("Current thread tasks (model-owned; use create_task, update_task and delete_task to keep this list current):\n")
	for _, task := range s.Tasks {
		encoded, _ := json.Marshal(task)
		out.Write(encoded)
		out.WriteByte('\n')
	}
	return strings.TrimSpace(out.String())
}

func (s ThreadState) SelectedTask() (ThreadTask, bool) {
	var selected ThreadTask
	for _, task := range s.Tasks {
		if task.Status != "doing" && task.Status != "todo" {
			continue
		}
		if selected.ID == "" || task.Status == "doing" && selected.Status == "todo" || task.Status == selected.Status && task.Priority < selected.Priority {
			selected = task
		}
	}
	return selected, selected.ID != ""
}

func (s ThreadState) RenewContext(clearNotes, pruneTasks bool) ThreadState {
	if pruneTasks {
		s.Tasks = slices.DeleteFunc(slices.Clone(s.Tasks), func(task ThreadTask) bool { return task.Status == "done" })
	}
	if clearNotes {
		s.Notes = ThreadNotes{}
	}
	s.Revision++
	return s
}

// ContinueTask commits the entire selected snapshot, so a stale completion
// decision cannot increment a changed task or a newly selected replacement.
func (s ThreadState) ContinueTask(selected ThreadTask, now time.Time) (ThreadState, error) {
	current, ok := s.SelectedTask()
	if !ok || current != selected || current.ContinuationCount == math.MaxInt {
		return s, ErrConflict
	}
	s.Tasks = slices.Clone(s.Tasks)
	index := slices.IndexFunc(s.Tasks, func(task ThreadTask) bool { return task.ID == selected.ID })
	s.Tasks[index].ContinuationCount++
	s.Tasks[index].UpdatedAt = now.UTC().Truncate(time.Millisecond)
	s.Revision++
	return s, s.Validate()
}

// Apply is pure: persistence must atomically commit the new state, event and
// tool receipt. A rejected mutation never changes the caller's slice.
func (s ThreadState) Apply(action ThreadStateAction, newID string, now time.Time, clearCompletedNotes bool) (ThreadState, json.RawMessage, error) {
	before := s
	if action.Instructions != nil {
		return before, nil, ErrInvalid
	}
	s.Tasks = slices.Clone(s.Tasks)
	now = now.UTC().Truncate(time.Millisecond)
	var value any
	editable := action.Title != nil || action.Description != nil || action.Acceptance != nil || action.Status != nil || action.StatusReason != nil || action.Priority != nil
	switch action.Kind {
	case "update_notes":
		if action.Content == nil || action.ID != "" || editable || !utf8.ValidString(*action.Content) || utf8.RuneCountInString(*action.Content) > 2048 {
			return before, nil, ErrInvalid
		}
		s.Notes = ThreadNotes{Content: redactThreadText(*action.Content), UpdatedAt: now}
		value = map[string]any{"notes": s.Notes}
	case "list_tasks":
		if action.Content != nil || action.ID != "" || editable {
			return before, nil, ErrInvalid
		}
		encoded, err := json.Marshal(map[string]any{"tasks": append([]ThreadTask{}, s.Tasks...)})
		return before, encoded, err
	case "create_task", "update_task", "delete_task":
		if action.Content != nil {
			return before, nil, ErrInvalid
		}
		index := slices.IndexFunc(s.Tasks, func(task ThreadTask) bool { return task.ID == action.ID })
		var task ThreadTask
		if action.Kind == "create_task" {
			if action.ID != "" || action.Title == nil || action.Description == nil || newID == "" || slices.ContainsFunc(s.Tasks, func(task ThreadTask) bool { return task.ID == newID }) {
				return before, nil, ErrInvalid
			}
			task = ThreadTask{ID: newID, Status: "todo", Priority: "p1"}
		} else {
			if action.ID == "" || index < 0 {
				return before, nil, ErrInvalid
			}
			task = s.Tasks[index]
		}
		if action.Kind == "delete_task" {
			if editable {
				return before, nil, ErrInvalid
			}
			s.Tasks = slices.Delete(s.Tasks, index, index+1)
			value = map[string]any{"deleted": true, "id": task.ID}
		} else {
			if !editable {
				return before, nil, ErrInvalid
			}
			for _, field := range []struct {
				value  *string
				target *string
				limit  int
			}{{action.Title, &task.Title, 1000}, {action.Description, &task.Description, 1000}, {action.Acceptance, &task.Acceptance, 32 << 10}, {action.StatusReason, &task.StatusReason, 1000}} {
				if field.value != nil {
					if !utf8.ValidString(*field.value) {
						return before, nil, ErrInvalid
					}
					*field.target = sanitizeThreadText(strings.TrimSpace(*field.value), field.limit)
				}
			}
			if action.Status != nil {
				task.Status = strings.TrimSpace(*action.Status)
			}
			if action.Priority != nil {
				task.Priority = strings.TrimSpace(*action.Priority)
			}
			task.UpdatedAt = now
			if action.Kind == "create_task" {
				s.Tasks = append(s.Tasks, task)
			} else {
				s.Tasks[index] = task
			}
			value = map[string]any{"task": task}
		}
		if clearCompletedNotes && len(s.Tasks) > 0 && !slices.ContainsFunc(s.Tasks, func(task ThreadTask) bool { return task.Status != "done" }) {
			s.Notes = ThreadNotes{}
		}
	default:
		return before, nil, ErrInvalid
	}
	s.Revision++
	if err := s.Validate(); err != nil {
		return before, nil, err
	}
	encoded, err := json.Marshal(value)
	return s, encoded, err
}

var threadSecretAssignment = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|authorization|cookie|token)[A-Za-z0-9_-]*\s*[:=]\s*("[^"\n\r]*"|'[^'\n\r]*'|(bearer\s+)?[^ \n\r\t]+)`)
var threadBearer = regexp.MustCompile(`(?i)bearer\s+[^ \n\r\t]+`)
var threadOpenAIKey = regexp.MustCompile(`sk-[A-Za-z0-9_-]{6,}`)

func redactThreadText(text string) string {
	text = threadSecretAssignment.ReplaceAllString(text, "[REDACTED]")
	text = threadBearer.ReplaceAllString(text, "Bearer [REDACTED]")
	return threadOpenAIKey.ReplaceAllString(text, "[REDACTED]")
}

func sanitizeThreadText(text string, limit int) string {
	if len(text) > limit {
		end := limit
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		text = fmt.Sprintf("%s...(truncated, total %d bytes)", text[:end], len(text))
	}
	return redactThreadText(text)
}
