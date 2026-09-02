package runtime

import (
	"errors"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/provenance"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/tools"
)

var ErrThreadRuntimeBusy = errors.New("runtime: thread runtime is busy")

// ThreadRuntimeSnapshot is one coherent view of the thread-scoped runtime
// dependencies. Replacement publishes a new bundle instead of mutating it;
// lifecycle owners keep old thread resources alive for their readers.
type ThreadRuntimeSnapshot struct {
	Thread            *thread.Thread
	ScratchpadDir     string
	PendingInputQueue *PendingInputQueue
	Modules           *runtimemodule.Set
	Tools             *tools.Registry
}

type ThreadRuntimeReplacement struct {
	Modules *runtimemodule.Set
	Tools   *tools.Registry
}

// ThreadRuntimeCheckpoint captures an already-published runtime bundle and
// its in-memory provenance state for rollback by the lifecycle owner.
type ThreadRuntimeCheckpoint struct {
	owner                       *Engine
	state                       threadRuntimeState
	provenanceTracker           *provenance.Tracker
	pendingPolicyRuntimeContext []llm.Message
}

func (c ThreadRuntimeCheckpoint) Snapshot() ThreadRuntimeSnapshot {
	return cloneThreadRuntimeSnapshot(c.state.ThreadRuntimeSnapshot)
}

type threadRuntimeState struct {
	ThreadRuntimeSnapshot
	prompt *prompt.Builder
}

// ReplaceThreadRuntime builds and publishes every thread-scoped dependency
// under one synchronization boundary. It serializes with turns and compaction,
// and refuses to move an active reservation or in-memory pending input.
func (e *Engine) ReplaceThreadRuntime(threadState *thread.Thread) error {
	return e.ReplaceThreadRuntimeBundle(threadState, ThreadRuntimeReplacement{})
}

// ReplaceThreadRuntimeBundle publishes Thread state and its sealed Module
// set/tool catalog under the existing Engine replacement transaction.
func (e *Engine) ReplaceThreadRuntimeBundle(threadState *thread.Thread, replacement ThreadRuntimeReplacement) error {
	if e == nil || threadState == nil || strings.TrimSpace(threadState.Dir) == "" {
		return errors.New("runtime: replacement thread is required")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.threadRuntimeMu.Lock()
	defer e.threadRuntimeMu.Unlock()
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()

	if e.activeTurnID != "" || len(e.pendingInput) > 0 {
		return ErrThreadRuntimeBusy
	}
	tracker, err := recoverThreadProvenance(threadState.Dir)
	if err != nil {
		return fmt.Errorf("runtime: recover provider provenance: %w", err)
	}

	current := e.threadRuntimeStateLocked()
	next := buildThreadRuntimeState(current, threadState, replacement)
	e.publishThreadRuntimeLocked(next)
	pendingPolicyContext := tracker.PendingPolicyContext()
	e.policyRuntimeContextMu.Lock()
	e.provenanceTracker = tracker
	e.pendingPolicyRuntimeContext = pendingPolicyContext
	e.policyRuntimeContextMu.Unlock()
	return nil
}

func recoverThreadProvenance(dir string) (*provenance.Tracker, error) {
	tracker := provenance.NewTracker()
	var replayErr error
	if err := thread.ReplayEvents(dir, func(event events.Event) {
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

// ThreadRuntimeSnapshot returns one coherent copy of the current
// thread-scoped runtime bundle.
func (e *Engine) ThreadRuntimeSnapshot() ThreadRuntimeSnapshot {
	if e == nil {
		return ThreadRuntimeSnapshot{}
	}
	e.threadRuntimeMu.RLock()
	state := e.threadRuntimeStateLocked()
	snapshot := cloneThreadRuntimeSnapshot(state.ThreadRuntimeSnapshot)
	e.threadRuntimeMu.RUnlock()
	return snapshot
}

// CaptureThreadRuntimeCheckpoint returns the current in-memory runtime state
// without retaining any external locks. A checkpoint belongs to this Engine.
func (e *Engine) CaptureThreadRuntimeCheckpoint() ThreadRuntimeCheckpoint {
	if e == nil {
		return ThreadRuntimeCheckpoint{}
	}
	e.mu.Lock()
	e.threadRuntimeMu.RLock()
	e.pendingMu.Lock()
	e.policyRuntimeContextMu.Lock()
	checkpoint := ThreadRuntimeCheckpoint{
		owner:                       e,
		state:                       e.threadRuntimeStateLocked(),
		provenanceTracker:           e.provenanceTracker,
		pendingPolicyRuntimeContext: append([]llm.Message(nil), e.pendingPolicyRuntimeContext...),
	}
	e.policyRuntimeContextMu.Unlock()
	e.pendingMu.Unlock()
	e.threadRuntimeMu.RUnlock()
	e.mu.Unlock()
	return checkpoint
}

// RestoreThreadRuntimeCheckpoint republishes a previously captured bundle
// without replaying its journal. Lifecycle rollback uses the exact state that
// was known-good before replacement.
func (e *Engine) RestoreThreadRuntimeCheckpoint(checkpoint ThreadRuntimeCheckpoint) error {
	if e == nil || checkpoint.owner != e || checkpoint.state.Thread == nil {
		return errors.New("runtime: valid thread runtime checkpoint is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.threadRuntimeMu.Lock()
	defer e.threadRuntimeMu.Unlock()
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if e.activeTurnID != "" || len(e.pendingInput) > 0 {
		return ErrThreadRuntimeBusy
	}
	e.publishThreadRuntimeLocked(checkpoint.state)
	e.policyRuntimeContextMu.Lock()
	e.provenanceTracker = checkpoint.provenanceTracker
	e.pendingPolicyRuntimeContext = append([]llm.Message(nil), checkpoint.pendingPolicyRuntimeContext...)
	e.policyRuntimeContextMu.Unlock()
	return nil
}

// PromptSections builds the prompt from the same immutable prompt builder and
// scratchpad selection that were published with the thread runtime.
func (e *Engine) PromptSections() []prompt.Section {
	sections, _ := e.PromptSectionsWithError()
	return sections
}

func (e *Engine) PromptSectionsWithError() ([]prompt.Section, error) {
	if e == nil {
		return nil, nil
	}
	e.threadRuntimeMu.RLock()
	state := e.threadRuntimeStateLocked()
	builder := state.prompt
	e.threadRuntimeMu.RUnlock()
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

// ThreadStateStatus reads Goal and Notes from the active Thread Modules.
func (e *Engine) ThreadStateStatus() (*workmem.GoalStatusSnapshot, *workmem.NotesSnapshot) {
	snapshot := e.ThreadRuntimeSnapshot()
	return ThreadStateStatusFromModules(snapshot.Modules)
}

func (e *Engine) currentThread() *thread.Thread {
	if e == nil {
		return nil
	}
	e.threadRuntimeMu.RLock()
	state := e.threadRuntimeStateLocked()
	threadState := state.Thread
	e.threadRuntimeMu.RUnlock()
	return threadState
}

func (e *Engine) currentPendingInputQueue() *PendingInputQueue {
	if e == nil {
		return nil
	}
	e.threadRuntimeMu.Lock()
	state := e.threadRuntimeStateLocked()
	queue := state.PendingInputQueue
	if queue == nil && state.Thread != nil && state.Thread.Dir != "" {
		queue = NewPendingInputQueue(state.Thread.Dir, PendingInputQueueOptions{Thread: state.Thread})
		e.PendingInputQueue = queue
		if e.threadRuntime != nil {
			next := *e.threadRuntime
			next.PendingInputQueue = queue
			e.threadRuntime = &next
		}
	}
	e.threadRuntimeMu.Unlock()
	return queue
}

func (e *Engine) threadRuntimeStateLocked() threadRuntimeState {
	if e.threadRuntime != nil {
		return *e.threadRuntime
	}
	scratchpadDir := ""
	if e.Thread != nil {
		scratchpadDir = e.Thread.ScratchpadDir()
	}
	return threadRuntimeState{
		ThreadRuntimeSnapshot: ThreadRuntimeSnapshot{
			Thread:            e.Thread,
			ScratchpadDir:     scratchpadDir,
			PendingInputQueue: e.PendingInputQueue,
			Tools:             e.Tools,
		},
		prompt: e.Prompt,
	}
}

func buildThreadRuntimeState(current threadRuntimeState, threadState *thread.Thread, replacement ThreadRuntimeReplacement) threadRuntimeState {
	scratchpadDir := threadState.ScratchpadDir()
	builder := clonePromptBuilder(current.prompt)
	if builder == nil {
		builder = &prompt.Builder{}
	}

	queue := NewPendingInputQueue(threadState.Dir, PendingInputQueueOptions{Thread: threadState})
	if current.PendingInputQueue != nil && current.PendingInputQueue.thread == threadState {
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

	return threadRuntimeState{
		ThreadRuntimeSnapshot: ThreadRuntimeSnapshot{
			Thread:            threadState,
			ScratchpadDir:     scratchpadDir,
			PendingInputQueue: queue,
			Modules:           modules,
			Tools:             toolRegistry,
		},
		prompt: builder,
	}
}

func (e *Engine) publishThreadRuntimeLocked(next threadRuntimeState) {
	published := next
	published.ThreadRuntimeSnapshot = cloneThreadRuntimeSnapshot(next.ThreadRuntimeSnapshot)
	e.threadRuntime = &published

	// Keep the bootstrap fields aligned with the published thread bundle.
	// Feature state belongs to Thread Modules.
	e.Thread = next.Thread
	e.Prompt = next.prompt
	e.PendingInputQueue = next.PendingInputQueue
	if next.Tools != nil {
		e.Tools = next.Tools
	}
}

func cloneThreadRuntimeSnapshot(snapshot ThreadRuntimeSnapshot) ThreadRuntimeSnapshot {
	return snapshot
}

func clonePromptBuilder(builder *prompt.Builder) *prompt.Builder {
	if builder == nil {
		return nil
	}
	cloned := *builder
	return &cloned
}
