package runtime

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/provenance"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

var ErrSessionRuntimeBusy = errors.New("runtime: session runtime is busy")

// SessionRuntimeSnapshot is one coherent view of the session-scoped runtime
// dependencies. Replacement publishes a new bundle instead of mutating it;
// lifecycle owners keep old session resources alive for their readers.
type SessionRuntimeSnapshot struct {
	Session           *session.Session
	ScratchpadDir     string
	PendingInputQueue *PendingInputQueue
	Modules           *runtimemodule.Set
	Tools             *tools.Registry
}

type SessionRuntimeReplacement struct {
	Modules *runtimemodule.Set
	Tools   *tools.Registry
}

// SessionRuntimeCheckpoint captures an already-published runtime bundle and
// its in-memory provenance state for rollback by the lifecycle owner.
type SessionRuntimeCheckpoint struct {
	owner                       *Engine
	state                       sessionRuntimeState
	provenanceTracker           *provenance.Tracker
	pendingPolicyRuntimeContext []llm.Message
}

func (c SessionRuntimeCheckpoint) Snapshot() SessionRuntimeSnapshot {
	return cloneSessionRuntimeSnapshot(c.state.SessionRuntimeSnapshot)
}

type sessionRuntimeState struct {
	SessionRuntimeSnapshot
	prompt *prompt.Builder
}

// ReplaceSessionRuntime builds and publishes every session-scoped dependency
// under one synchronization boundary. It serializes with turns and compaction,
// and refuses to move an active reservation or in-memory pending input.
func (e *Engine) ReplaceSessionRuntime(sess *session.Session) error {
	return e.ReplaceSessionRuntimeBundle(sess, SessionRuntimeReplacement{})
}

// ReplaceSessionRuntimeBundle publishes Session state and its sealed Module
// set/tool catalog under the existing Engine replacement transaction.
func (e *Engine) ReplaceSessionRuntimeBundle(sess *session.Session, replacement SessionRuntimeReplacement) error {
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
	tracker, err := recoverSessionProvenance(sess.Dir)
	if err != nil {
		return fmt.Errorf("runtime: recover provider provenance: %w", err)
	}

	current := e.sessionRuntimeStateLocked()
	next := buildSessionRuntimeState(current, sess, replacement)
	e.publishSessionRuntimeLocked(next)
	pendingPolicyContext := tracker.PendingPolicyContext()
	e.policyRuntimeContextMu.Lock()
	e.provenanceTracker = tracker
	e.pendingPolicyRuntimeContext = pendingPolicyContext
	e.policyRuntimeContextMu.Unlock()
	return nil
}

func recoverSessionProvenance(dir string) (*provenance.Tracker, error) {
	tracker := provenance.NewTracker()
	var replayErr error
	if err := session.ReplayEvents(dir, func(event events.Event) {
		if replayErr == nil {
			replayErr = tracker.ReplayEvent(event)
		}
	}); err != nil {
		return nil, err
	}
	if replayErr != nil {
		return nil, replayErr
	}
	return tracker, nil
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

// CaptureSessionRuntimeCheckpoint returns the current in-memory runtime state
// without retaining any external locks. A checkpoint belongs to this Engine.
func (e *Engine) CaptureSessionRuntimeCheckpoint() SessionRuntimeCheckpoint {
	if e == nil {
		return SessionRuntimeCheckpoint{}
	}
	e.mu.Lock()
	e.sessionRuntimeMu.RLock()
	e.pendingMu.Lock()
	e.policyRuntimeContextMu.Lock()
	checkpoint := SessionRuntimeCheckpoint{
		owner:                       e,
		state:                       e.sessionRuntimeStateLocked(),
		provenanceTracker:           e.provenanceTracker,
		pendingPolicyRuntimeContext: append([]llm.Message(nil), e.pendingPolicyRuntimeContext...),
	}
	e.policyRuntimeContextMu.Unlock()
	e.pendingMu.Unlock()
	e.sessionRuntimeMu.RUnlock()
	e.mu.Unlock()
	return checkpoint
}

// RestoreSessionRuntimeCheckpoint republishes a previously captured bundle
// without replaying its journal. Lifecycle rollback uses the exact state that
// was known-good before replacement.
func (e *Engine) RestoreSessionRuntimeCheckpoint(checkpoint SessionRuntimeCheckpoint) error {
	if e == nil || checkpoint.owner != e || checkpoint.state.Session == nil {
		return errors.New("runtime: valid session runtime checkpoint is required")
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
	e.publishSessionRuntimeLocked(checkpoint.state)
	e.policyRuntimeContextMu.Lock()
	e.provenanceTracker = checkpoint.provenanceTracker
	e.pendingPolicyRuntimeContext = append([]llm.Message(nil), checkpoint.pendingPolicyRuntimeContext...)
	e.policyRuntimeContextMu.Unlock()
	return nil
}

// PromptSections builds the prompt from the same immutable prompt builder and
// scratchpad selection that were published with the session runtime.
func (e *Engine) PromptSections() []prompt.Section {
	sections, _ := e.PromptSectionsWithError()
	return sections
}

func (e *Engine) PromptSectionsWithError() ([]prompt.Section, error) {
	if e == nil {
		return nil, nil
	}
	e.sessionRuntimeMu.RLock()
	state := e.sessionRuntimeStateLocked()
	builder := state.prompt
	e.sessionRuntimeMu.RUnlock()
	if builder == nil {
		return nil, nil
	}
	return builder.SectionsWithError()
}

func (e *Engine) SystemPrompt() string {
	system, _ := e.SystemPromptWithError()
	return system
}

func (e *Engine) SystemPromptWithError() (string, error) {
	sections, err := e.PromptSectionsWithError()
	if err != nil {
		return "", err
	}
	return prompt.JoinSections(sections), nil
}

// SessionStateStatus reads Goal and Notes from the active Session Modules.
func (e *Engine) SessionStateStatus() (*GoalStatusSnapshot, *NotesSnapshot) {
	snapshot := e.SessionRuntimeSnapshot()
	return SessionStateStatusFromModules(snapshot.Modules)
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

func (e *Engine) sessionRuntimeStateLocked() sessionRuntimeState {
	if e.sessionRuntime != nil {
		return *e.sessionRuntime
	}
	scratchpadDir := ""
	if e.Session != nil {
		scratchpadDir = e.Session.ScratchpadDir()
	}
	return sessionRuntimeState{
		SessionRuntimeSnapshot: SessionRuntimeSnapshot{
			Session:           e.Session,
			ScratchpadDir:     scratchpadDir,
			PendingInputQueue: e.PendingInputQueue,
			Tools:             e.Tools,
		},
		prompt: e.Prompt,
	}
}

func buildSessionRuntimeState(current sessionRuntimeState, sess *session.Session, replacement SessionRuntimeReplacement) sessionRuntimeState {
	scratchpadDir := sess.ScratchpadDir()
	builder := clonePromptBuilder(current.prompt)
	if builder == nil {
		builder = &prompt.Builder{}
	}

	queue := NewPendingInputQueue(sess.Dir, PendingInputQueueOptions{})
	if current.PendingInputQueue != nil && filepath.Dir(current.PendingInputQueue.path) == sess.Dir {
		queue = current.PendingInputQueue
	}
	modules := current.Modules
	if replacement.Modules != nil {
		modules = replacement.Modules
	}
	toolRegistry := current.Tools
	if replacement.Tools != nil {
		toolRegistry = replacement.Tools
	}

	return sessionRuntimeState{
		SessionRuntimeSnapshot: SessionRuntimeSnapshot{
			Session:           sess,
			ScratchpadDir:     scratchpadDir,
			PendingInputQueue: queue,
			Modules:           modules,
			Tools:             toolRegistry,
		},
		prompt: builder,
	}
}

func (e *Engine) publishSessionRuntimeLocked(next sessionRuntimeState) {
	published := next
	published.SessionRuntimeSnapshot = cloneSessionRuntimeSnapshot(next.SessionRuntimeSnapshot)
	e.sessionRuntime = &published

	// Keep the generic compatibility fields aligned for constructors and
	// existing tests. Feature state belongs to Session Modules.
	e.Session = next.Session
	e.Prompt = next.prompt
	e.PendingInputQueue = next.PendingInputQueue
	if next.Tools != nil {
		e.Tools = next.Tools
	}
}

func cloneSessionRuntimeSnapshot(snapshot SessionRuntimeSnapshot) SessionRuntimeSnapshot {
	return snapshot
}

func clonePromptBuilder(builder *prompt.Builder) *prompt.Builder {
	if builder == nil {
		return nil
	}
	cloned := *builder
	return &cloned
}
