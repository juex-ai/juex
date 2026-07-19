package runtime

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/skills"
)

var ErrSessionRuntimeBusy = errors.New("runtime: session runtime is busy")

// SessionRuntimeSnapshot is one coherent view of the session-scoped runtime
// dependencies. Replacement publishes a new bundle instead of mutating it;
// lifecycle owners keep old session resources alive for their readers.
type SessionRuntimeSnapshot struct {
	Session           *session.Session
	ScratchpadDir     string
	PendingInputQueue *PendingInputQueue
	Notes             *NotesStore
	GoalState         *GoalStateStore
	HookContext       hooks.Request
}

type sessionRuntimeState struct {
	SessionRuntimeSnapshot
	prompt *prompt.Builder
}

// ReplaceSessionRuntime builds and publishes every session-scoped dependency
// under one synchronization boundary. It serializes with turns and compaction,
// and refuses to move an active reservation or in-memory pending input.
func (e *Engine) ReplaceSessionRuntime(sess *session.Session) error {
	if e == nil || sess == nil || strings.TrimSpace(sess.Dir) == "" {
		return errors.New("runtime: replacement session is required")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessionRuntimeMu.Lock()
	defer e.sessionRuntimeMu.Unlock()
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()

	if e.activeTurnID != "" || len(e.pendingInput) > 0 {
		return ErrSessionRuntimeBusy
	}

	current := e.sessionRuntimeStateLocked()
	next := buildSessionRuntimeState(current, sess)
	e.publishSessionRuntimeLocked(next)
	return nil
}

// SessionRuntimeSnapshot returns one coherent copy of the current
// session-scoped runtime bundle.
func (e *Engine) SessionRuntimeSnapshot() SessionRuntimeSnapshot {
	if e == nil {
		return SessionRuntimeSnapshot{}
	}
	e.sessionRuntimeMu.RLock()
	state := e.sessionRuntimeStateLocked()
	snapshot := cloneSessionRuntimeSnapshot(state.SessionRuntimeSnapshot)
	e.sessionRuntimeMu.RUnlock()
	return snapshot
}

// PromptSections builds the prompt from the same immutable prompt builder and
// scratchpad selection that were published with the session runtime.
func (e *Engine) PromptSections() []prompt.Section {
	if e == nil {
		return nil
	}
	e.sessionRuntimeMu.RLock()
	state := e.sessionRuntimeStateLocked()
	builder := state.prompt
	e.sessionRuntimeMu.RUnlock()
	if builder == nil {
		return nil
	}
	return builder.Sections()
}

func (e *Engine) SystemPrompt() string {
	return prompt.JoinSections(e.PromptSections())
}

func (e *Engine) PromptSkillStatus() (skills.PromptBudgetReport, int, bool) {
	if e == nil {
		return skills.PromptBudgetReport{}, 0, false
	}
	e.sessionRuntimeMu.RLock()
	state := e.sessionRuntimeStateLocked()
	builder := state.prompt
	e.sessionRuntimeMu.RUnlock()
	if builder == nil || builder.Skills == nil {
		return skills.PromptBudgetReport{}, 0, false
	}
	return builder.Skills.PromptReport(), len(builder.Skills.Filtered()), true
}

// SessionStateStatus reads Goal and Notes from one runtime snapshot.
func (e *Engine) SessionStateStatus() (*GoalStatusSnapshot, *NotesSnapshot) {
	snapshot := e.SessionRuntimeSnapshot()
	var goal *GoalStatusSnapshot
	if snapshot.GoalState != nil {
		goal, _ = snapshot.GoalState.StatusSnapshot()
	}
	var notes *NotesSnapshot
	if snapshot.Notes != nil {
		notes, _ = snapshot.Notes.StatusSnapshot()
	}
	return goal, notes
}

func (e *Engine) currentSession() *session.Session {
	if e == nil {
		return nil
	}
	e.sessionRuntimeMu.RLock()
	state := e.sessionRuntimeStateLocked()
	sess := state.Session
	e.sessionRuntimeMu.RUnlock()
	return sess
}

func (e *Engine) currentPendingInputQueue() *PendingInputQueue {
	if e == nil {
		return nil
	}
	e.sessionRuntimeMu.Lock()
	state := e.sessionRuntimeStateLocked()
	queue := state.PendingInputQueue
	if queue == nil && state.Session != nil && state.Session.Dir != "" {
		queue = NewPendingInputQueue(state.Session.Dir, PendingInputQueueOptions{})
		e.PendingInputQueue = queue
		if e.sessionRuntime != nil {
			next := *e.sessionRuntime
			next.PendingInputQueue = queue
			e.sessionRuntime = &next
		}
	}
	e.sessionRuntimeMu.Unlock()
	return queue
}

func (e *Engine) currentNotesStore() *NotesStore {
	if e == nil {
		return nil
	}
	e.sessionRuntimeMu.Lock()
	state := e.sessionRuntimeStateLocked()
	store := state.Notes
	if store == nil && state.Session != nil && state.Session.Dir != "" {
		store = NewNotesStore(state.Session.Dir)
		e.Notes = store
		if e.sessionRuntime != nil {
			next := *e.sessionRuntime
			next.Notes = store
			e.sessionRuntime = &next
		}
	}
	e.sessionRuntimeMu.Unlock()
	return store
}

func (e *Engine) currentGoalStateStore() *GoalStateStore {
	if e == nil {
		return nil
	}
	e.sessionRuntimeMu.RLock()
	state := e.sessionRuntimeStateLocked()
	store := state.GoalState
	e.sessionRuntimeMu.RUnlock()
	return store
}

func (e *Engine) setNotesStore(store *NotesStore) {
	if e == nil {
		return
	}
	e.sessionRuntimeMu.Lock()
	e.Notes = store
	if e.sessionRuntime != nil {
		next := *e.sessionRuntime
		next.Notes = store
		e.sessionRuntime = &next
	}
	e.sessionRuntimeMu.Unlock()
}

func (e *Engine) sessionRuntimeStateLocked() sessionRuntimeState {
	if e.sessionRuntime != nil {
		return *e.sessionRuntime
	}
	scratchpadDir := ""
	if e.Prompt != nil {
		scratchpadDir = e.Prompt.ScratchpadDir
	}
	return sessionRuntimeState{
		SessionRuntimeSnapshot: SessionRuntimeSnapshot{
			Session:           e.Session,
			ScratchpadDir:     scratchpadDir,
			PendingInputQueue: e.PendingInputQueue,
			Notes:             e.Notes,
			GoalState:         e.GoalState,
			HookContext:       cloneHookRequest(e.HookContext),
		},
		prompt: e.Prompt,
	}
}

func buildSessionRuntimeState(current sessionRuntimeState, sess *session.Session) sessionRuntimeState {
	scratchpadDir := sess.ScratchpadDir()
	builder := clonePromptBuilder(current.prompt)
	if builder == nil {
		builder = &prompt.Builder{}
	}
	builder.ScratchpadDir = scratchpadDir

	queue := NewPendingInputQueue(sess.Dir, PendingInputQueueOptions{})
	if current.PendingInputQueue != nil && filepath.Dir(current.PendingInputQueue.path) == sess.Dir {
		queue = current.PendingInputQueue
	}
	notes := NewNotesStore(sess.Dir)
	if current.Notes != nil && current.Notes.SessionDir == sess.Dir {
		notes = current.Notes
	}
	goal := NewGoalStateStore(sess.Dir, GoalStateOptions{})
	if current.GoalState != nil && current.GoalState.SessionDir == sess.Dir {
		goal = current.GoalState
	}
	hookContext := cloneHookRequest(current.HookContext)
	hookContext.EventName = ""
	hookContext.SessionID = sess.ID
	hookContext.TurnID = ""
	hookContext.ConversationPath = filepath.Join(sess.Dir, "conversation.jsonl")
	hookContext.EventsPath = filepath.Join(sess.Dir, "events.jsonl")
	hookContext.ToolName = ""
	hookContext.ToolInput = nil
	hookContext.ToolResult = ""
	hookContext.UserInput = ""
	hookContext.CompactReason = ""
	hookContext.CompactAuto = false
	hookContext.GoalState = nil
	hookContext.Observer = nil

	return sessionRuntimeState{
		SessionRuntimeSnapshot: SessionRuntimeSnapshot{
			Session:           sess,
			ScratchpadDir:     scratchpadDir,
			PendingInputQueue: queue,
			Notes:             notes,
			GoalState:         goal,
			HookContext:       hookContext,
		},
		prompt: builder,
	}
}

func (e *Engine) publishSessionRuntimeLocked(next sessionRuntimeState) {
	published := next
	published.SessionRuntimeSnapshot = cloneSessionRuntimeSnapshot(next.SessionRuntimeSnapshot)
	e.sessionRuntime = &published

	// Keep the compatibility fields aligned for constructors and existing
	// tests. Production readers use SessionRuntimeSnapshot and helpers above.
	e.Session = next.Session
	e.Prompt = next.prompt
	e.PendingInputQueue = next.PendingInputQueue
	e.Notes = next.Notes
	e.GoalState = next.GoalState
	e.HookContext = cloneHookRequest(next.HookContext)
}

func cloneSessionRuntimeSnapshot(snapshot SessionRuntimeSnapshot) SessionRuntimeSnapshot {
	snapshot.HookContext = cloneHookRequest(snapshot.HookContext)
	return snapshot
}

func clonePromptBuilder(builder *prompt.Builder) *prompt.Builder {
	if builder == nil {
		return nil
	}
	cloned := *builder
	cloned.AgentsMDDirs = append([]string(nil), builder.AgentsMDDirs...)
	cloned.Shell.Args = append([]string(nil), builder.Shell.Args...)
	return &cloned
}

func cloneHookRequest(req hooks.Request) hooks.Request {
	req.WorkspaceRoots = append([]string(nil), req.WorkspaceRoots...)
	if req.ToolInput != nil {
		req.ToolInput = cloneMap(req.ToolInput)
	}
	req.GoalState = append([]byte(nil), req.GoalState...)
	return req
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
