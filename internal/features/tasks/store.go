package tasks

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/homestore"
	modstate "github.com/juex-ai/juex/internal/framework/module/state"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type Status string
type Priority string

const (
	Todo    Status   = "todo"
	Doing   Status   = "doing"
	Done    Status   = "done"
	Pending Status   = "pending"
	Failed  Status   = "failed"
	P0      Priority = "p0"
	P1      Priority = "p1"
	P2      Priority = "p2"
)

const (
	maxTaskCount        = 64
	maxTaskContextBytes = 32 * 1024
)

var owner = modstate.Owner{Module: ModuleID, Scope: modstate.ScopeThread}

type Task struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	Acceptance        string    `json:"acceptance"`
	Status            Status    `json:"status"`
	StatusReason      string    `json:"status_reason"`
	Priority          Priority  `json:"priority"`
	ContinuationCount int       `json:"continuation_count"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type TasksSnapshot struct {
	Tasks []Task `json:"tasks"`
}
type State struct {
	Version int    `json:"version"`
	Tasks   []Task `json:"tasks"`
}
type Create struct {
	Title        string
	Description  string
	Acceptance   string
	Status       Status
	StatusReason string
	Priority     Priority
}
type Update struct {
	Title        *string
	Description  *string
	Acceptance   *string
	Status       Status
	StatusReason *string
	Priority     Priority
}
type Options struct{ Now func() time.Time }
type Store struct {
	ThreadDir string
	Path      string
	Now       func() time.Time
	mu        sync.Mutex
}
type GateDecision struct {
	Task           Task
	BlockStop      bool
	ContinuePrompt string
	Reason         string
}

func NewStore(threadDir string, opts Options) *Store {
	return &Store{ThreadDir: threadDir, Path: filepath.Join(modstate.Directory(threadDir, owner), "tasks.json"), Now: opts.Now}
}

func (s *Store) Snapshot() (State, error) {
	if s == nil {
		return State{Version: 1, Tasks: []Task{}}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}
func (s *Store) StatusSnapshot() (*TasksSnapshot, error) {
	state, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	return state.StatusSnapshot(), nil
}
func (s State) StatusSnapshot() *TasksSnapshot {
	if len(s.Tasks) == 0 {
		return nil
	}
	return &TasksSnapshot{Tasks: append([]Task(nil), s.Tasks...)}
}
func (s State) RawMessage() json.RawMessage {
	data, _ := json.Marshal(s)
	return data
}

func (s *Store) Create(in Create) (Task, error) {
	if s == nil {
		return Task{}, fmt.Errorf("tasks state is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := thread.CheckContextRenewalFileReady(s.ThreadDir, s.Path); err != nil {
		return Task{}, err
	}
	state, err := s.loadLocked()
	if err != nil {
		return Task{}, err
	}
	if in.Status == "" {
		in.Status = Todo
	}
	if in.Priority == "" {
		in.Priority = P1
	}
	task := normalize(Task{ID: rand.Text(), Title: in.Title, Description: in.Description, Acceptance: in.Acceptance, Status: in.Status, StatusReason: in.StatusReason, Priority: in.Priority, UpdatedAt: s.now()})
	if err := validate(task); err != nil {
		return Task{}, err
	}
	state.Tasks = append(state.Tasks, task)
	if err := validateCapacity(state); err != nil {
		return Task{}, err
	}
	if err := s.saveLocked(state); err != nil {
		return Task{}, err
	}
	return task, nil
}
func (s *Store) Update(id string, in Update) (Task, error) {
	if s == nil {
		return Task{}, fmt.Errorf("tasks state is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := thread.CheckContextRenewalFileReady(s.ThreadDir, s.Path); err != nil {
		return Task{}, err
	}
	state, err := s.loadLocked()
	if err != nil {
		return Task{}, err
	}
	for i, task := range state.Tasks {
		if task.ID != id {
			continue
		}
		if in.Title != nil {
			task.Title = *in.Title
		}
		if in.Description != nil {
			task.Description = *in.Description
		}
		if in.Acceptance != nil {
			task.Acceptance = *in.Acceptance
		}
		if in.StatusReason != nil {
			task.StatusReason = *in.StatusReason
		}
		if in.Status != "" {
			task.Status = in.Status
		}
		if in.Priority != "" {
			task.Priority = in.Priority
		}
		task.UpdatedAt = s.now()
		task = normalize(task)
		if err := validate(task); err != nil {
			return Task{}, err
		}
		state.Tasks[i] = task
		if err := validateCapacity(state); err != nil {
			return Task{}, err
		}
		if err := s.saveLocked(state); err != nil {
			return Task{}, err
		}
		return task, nil
	}
	return Task{}, fmt.Errorf("task %q not found", id)
}
func (s *Store) Delete(id string) error {
	if s == nil {
		return fmt.Errorf("tasks state is not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := thread.CheckContextRenewalFileReady(s.ThreadDir, s.Path); err != nil {
		return err
	}
	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i, task := range state.Tasks {
		if task.ID == id {
			state.Tasks = append(state.Tasks[:i], state.Tasks[i+1:]...)
			return s.saveLocked(state)
		}
	}
	return fmt.Errorf("task %q not found", id)
}
func (s *Store) Clear() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := thread.CheckContextRenewalFileReady(s.ThreadDir, s.Path); err != nil {
		return err
	}
	return modstate.RemoveFile(s.Path)
}

func selectedTask(state State) (Task, bool) {
	var selected Task
	for _, task := range state.Tasks {
		if task.Status != Doing && task.Status != Todo {
			continue
		}
		if selected.ID == "" || (task.Status == Doing && selected.Status == Todo) ||
			(task.Status == selected.Status && task.Priority < selected.Priority) {
			selected = task
		}
	}
	return selected, selected.ID != ""
}
func (s *Store) CompletionGateDecision() (GateDecision, error) {
	state, err := s.Snapshot()
	if err != nil {
		return GateDecision{}, err
	}
	task, ok := selectedTask(state)
	if !ok {
		return GateDecision{}, nil
	}
	prompt := "Continue the selected unfinished task. Use update_task with its ID to record progress: doing while working, pending when new external input is required, done after verifying acceptance, or failed when it cannot be completed.\n\n" + task.render()
	return GateDecision{Task: task, BlockStop: true, ContinuePrompt: prompt, Reason: "task_unfinished"}, nil
}
func (s *Store) RecordContinuation(decision GateDecision) (bool, error) {
	if s == nil || !decision.BlockStop {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := thread.CheckContextRenewalFileReady(s.ThreadDir, s.Path); err != nil {
		return false, err
	}
	state, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	task, ok := selectedTask(state)
	// The complete selected value fences edits, deletion, reselection, and replay.
	if !ok || task != decision.Task {
		return false, nil
	}
	for i := range state.Tasks {
		if state.Tasks[i].ID == task.ID {
			state.Tasks[i].ContinuationCount++
			state.Tasks[i].UpdatedAt = s.now()
			break
		}
	}
	if err := s.saveLocked(state); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) StagePruneDone(generationID string) (finalize, rollback func() error, err error) {
	noop := func() error { return nil }
	if s == nil {
		return noop, noop, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := thread.CheckContextRenewalFileReady(s.ThreadDir, s.Path); err != nil {
		return nil, nil, err
	}
	state, err := s.loadLocked()
	if err != nil {
		return nil, nil, err
	}
	remaining := state.unfinished()
	if len(remaining.Tasks) == len(state.Tasks) {
		return noop, noop, nil
	}
	data, err := encode(remaining)
	if err != nil {
		return nil, nil, err
	}
	change, err := thread.StageContextRenewalFileReplace(s.ThreadDir, s.Path, generationID, data)
	if err != nil {
		return nil, nil, err
	}
	wrap := func(action func() error) func() error {
		return func() error { s.mu.Lock(); defer s.mu.Unlock(); return action() }
	}
	return wrap(change.Finalize), wrap(change.Rollback), nil
}
func (s State) unfinished() State {
	result := State{Version: 1, Tasks: []Task{}}
	for _, task := range s.Tasks {
		if task.Status != Done {
			result.Tasks = append(result.Tasks, task)
		}
	}
	return result
}
func (s *Store) loadLocked() (State, error) {
	state := State{Version: 1, Tasks: []Task{}}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("tasks read: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("tasks parse: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return State{}, fmt.Errorf("tasks parse: trailing content")
	}
	if state.Version != 1 {
		return State{}, fmt.Errorf("tasks: unsupported version %d", state.Version)
	}
	seen := map[string]bool{}
	for _, task := range state.Tasks {
		if err := validate(task); err != nil {
			return State{}, err
		}
		if seen[task.ID] {
			return State{}, fmt.Errorf("tasks: duplicate id %q", task.ID)
		}
		seen[task.ID] = true
	}
	if state.Tasks == nil {
		state.Tasks = []Task{}
	}
	return state, nil
}
func encode(state State) ([]byte, error) {
	data, err := json.MarshalIndent(state, "", "  ")
	return append(data, '\n'), err
}

func validateCapacity(state State) error {
	if len(state.Tasks) > maxTaskCount {
		return fmt.Errorf("tasks capacity is %d entries; consolidate or delete existing tasks first", maxTaskCount)
	}
	bounded := State{Version: state.Version, Tasks: append([]Task(nil), state.Tasks...)}
	for i := range bounded.Tasks {
		// Reserve runtime-owned metadata so subsequent continuations cannot
		// consume the room accepted by a model-owned task mutation.
		bounded.Tasks[i].ContinuationCount = math.MaxInt
		bounded.Tasks[i].UpdatedAt = time.Date(9999, 12, 31, 23, 59, 59, 999000000, time.UTC)
	}
	text, _ := bounded.RenderProviderContext()
	if len(text) > maxTaskContextBytes {
		return fmt.Errorf("tasks context exceeds %d bytes; shorten or delete existing tasks first", maxTaskContextBytes)
	}
	return nil
}

func (s *Store) saveLocked(state State) error {
	data, err := encode(state)
	if err != nil {
		return err
	}
	if _, err := modstate.Prepare(s.ThreadDir, owner, modstate.DiscardOnRemoval); err != nil {
		return err
	}
	return homestore.WriteFileAtomic(s.Path, data, 0600, 0700)
}
func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Millisecond)
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}
func normalize(task Task) Task {
	task.Title = sanitizeTaskTextLimit(strings.TrimSpace(task.Title), 1000)
	task.Description = sanitizeTaskTextLimit(strings.TrimSpace(task.Description), 1000)
	task.Acceptance = sanitizeTaskTextLimit(strings.TrimSpace(task.Acceptance), 32*1024)
	task.StatusReason = sanitizeTaskTextLimit(strings.TrimSpace(task.StatusReason), 1000)
	return task
}
func validate(task Task) error {
	if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Title) == "" || strings.TrimSpace(task.Description) == "" {
		return fmt.Errorf("task id, title and description are required")
	}
	switch task.Status {
	case Todo, Doing, Done, Pending, Failed:
	default:
		return fmt.Errorf("invalid task status %q", task.Status)
	}
	switch task.Priority {
	case P0, P1, P2:
	default:
		return fmt.Errorf("invalid task priority %q", task.Priority)
	}
	if task.ContinuationCount < 0 {
		return fmt.Errorf("task continuation_count cannot be negative")
	}
	if task.UpdatedAt.IsZero() {
		return fmt.Errorf("task updated_at is required")
	}
	return nil
}
func (t Task) render() string {
	data, _ := json.Marshal(t)
	return string(data)
}
func (s State) RenderProviderContext() (string, bool) {
	if len(s.Tasks) == 0 {
		return "", false
	}
	var b strings.Builder
	b.WriteString("Current thread tasks (model-owned; use create_task, update_task and delete_task to keep this list current):\n")
	for _, task := range s.Tasks {
		b.WriteString(task.render())
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String()), true
}
