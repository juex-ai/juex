// Package runtime implements the synchronous turn loop that drives user or
// system-originated input through repeated LLM calls and tool dispatches until
// the model stops requesting tools.
//
// Behaviour highlights:
//
//   - System prompt sections are rebuilt every turn so dynamic guidance and
//     Session state changes propagate immediately.
//   - Context projection externalizes oversized user inputs and tool results
//     before provider submission while preserving recoverable session history.
//   - Automatic and manual compaction keep active context bounded with compact
//     summary markers and retained recent tail messages.
//   - independent tool_use blocks within one LLM response run in parallel;
//     model-owned session-state tools run in provider order, and all results
//     are reattached to history in the original order.
//   - Pending input lets transports queue user or critical external messages
//     while preserving assistant tool-use / user tool-result adjacency.
//   - Turns run until the model finishes, the parent context is cancelled, or a
//     provider/tool/context error stops progress.
//   - Every state transition emits an event with a stable TurnID so downstream
//     consumers can stitch a transcript.
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/chunkedwrite"
	"github.com/juex-ai/juex/internal/errorclass"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/provenance"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/toolevents"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	DefaultMaxPendingInput  = 16
	DefaultPendingInputTTL  = 15 * time.Minute
	DefaultExternalEventTTL = 24 * time.Hour
	maxToolErrorOutput      = 32 * 1024
	maxShellPolicyContent   = 128 * 1024
	maxShellEventDiagnostic = 4 * 1024
)

type Engine struct {
	Provider          llm.Provider
	SummaryProvider   llm.Provider
	SummaryProvenance provenance.SafeProvider
	// SummaryContextWindow is the dedicated compaction summary model's context
	// window. Zero keeps the serving candidate window as the fallback.
	SummaryContextWindow int
	ModelCandidates      []ModelCandidate
	ModelHealth          *llm.ModelHealth
	Tools                *tools.Registry
	RuntimeModules       *runtimemodule.Set
	RuntimeContext       runtimemodule.RuntimeContext
	Bus                  *events.Bus
	// Session, Prompt, and PendingInputQueue seed the first session runtime
	// bundle during bootstrap. Concurrent production code must use the
	// synchronized session-runtime methods after publication instead of reading
	// or replacing these fields directly.
	Session *session.Session
	Prompt  *prompt.Builder
	// WorkDir is the workspace root. Runtime state may live outside it, so
	// workspace-relative tools and artifacts must not infer it from Session.
	WorkDir string
	// ArtifactDir is the current Agent's managed Artifact root.
	ArtifactDir string
	// MaxPendingInputs caps user or external event messages that can be
	// queued while a turn is active. When omitted, DefaultMaxPendingInput is
	// used. A full queue rejects new input instead of silently dropping it.
	MaxPendingInputs int
	// PendingInputQueue persists pending input records in the session
	// directory. When omitted, the engine creates a session-local queue on
	// first use.
	PendingInputQueue *PendingInputQueue
	// ShowBuiltinPolicyTraces includes built-in runtime gates in UI-only policy
	// trace messages. Module-provided policy traces are always shown.
	ShowBuiltinPolicyTraces bool
	// NotifyModelChanges adds provider-visible notices when the serving model
	// degrades or recovers. Fallback events and model attribution are unchanged.
	NotifyModelChanges bool
	// PendingInputTTL controls generated-id user steer records.
	PendingInputTTL time.Duration
	// ExternalEventTTL controls MCP/external event records when the caller
	// does not pass a TTL.
	ExternalEventTTL time.Duration
	// ContextWindow is the provider context window in tokens. When omitted,
	// the engine uses DefaultContextWindowTokens.
	ContextWindow int
	// MaxOutputTokens optionally caps provider-visible output for normal
	// turns. A zero value leaves the provider default in place.
	MaxOutputTokens int
	Compaction      CompactionPolicy
	ToolOutput      ToolOutputPolicy

	// mu serializes turns for one Engine. MCP notifications can arrive while
	// a user turn is running, and both paths append to the same session
	// history; queuing them preserves the provider-facing transcript order.
	mu sync.Mutex

	sessionRuntimeMu sync.RWMutex
	sessionRuntime   *sessionRuntimeState

	pendingLifecycleMu sync.Mutex
	pendingMu          sync.Mutex
	activeTurnID       string
	// terminalPublishing keeps later admissions behind a durably committed
	// terminal event while its synchronous subscribers run outside pendingLifecycleMu.
	terminalPublishing string
	pendingInput       []queuedPendingInput
	// pendingEventAnnouncing keeps queue mutations available while ensuring
	// their events cannot overtake a queue transition already being announced.
	pendingEventAnnouncing bool
	pendingDeferredEvents  []events.Event

	activeOperationMu         sync.Mutex
	activeOperationCancel     context.CancelCauseFunc
	activeOperationGeneration uint64

	policyRuntimeContextMu      sync.Mutex
	pendingPolicyRuntimeContext []llm.Message
	provenanceTracker           *provenance.Tracker

	autoCompactFailures int
	toolFailures        *toolFailureLedger

	tokenCalibrationMu sync.RWMutex
	tokenCalibration   tokenEstimateCalibration
}

var (
	ErrNoActiveTurn          = errors.New("runtime: no active turn accepting pending input")
	ErrActiveTurnExists      = errors.New("runtime: active turn already accepting pending input")
	ErrPendingInputQueueFull = errors.New("runtime: pending input queue full")
	ErrPendingInputExpired   = errors.New("runtime: pending input expired")
	ErrPendingInputHandled   = errors.New("runtime: pending input is no longer replayable")
)

type PendingInputStatus struct {
	TurnID           string `json:"turn_id,omitempty"`
	PendingCount     int    `json:"pending_count"`
	MaxPendingInputs int    `json:"max_pending_inputs"`
}

type queuedPendingInput struct {
	RecordID string
	Message  llm.Message
	Origin   PendingInputOrigin
}

// Turn drives one user input to completion. The returned string is the final
// assistant text response (concatenated text blocks). Returns an error when
// cancellation or provider/tool/context failure stops the turn.
func (e *Engine) Turn(ctx context.Context, userInput string) (string, error) {
	return e.TurnMessage(ctx, llm.ClassifyUserMessage(llm.TextMessage(llm.RoleUser, userInput)))
}

func (e *Engine) ReserveTurnID(turnID string) error {
	if e == nil {
		return ErrNoActiveTurn
	}
	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()
	return e.reserveTurnID(turnID, TurnAdmittedPayload{})
}

// AdmitTurnMessage durably accepts one main Turn input before establishing the
// active execution boundary. Repeating admission for the same Turn returns the
// already accepted message with its stable Framework-owned identity.
func (e *Engine) AdmitTurnMessage(turnID string, userMsg llm.Message) (llm.Message, error) {
	if e == nil {
		return llm.Message{}, ErrNoActiveTurn
	}
	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()
	record, err := e.admitTurnMessage(turnID, userMsg)
	return record.Message, err
}

func (e *Engine) admitTurnMessage(turnID string, userMsg llm.Message) (PendingInputRecord, error) {
	if e == nil {
		return PendingInputRecord{}, ErrNoActiveTurn
	}
	if turnID == "" {
		return PendingInputRecord{}, errors.New("runtime: empty turn id")
	}
	queue := e.currentPendingInputQueue()
	if queue == nil {
		return PendingInputRecord{}, errors.New("runtime: pending input queue unavailable")
	}
	userMsg = llm.ClassifyUserMessage(userMsg)

	e.pendingMu.Lock()
	if e.terminalPublishing != "" {
		e.pendingMu.Unlock()
		return PendingInputRecord{}, ErrActiveTurnExists
	}
	if e.activeTurnID != "" && e.activeTurnID != turnID {
		e.pendingMu.Unlock()
		return PendingInputRecord{}, ErrActiveTurnExists
	}
	alreadyActive := e.activeTurnID == turnID
	record, err := queue.StageTurnInput(turnID, userMsg, alreadyActive)
	if err != nil {
		e.pendingMu.Unlock()
		return PendingInputRecord{}, fmt.Errorf("persist accepted turn input: %w", err)
	}
	admitted := e.activeTurnID == ""
	createdAdmissionIntent := record.Origin == PendingInputOriginTurn && record.State == PendingInputStateAccepting && record.TurnID == turnID
	e.activeTurnID = turnID
	if admitted {
		e.pendingEventAnnouncing = true
	}
	e.pendingMu.Unlock()

	if admitted {
		defer e.flushPendingEvents()
		if err := e.emit(events.Event{Type: TurnAdmittedType, TurnID: turnID, Payload: TurnAdmittedPayload{MessageID: record.MessageID}}); err != nil {
			var dropErr error
			if createdAdmissionIntent {
				dropErr = queue.MarkDropped([]string{record.ID})
			}
			commitErr := fmt.Errorf("commit turn admission: %w", err)
			if dropErr != nil {
				commitErr = errors.Join(commitErr, fmt.Errorf("drop rejected turn admission: %w", dropErr))
			}
			return PendingInputRecord{}, e.preservePendingInputBeforeFailedAdmissionReleaseLocked(turnID, commitErr)
		}
		if err := queue.CommitTurnInput(record.ID, turnID); err != nil {
			var dropErr error
			if createdAdmissionIntent {
				dropErr = queue.MarkDropped([]string{record.ID})
			}
			commitErr := fmt.Errorf("persist committed turn admission: %w", err)
			if dropErr != nil {
				commitErr = errors.Join(commitErr, fmt.Errorf("drop uncommitted turn admission: %w", dropErr))
			}
			commitErr = e.preservePendingInputBeforeFailedAdmissionReleaseLocked(turnID, commitErr)
			return PendingInputRecord{}, e.failTurn(turnID, commitErr)
		}
		record.Origin = PendingInputOriginTurn
		record.State = PendingInputStateAdmitted
		record.TurnID = turnID
		record.ExpiresAt = time.Time{}
	}
	return record, nil
}

func (e *Engine) preservePendingInputBeforeFailedAdmissionReleaseLocked(turnID string, admissionErr error) error {
	if preserveErr := e.preservePendingInputAfterFailureLocked(turnID); preserveErr != nil {
		admissionErr = errors.Join(admissionErr, fmt.Errorf("preserve pending input after turn admission failure: %w", preserveErr))
	}
	e.finishActiveTurn(turnID)
	return admissionErr
}

func (e *Engine) ReserveCompactionTurnID(turnID string) error {
	if e == nil {
		return ErrNoActiveTurn
	}
	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()
	return e.reserveTurnID(turnID, TurnAdmittedPayload{
		Operation: TurnAdmissionOperationCompact,
	})
}

func (e *Engine) reserveTurnID(turnID string, payload TurnAdmittedPayload) error {
	if e == nil {
		return ErrNoActiveTurn
	}
	if turnID == "" {
		return fmt.Errorf("runtime: empty turn id")
	}
	e.pendingMu.Lock()
	if e.terminalPublishing != "" {
		e.pendingMu.Unlock()
		return ErrActiveTurnExists
	}
	if e.activeTurnID != "" && e.activeTurnID != turnID {
		e.pendingMu.Unlock()
		return ErrActiveTurnExists
	}
	admitted := e.activeTurnID == ""
	e.activeTurnID = turnID
	if admitted {
		e.pendingEventAnnouncing = true
	}
	e.pendingMu.Unlock()
	if admitted {
		defer e.flushPendingEvents()
		if err := e.emit(events.Event{Type: TurnAdmittedType, TurnID: turnID, Payload: payload}); err != nil {
			e.finishActiveTurn(turnID)
			return fmt.Errorf("commit turn admission: %w", err)
		}
	}
	return nil
}

func (e *Engine) EnqueuePendingInput(ctx context.Context, userInput string) (PendingInputStatus, error) {
	return e.EnqueuePendingMessage(ctx, llm.ClassifyUserMessage(llm.TextMessage(llm.RoleUser, userInput)))
}

func (e *Engine) EnqueuePendingMessage(ctx context.Context, userMsg llm.Message) (PendingInputStatus, error) {
	return e.EnqueuePendingMessageWithOptions(ctx, userMsg, PendingInputOptions{})
}

// PersistPendingMessageWithOptions durably accepts a message independently of
// whether a turn is active. Callers can then attach the returned record to the
// active turn or start a new turn without losing the accepted input in between.
func (e *Engine) PersistPendingMessageWithOptions(ctx context.Context, userMsg llm.Message, opts PendingInputOptions) (PendingInputRecord, error) {
	userMsg = llm.ClassifyUserMessage(userMsg)
	if e == nil {
		return PendingInputRecord{}, ErrNoActiveTurn
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PendingInputRecord{}, err
	}
	queue := e.currentPendingInputQueue()
	if queue == nil {
		return PendingInputRecord{}, fmt.Errorf("pending input queue unavailable")
	}
	opts = e.defaultPendingInputOptions(userMsg, opts)
	e.pendingMu.Lock()
	turnID := e.activeTurnID
	e.pendingMu.Unlock()
	return queue.Enqueue(userMsg, opts, turnID)
}

// DropPersistedPendingMessage prevents an accepted external input from being
// replayed after its owner has determined that delivery is no longer valid.
func (e *Engine) DropPersistedPendingMessage(id string) error {
	if e == nil || id == "" {
		return nil
	}
	queue := e.currentPendingInputQueue()
	if queue == nil {
		return nil
	}
	return queue.MarkDropped([]string{id})
}

// PersistedPendingMessage returns the latest durable state for id. Delivery
// owners use this after a failed turn to distinguish a message rejected before
// transcript append from one already consumed by the runtime.
func (e *Engine) PersistedPendingMessage(id string) (PendingInputRecord, bool, error) {
	if e == nil || id == "" {
		return PendingInputRecord{}, false, nil
	}
	queue := e.currentPendingInputQueue()
	if queue == nil {
		return PendingInputRecord{}, false, nil
	}
	records, err := queue.Records()
	if err != nil {
		return PendingInputRecord{}, false, err
	}
	record, ok := records[id]
	return record, ok, nil
}

// EnqueuePersistedPendingMessage attaches one already-durable record to the
// current in-memory turn queue. Queue-full is intentionally event-free because
// the durable record remains accepted and its owner may retry admission.
func (e *Engine) EnqueuePersistedPendingMessage(ctx context.Context, record PendingInputRecord) (PendingInputStatus, error) {
	if e == nil {
		return PendingInputStatus{}, ErrNoActiveTurn
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PendingInputStatus{}, err
	}
	queue := e.currentPendingInputQueue()
	if queue == nil {
		return PendingInputStatus{}, fmt.Errorf("pending input queue unavailable")
	}
	records, err := queue.Records()
	if err != nil {
		return PendingInputStatus{}, err
	}
	if current, ok := records[record.ID]; ok {
		record = current
	}
	if record.Expired(queue.now().UTC()) {
		if err := queue.MarkExpired([]string{record.ID}); err != nil {
			return PendingInputStatus{}, err
		}
		return PendingInputStatus{}, ErrPendingInputExpired
	}
	if !isReplayablePendingState(record.State) {
		return PendingInputStatus{}, ErrPendingInputHandled
	}
	max := e.effectiveMaxPendingInputs()
	e.pendingMu.Lock()
	turnID := e.activeTurnID
	status := PendingInputStatus{TurnID: turnID, PendingCount: len(e.pendingInput), MaxPendingInputs: max}
	if turnID == "" {
		e.pendingMu.Unlock()
		return status, ErrNoActiveTurn
	}
	if e.hasPendingRecordLocked(record.ID) {
		e.pendingMu.Unlock()
		return status, nil
	}
	if len(e.pendingInput) >= max {
		e.pendingMu.Unlock()
		return status, ErrPendingInputQueueFull
	}
	e.pendingInput = append(e.pendingInput, queuedPendingInput{RecordID: record.ID, Message: record.Message, Origin: record.Origin})
	status.PendingCount = len(e.pendingInput)
	event := events.Event{Type: "pending_input.queued", TurnID: turnID, Payload: PendingInputQueuedPayload{
		Input:            record.Message.FirstText(),
		Kind:             record.Message.Kind,
		MessageID:        record.Message.ID,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
	}}
	deferred := e.deferPendingEventLocked(event)
	e.pendingMu.Unlock()
	if !deferred {
		_ = e.emit(event)
	}
	return status, nil
}

func (e *Engine) EnqueuePendingMessageWithOptions(ctx context.Context, userMsg llm.Message, opts PendingInputOptions) (PendingInputStatus, error) {
	if e == nil {
		return PendingInputStatus{}, ErrNoActiveTurn
	}
	if e.pendingEventAnnouncementActive() {
		status, _, err := e.enqueuePendingMessageWithOptions(ctx, userMsg, opts)
		return status, err
	}
	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()
	status, _, err := e.enqueuePendingMessageWithOptions(ctx, userMsg, opts)
	return status, err
}

func (e *Engine) pendingEventAnnouncementActive() bool {
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	return e.pendingEventAnnouncing
}

func (e *Engine) enqueuePendingMessageWithOptions(ctx context.Context, userMsg llm.Message, opts PendingInputOptions) (PendingInputStatus, PendingInputRecord, error) {
	userMsg = llm.ClassifyUserMessage(userMsg)
	if e == nil {
		return PendingInputStatus{}, PendingInputRecord{}, ErrNoActiveTurn
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PendingInputStatus{}, PendingInputRecord{}, err
	}
	max := e.effectiveMaxPendingInputs()
	queue := e.currentPendingInputQueue()
	e.pendingMu.Lock()
	turnID := e.activeTurnID
	status := PendingInputStatus{TurnID: turnID, PendingCount: len(e.pendingInput), MaxPendingInputs: max}
	if turnID == "" {
		e.pendingMu.Unlock()
		return status, PendingInputRecord{}, ErrNoActiveTurn
	}
	if len(e.pendingInput) >= max {
		event := events.Event{Type: "pending_input.rejected", TurnID: turnID, Payload: PendingInputRejectedPayload{
			Input:            userMsg.FirstText(),
			Kind:             userMsg.Kind,
			PendingCount:     status.PendingCount,
			MaxPendingInputs: status.MaxPendingInputs,
			Reason:           "queue_full",
		}}
		deferred := e.deferPendingEventLocked(event)
		e.pendingMu.Unlock()
		if !deferred {
			_ = e.emit(event)
		}
		return status, PendingInputRecord{}, ErrPendingInputQueueFull
	}
	recordID := ""
	var acceptedRecord PendingInputRecord
	if queue != nil {
		opts = e.defaultPendingInputOptions(userMsg, opts)
		record, err := queue.Enqueue(userMsg, opts, turnID)
		if err != nil {
			e.pendingMu.Unlock()
			return status, PendingInputRecord{}, err
		}
		acceptedRecord = record
		recordID = record.ID
		userMsg = record.Message
		if !isReplayablePendingState(record.State) {
			status.PendingCount = len(e.pendingInput)
			e.pendingMu.Unlock()
			return status, acceptedRecord, nil
		}
		if e.hasPendingRecordLocked(record.ID) {
			status.PendingCount = len(e.pendingInput)
			e.pendingMu.Unlock()
			return status, acceptedRecord, nil
		}
	}
	e.pendingInput = append(e.pendingInput, queuedPendingInput{RecordID: recordID, Message: userMsg, Origin: PendingInputOriginQueued})
	status.PendingCount = len(e.pendingInput)
	event := events.Event{Type: "pending_input.queued", TurnID: turnID, Payload: PendingInputQueuedPayload{
		Input:            userMsg.FirstText(),
		Kind:             userMsg.Kind,
		MessageID:        userMsg.ID,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
	}}
	deferred := e.deferPendingEventLocked(event)
	e.pendingMu.Unlock()
	if !deferred {
		_ = e.emit(event)
	}
	return status, acceptedRecord, nil
}

func (e *Engine) PendingInputStatus() PendingInputStatus {
	if e == nil {
		return PendingInputStatus{}
	}
	max := e.effectiveMaxPendingInputs()
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	return PendingInputStatus{
		TurnID:           e.activeTurnID,
		PendingCount:     len(e.pendingInput),
		MaxPendingInputs: max,
	}
}

// PromotePendingInputTurn turns the first queued input from a reserved
// non-provider phase into the user message for a real provider turn.
func (e *Engine) PromotePendingInputTurn(currentTurnID, nextTurnID string) (llm.Message, PendingInputStatus, bool, error) {
	if e == nil {
		return llm.Message{}, PendingInputStatus{}, false, nil
	}
	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()
	item, status, promoted, err := e.promotePendingInputTurn(currentTurnID, nextTurnID)
	return item.Message, status, promoted, err
}

func (e *Engine) promotePendingInputTurn(currentTurnID, nextTurnID string) (queuedPendingInput, PendingInputStatus, bool, error) {
	if e == nil || nextTurnID == "" {
		return queuedPendingInput{}, PendingInputStatus{}, false, nil
	}
	max := e.effectiveMaxPendingInputs()
	queue := e.currentPendingInputQueue()
	e.pendingMu.Lock()
	if e.terminalPublishing != "" {
		status := PendingInputStatus{
			TurnID:           e.terminalPublishing,
			PendingCount:     len(e.pendingInput),
			MaxPendingInputs: max,
		}
		e.pendingMu.Unlock()
		return queuedPendingInput{}, status, false, ErrActiveTurnExists
	}
	if e.activeTurnID != currentTurnID || len(e.pendingInput) == 0 {
		if e.activeTurnID == currentTurnID {
			e.activeTurnID = ""
		}
		status := PendingInputStatus{
			TurnID:           e.activeTurnID,
			PendingCount:     len(e.pendingInput),
			MaxPendingInputs: max,
		}
		e.pendingMu.Unlock()
		return queuedPendingInput{}, status, false, nil
	}
	item := e.pendingInput[0]
	e.activeTurnID = nextTurnID
	e.pendingEventAnnouncing = true
	e.pendingMu.Unlock()

	if err := e.emit(events.Event{Type: TurnAdmittedType, TurnID: nextTurnID, Payload: TurnAdmittedPayload{MessageID: item.Message.ID}}); err != nil {
		admissionErr := fmt.Errorf("commit promoted turn admission: %w", err)
		if preserveErr := e.preservePendingInputAfterFailedPromotionLocked(nextTurnID); preserveErr != nil {
			admissionErr = errors.Join(admissionErr, fmt.Errorf("preserve pending input after turn promotion failure: %w", preserveErr))
		}
		e.finishActiveTurn(nextTurnID)
		e.flushPendingEvents()
		status := e.PendingInputStatus()
		return queuedPendingInput{}, status, false, admissionErr
	}
	if item.RecordID != "" && queue != nil {
		if err := queue.PromoteToTurnInput([]string{item.RecordID}, nextTurnID); err != nil {
			promotionErr := fmt.Errorf("mark promoted pending input admitted: %w", err)
			if preserveErr := e.preservePendingInputAfterFailedPromotionLocked(nextTurnID); preserveErr != nil {
				promotionErr = errors.Join(promotionErr, fmt.Errorf("preserve pending input after turn promotion failure: %w", preserveErr))
			}
			e.finishActiveTurn(nextTurnID)
			promotionErr = e.failTurn(nextTurnID, promotionErr)
			e.flushPendingEvents()
			return queuedPendingInput{}, e.PendingInputStatus(), false, promotionErr
		}
	}

	e.pendingMu.Lock()
	e.pendingInput[0] = queuedPendingInput{}
	e.pendingInput = e.pendingInput[1:]
	status := PendingInputStatus{
		TurnID:           nextTurnID,
		PendingCount:     len(e.pendingInput),
		MaxPendingInputs: max,
	}
	e.pendingMu.Unlock()
	_ = e.emit(events.Event{Type: PendingInputPromotedType, TurnID: nextTurnID, Payload: PendingInputPromotedPayload{
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
	}})
	if item.RecordID != "" {
		e.notifyPendingInputsAdmitted(context.Background(), nextTurnID, []string{item.RecordID})
	}
	e.flushPendingEvents()
	return item, status, true, nil
}

func (e *Engine) preservePendingInputAfterFailedPromotionLocked(turnID string) error {
	// The promotion trigger must stay replayable after its durable transition
	// fails; only inputs accepted during the announcement are terminally kept.
	repairedTranscript := false
	for {
		e.pendingMu.Lock()
		if e.activeTurnID != turnID || len(e.pendingInput) <= 1 {
			e.pendingMu.Unlock()
			return nil
		}
		pending := append([]queuedPendingInput(nil), e.pendingInput[1:]...)
		e.pendingInput = e.pendingInput[:1]
		e.pendingMu.Unlock()

		if !repairedTranscript {
			if err := e.repairTranscriptLocked(turnID, "turn_promotion_failure_pending_input"); err != nil {
				e.restorePendingInputAfterPromotionTrigger(pending)
				return err
			}
			repairedTranscript = true
		}
		if err := e.commitPendingInputBatchLocked(context.Background(), turnID, pending); err != nil {
			e.restorePendingInputAfterPromotionTrigger(pending)
			return err
		}
	}
}

func (e *Engine) restorePendingInputAfterPromotionTrigger(pending []queuedPendingInput) {
	if len(pending) == 0 {
		return
	}
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if len(e.pendingInput) == 0 {
		e.pendingInput = append([]queuedPendingInput(nil), pending...)
		return
	}
	restored := make([]queuedPendingInput, 0, len(e.pendingInput)+len(pending))
	restored = append(restored, e.pendingInput[0])
	restored = append(restored, pending...)
	restored = append(restored, e.pendingInput[1:]...)
	e.pendingInput = restored
}

// TurnMessage drives one already-constructed user message to completion.
// It exists for system-originated user turns, such as MCP channel events,
// that need app metadata while still reaching the provider as normal text.
func (e *Engine) TurnMessage(ctx context.Context, userMsg llm.Message) (out string, err error) {
	return e.TurnMessageWithID(ctx, userMsg, "")
}

func (e *Engine) TurnMessageWithID(ctx context.Context, userMsg llm.Message, turnID string) (out string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if turnID == "" {
		result, receiveErr := e.ReceivePendingInput(ctx, PendingInputRequest{Message: userMsg, RequireStart: true})
		if receiveErr != nil {
			return "", receiveErr
		}
		if result.Disposition != PendingInputStarted {
			return "", fmt.Errorf("runtime: synchronous input was not started: %s", result.Disposition)
		}
		turnID = result.TurnID
		userMsg = result.Message
	} else {
		userMsg, err = e.AdmitTurnMessage(turnID, userMsg)
		if err != nil {
			return "", err
		}
	}
	ctx, _, finishOperation := e.beginActiveOperation(ctx)
	previousFailures := e.toolFailures
	e.toolFailures = newToolFailureLedger(e.WorkDir)
	lifecycle := turnLifecycle{
		engine:  e,
		turnID:  turnID,
		userMsg: userMsg,
		start:   time.Now(),
	}
	defer func() {
		e.toolFailures = previousFailures
		if !lifecycle.activeClosed {
			e.finishActiveTurn(turnID)
		}
	}()
	defer finishOperation()
	var result turnLifecycleResult
	result, err = lifecycle.runLocked(ctx)
	if err != nil {
		err = cancellation.NormalizeErrorWithContext(ctx, err)
		err = e.failActiveTurnLocked(turnID, err, lifecycle.pendingLifecycleHeld)
		lifecycle.pendingLifecycleHeld = false
		return "", err
	}
	return result.output, nil
}

// CancelActiveTurn cancels the currently executing turn or compaction once.
func (e *Engine) CancelActiveTurn(cause error) bool {
	if e == nil {
		return false
	}
	if cause == nil {
		cause = cancellation.ErrUserCancelled
	}
	e.activeOperationMu.Lock()
	cancel := e.activeOperationCancel
	if cancel == nil {
		e.activeOperationMu.Unlock()
		return false
	}
	e.activeOperationCancel = nil
	cancel(cause)
	e.activeOperationMu.Unlock()
	return true
}

func (e *Engine) beginActiveOperation(parent context.Context) (context.Context, uint64, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	e.activeOperationMu.Lock()
	e.activeOperationGeneration++
	generation := e.activeOperationGeneration
	e.activeOperationCancel = cancel
	e.activeOperationMu.Unlock()
	finish := func() {
		e.activeOperationMu.Lock()
		if e.activeOperationGeneration == generation {
			e.activeOperationCancel = nil
		}
		e.activeOperationMu.Unlock()
		cancel(nil)
	}
	return ctx, generation, finish
}

type preparedTurnContext struct {
	promptSections []prompt.Section
	systemPrompt   string
	tools          []llm.ToolSpec
	policy         compactionPolicy
	userMessage    llm.Message
}

type providerTurnRequest struct {
	iter                 int
	history              []llm.Message
	estimatedInputTokens int
	policyContext        []llm.Message
	epochID              string
	requestDigest        string
}

type ModelCandidate struct {
	Ref             string
	Provider        llm.Provider
	Provenance      provenance.SafeProvider
	ContextWindow   int
	MaxOutputTokens int
}

type providerTurnResult struct {
	response  llm.Response
	request   providerTurnRequest
	candidate ModelCandidate
	notice    *llm.Message
}

type recordedProviderResponse struct {
	finalText     string
	stopReason    llm.StopReason
	toolCalls     []llm.Block
	iter          int
	messageID     string
	contextWindow int
}

func (e *Engine) prepareTurnContextLocked(ctx context.Context, turnID string, userMsg llm.Message) (preparedTurnContext, error) {
	original := userMsg
	policyMessage, err := runtimemodule.ApplyTurnInputPolicies(ctx, runtimemodule.TurnInputRequest{
		Runtime:  e.policyRuntimeContext(),
		Session:  e.policySessionContext(),
		TurnID:   turnID,
		Message:  userMsg,
		Observer: e.policyObserver(turnID),
	}, e.policySets()...)
	if err != nil {
		original.PolicyBlocked = true
		if persistErr := e.recordTurnStartLocked(turnID, original); persistErr != nil {
			return preparedTurnContext{}, errors.Join(err, fmt.Errorf("persist accepted user input after policy failure: %w", persistErr))
		}
		return preparedTurnContext{}, err
	}
	userMsg = policyMessage

	prepared := preparedTurnContext{
		tools:  e.Tools.Specs(),
		policy: effectiveCompactionPolicy(e.Compaction, e.ContextWindow),
	}
	projectedUserMsg, projection, err := e.projectMessageLocked(userMsg, prepared.policy)
	if err != nil {
		return preparedTurnContext{}, e.persistAcceptedInputAfterPreparationFailureLocked(turnID, userMsg, err)
	}
	prepared.userMessage = projectedUserMsg
	if err := e.emitProjectionApplied(turnID, projection); err != nil {
		projectionErr := fmt.Errorf("commit user input projection: %w", err)
		return preparedTurnContext{}, e.persistAcceptedInputAfterPreparationFailureLocked(turnID, userMsg, projectionErr)
	}

	promptSections, err := e.PromptSectionsWithError()
	if err != nil {
		promptErr := fmt.Errorf("runtime: build prompt context: %w", err)
		if persistErr := e.recordTurnStartLocked(turnID, prepared.userMessage); persistErr != nil {
			return preparedTurnContext{}, errors.Join(promptErr, fmt.Errorf("persist accepted user input after prompt failure: %w", persistErr))
		}
		return preparedTurnContext{}, promptErr
	}
	prepared.promptSections = promptSections
	prepared.systemPrompt = prompt.JoinSections(prepared.promptSections)

	if err := e.maybeCompact(ctx, turnID, prepared.systemPrompt, prepared.tools, prepared.userMessage); err != nil {
		if !canContinueAfterAutoCompactError(ctx, prepared.userMessage) {
			return preparedTurnContext{}, e.persistAcceptedInputAfterPreparationFailureLocked(turnID, prepared.userMessage, err)
		}
	}
	return prepared, nil
}

func (e *Engine) persistAcceptedInputAfterPreparationFailureLocked(turnID string, message llm.Message, cause error) error {
	if persistErr := e.recordTurnStartLocked(turnID, message); persistErr != nil {
		return errors.Join(cause, fmt.Errorf("persist accepted user input after preparation failure: %w", persistErr))
	}
	return cause
}

func (e *Engine) recordTurnStartLocked(turnID string, userMsg llm.Message) error {
	persisted, err := e.currentSession().AppendAssigned(userMsg)
	if err != nil {
		return fmt.Errorf("session append user: %w", err)
	}
	if err := e.markPendingInputMessageProcessed(persisted); err != nil {
		return fmt.Errorf("mark pending input user processed: %w", err)
	}
	if err := e.emit(events.Event{Type: "turn.started", TurnID: turnID, Payload: TurnStartedPayload{
		Input:     persisted.FirstText(),
		Kind:      persisted.Kind,
		MessageID: persisted.ID,
	}}); err != nil {
		return fmt.Errorf("commit turn start: %w", err)
	}
	return nil
}

func (e *Engine) repairTranscriptLocked(turnID, reason string) error {
	repairs, err := e.currentSession().RepairTranscript(reason)
	if err != nil {
		return fmt.Errorf("session repair transcript: %w", err)
	}
	if len(repairs) > 0 {
		for _, event := range session.ProjectTranscriptRepairEvents(turnID, reason, repairs) {
			if err := e.emit(event); err != nil {
				if event.Type != "transcript.repaired" {
					return fmt.Errorf("commit unknown tool outcome: %w", err)
				}
				return fmt.Errorf("commit transcript repair: %w", err)
			}
		}
	}
	return nil
}

// RecoverTranscript repairs an attached Session while routing recovery facts
// through the configured Event Bus. The caller must hold the Session lifetime
// lock, as App does for the full attachment lifetime.
func (e *Engine) RecoverTranscript(reason string) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.repairTranscriptLocked("", reason)
}

func (e *Engine) prepareProviderRequestLocked(ctx context.Context, turnID string, iter int, prepared preparedTurnContext) (providerTurnRequest, error) {
	policyContext := e.pendingPolicyRuntimeContextSnapshot()
	active, err := e.activeContextLockedWithPolicyContextError(ctx, policyContext)
	if err != nil {
		return providerTurnRequest{}, fmt.Errorf("runtime: build provider context: %w", err)
	}
	requestHistory := active.Messages
	return providerTurnRequest{
		iter:          iter,
		history:       requestHistory,
		policyContext: policyContext,
	}, nil
}

func (e *Engine) requestProviderTurnLocked(ctx context.Context, turnID string, prepared preparedTurnContext, base providerTurnRequest) (providerTurnResult, error) {
	candidates := e.effectiveModelCandidatesLocked()
	if len(candidates) == 0 {
		return providerTurnResult{}, fmt.Errorf("llm: no model candidates configured")
	}
	health := e.ModelHealth
	if health == nil {
		health = llm.NewModelHealth(llm.ModelHealthOptions{})
		e.ModelHealth = health
	}
	refs := make([]string, len(candidates))
	for i := range candidates {
		refs[i] = candidates[i].Ref
	}
	attempted := map[string]struct{}{}
	previousModel := previousAssistantModel(e.currentSession().History)
	var failures []modelAttemptFailure
	var skipped []llm.ModelHealthSkip
	var pending *modelFallbackTransition
	attempt := 0

	for {
		selection, ok := health.Acquire(refs, attempted)
		skipped = append(skipped, selection.Skipped...)
		for _, skip := range selection.Skipped {
			attempted[skip.Ref] = struct{}{}
		}
		if !ok {
			if pending != nil {
				e.emitModelFallback(turnID, *pending, "")
			}
			for _, skipped := range selection.Skipped {
				e.emitModelFallback(turnID, modelFallbackTransition{
					from:     skipped.Ref,
					reason:   skipped.Reason,
					cooldown: skipped.CooldownRemaining,
				}, "")
			}
			return providerTurnResult{request: base}, modelChainError(failures, skipped)
		}
		candidate := candidates[selection.Index]
		attempt++
		attempted[candidate.Ref] = struct{}{}
		var notice *llm.Message
		if e.NotifyModelChanges {
			notice = modelSwitchNotice(previousModel, candidate.Ref, refs, selection, pending, failures, skipped)
			if notice != nil {
				notice.ID = providerNoticeMessageID(turnID, base.iter, candidate.Ref, *notice)
			}
		}
		request, err := e.prepareCandidateRequestLocked(ctx, turnID, prepared, base, candidate, notice, selection.Index > 0)
		if err != nil {
			health.Complete(selection.Ticket, llm.ModelHealthNeutral, "")
			return providerTurnResult{request: request}, err
		}
		base.policyContext = request.policyContext
		active, contextErr := e.activeContextLockedWithPolicyContextError(ctx, base.policyContext)
		if contextErr != nil {
			health.Complete(selection.Ticket, llm.ModelHealthNeutral, "")
			return providerTurnResult{request: base}, fmt.Errorf("runtime: build provider context: %w", contextErr)
		}
		base.history = active.Messages
		if pending != nil {
			e.emitModelFallback(turnID, *pending, candidate.Ref)
		}
		for _, skipped := range selection.Skipped {
			e.emitModelFallback(turnID, modelFallbackTransition{
				from:     skipped.Ref,
				reason:   skipped.Reason,
				cooldown: skipped.CooldownRemaining,
			}, candidate.Ref)
		}
		cachePolicy := e.cachePolicyLocked()
		epoch, err := e.checkpointProviderRequestLocked(turnID, prepared, request, candidate, cachePolicy, attempt)
		if err != nil {
			health.Complete(selection.Ticket, llm.ModelHealthNeutral, "")
			return providerTurnResult{request: request}, err
		}
		request.epochID = epoch.EpochID
		request.requestDigest = epoch.RequestDigest
		if err := e.emit(events.Event{Type: "llm.requested", TurnID: turnID, Payload: LLMRequestedPayload{
			Iter:          base.iter,
			Purpose:       "turn",
			HistoryLen:    len(request.history),
			ToolCount:     len(prepared.tools),
			Model:         candidate.Ref,
			EpochID:       request.epochID,
			RequestDigest: request.requestDigest,
		}}); err != nil {
			health.Complete(selection.Ticket, llm.ModelHealthNeutral, "")
			return providerTurnResult{request: request}, fmt.Errorf("commit provider request: %w", err)
		}

		resp, err := llm.CompleteWithOptions(ctx, candidate.Provider, prepared.systemPrompt, request.history, prepared.tools, llm.CompleteOptions{
			Purpose:         "turn",
			MaxOutputTokens: candidateMaxOutputTokens(candidate, e.MaxOutputTokens),
			CachePolicy:     cachePolicy,
			RetryObserver:   e.providerRetryObserverForEpochLocked(turnID, "turn", &request.iter, request.epochID, request.requestDigest),
			OnDelta: func(delta llm.StreamDelta) {
				_ = e.emit(events.Event{Type: "llm.output_delta", TurnID: turnID, Transient: true, Payload: LLMOutputDeltaPayload{
					Iter:  request.iter,
					Model: candidate.Ref,
					Kind:  delta.Kind,
					Index: delta.Index,
					Text:  delta.Text,
				}})
			},
		})
		if err == nil {
			if contextErr := cancellation.ContextError(ctx); contextErr != nil {
				health.Complete(selection.Ticket, llm.ModelHealthSuccess, "")
				discardErr := fmt.Errorf("provider response discarded: %w", contextErr)
				if emitErr := e.emitProviderTurnErrored(turnID, request, candidate.Ref, discardErr); emitErr != nil {
					return providerTurnResult{request: request}, fmt.Errorf("commit discarded provider response: %w", emitErr)
				}
				return providerTurnResult{request: request}, context.Canceled
			}
			health.Complete(selection.Ticket, llm.ModelHealthSuccess, "")
			return providerTurnResult{response: resp, request: request, candidate: candidate, notice: notice}, nil
		}
		if emitErr := e.emitProviderTurnErrored(turnID, request, candidate.Ref, err); emitErr != nil {
			health.Complete(selection.Ticket, llm.ModelHealthNeutral, "")
			return providerTurnResult{request: request}, &modelRequestError{
				err:           errors.Join(err, fmt.Errorf("commit provider error: %w", emitErr)),
				contextWindow: candidateContextWindow(candidate, e.ContextWindow),
			}
		}
		reason, eligible := llm.ClassifyFallbackError(err)
		if !eligible {
			health.Complete(selection.Ticket, llm.ModelHealthNeutral, "")
			return providerTurnResult{request: request}, &modelRequestError{err: err, contextWindow: candidateContextWindow(candidate, e.ContextWindow)}
		}
		if len(candidates) == 1 {
			health.Complete(selection.Ticket, llm.ModelHealthNeutral, "")
			return providerTurnResult{request: request}, &modelRequestError{err: err, contextWindow: candidateContextWindow(candidate, e.ContextWindow)}
		}
		transition := health.Complete(selection.Ticket, llm.ModelHealthEligibleFailure, string(reason))
		failures = append(failures, modelAttemptFailure{ref: candidate.Ref, err: err})
		pending = &modelFallbackTransition{
			from:     candidate.Ref,
			reason:   string(reason),
			cooldown: transition.Cooldown,
			probe:    selection.Ticket.Probe,
		}
		base.policyContext = e.pendingPolicyRuntimeContextSnapshot()
		active, contextErr = e.activeContextLockedWithPolicyContextError(ctx, base.policyContext)
		if contextErr != nil {
			return providerTurnResult{request: base}, fmt.Errorf("runtime: build provider context: %w", contextErr)
		}
		base.history = active.Messages
	}
}

func (e *Engine) emitProviderTurnErrored(turnID string, request providerTurnRequest, model string, cause error) error {
	return e.emit(events.Event{Type: "llm.errored", TurnID: turnID, Payload: LLMErroredPayload{
		Iter:          request.iter,
		Purpose:       "turn",
		Model:         model,
		Error:         cause.Error(),
		EpochID:       request.epochID,
		RequestDigest: request.requestDigest,
	}})
}

func (e *Engine) providerRetryObserverForEpochLocked(turnID, purpose string, iter *int, epochID, requestDigest string) func(llm.ProviderRetryDiagnostic) {
	var iterCopy *int
	if iter != nil {
		value := *iter
		iterCopy = &value
	}
	return func(d llm.ProviderRetryDiagnostic) {
		_ = e.emit(events.Event{Type: "llm.retry", TurnID: turnID, Payload: LLMRetryPayload{
			ProviderRetryDiagnostic: d,
			Purpose:                 purpose,
			Iter:                    iterCopy,
			EpochID:                 epochID,
			RequestDigest:           requestDigest,
		}})
	}
}

func (e *Engine) continueAfterProviderFailure(ctx context.Context, turnID string, request providerTurnRequest, err error) bool {
	if err == nil || cancellation.ContextError(ctx) != nil || !llm.IsRetryableProviderError(err) {
		return false
	}
	classification := errorclass.Classify(err)
	if classification.Kind != errorclass.KindError && classification.Kind != errorclass.KindTimeout {
		return false
	}
	e.pendingMu.Lock()
	canContinue := e.activeTurnID == turnID && len(e.pendingInput) > 0
	e.pendingMu.Unlock()
	if !canContinue {
		return false
	}
	provider := ""
	if e.Provider != nil {
		provider = e.Provider.Name()
	}
	e.providerRetryObserverForEpochLocked(turnID, "turn", &request.iter, request.epochID, request.requestDigest)(llm.ProviderRetryDiagnostic{
		Provider:    provider,
		Model:       provider,
		Operation:   "turn.pending_input",
		Attempt:     1,
		MaxAttempts: 2,
		RetryReason: "pending_input_after_provider_error",
		RawError:    err.Error(),
		WillRetry:   true,
	})
	return true
}

func (e *Engine) recordProviderResponseLocked(turnID string, prepared preparedTurnContext, result providerTurnResult) (recordedProviderResponse, error) {
	request := result.request
	resp := result.response
	msg := resp.Message
	if len(e.ModelCandidates) > 0 || msg.Model == "" {
		msg.Model = result.candidate.Ref
	}
	msg.Blocks = prepareToolInputs(msg.Blocks, e.Tools)
	var contextUsage *llm.ContextUsage
	if !resp.Usage.IsZero() {
		snapshot := e.contextUsageSnapshot(msg.Model, candidateContextWindow(result.candidate, e.ContextWindow), resp.Usage, prepared.promptSections, prepared.tools, request.history)
		contextUsage = &snapshot
	}
	messages := make([]llm.Message, 0, 2)
	if result.notice != nil {
		messages = append(messages, *result.notice)
	}
	messages = append(messages, msg)
	sess := e.currentSession()
	persisted, err := sess.AppendBatchAssigned(messages)
	if err != nil {
		return recordedProviderResponse{}, fmt.Errorf("session append provider response: %w", err)
	}
	msg = persisted[len(persisted)-1]
	var notice *llm.Message
	if len(persisted) > 1 {
		notice = &persisted[0]
	}
	e.updateTokenEstimateCalibration(resp.Usage.InputTokens, request.estimatedInputTokens)
	totalUsage := sess.RecordResponseUsage(resp.Usage, contextUsage)

	// Enrich the responded event with the assistant's text + thinking +
	// tool calls so verbose UIs can render them without subscribing to
	// the conversation log. Bounded by what the LLM returned in this
	// single turn, so payload size is reasonable.
	payload := LLMRespondedPayload{
		Iter:          request.iter,
		StopReason:    resp.StopReason,
		Usage:         resp.Usage,
		TokenUsage:    totalUsage,
		Blocks:        msg.Blocks,
		Text:          responseText(msg),
		Thinking:      responseThinking(msg),
		ToolCalls:     responseToolCalls(msg, request.iter),
		Model:         msg.Model,
		ContextUsage:  contextUsage,
		Notice:        notice,
		MessageID:     msg.ID,
		EpochID:       request.epochID,
		RequestDigest: request.requestDigest,
	}
	if err := e.emit(events.Event{Type: "llm.responded", TurnID: turnID, Payload: payload}); err != nil {
		return recordedProviderResponse{}, fmt.Errorf("commit provider response: %w", err)
	}

	toolCalls := msg.ToolCalls()
	return recordedProviderResponse{
		finalText:     llm.FormatBlocksForTerminal(msg.Blocks),
		stopReason:    resp.StopReason,
		toolCalls:     toolCalls,
		iter:          request.iter,
		messageID:     msg.ID,
		contextWindow: candidateContextWindow(result.candidate, e.ContextWindow),
	}, nil
}

func (e *Engine) recordToolBatchLocked(ctx context.Context, turnID string, recorded recordedProviderResponse) error {
	if err := e.emit(events.Event{Type: TurnPhaseType, TurnID: turnID, Payload: TurnPhasePayload{Phase: TurnPhaseToolBatch}}); err != nil {
		return fmt.Errorf("commit tool batch phase: %w", err)
	}
	executions := make([]toolExecutionCall, len(recorded.toolCalls))
	for index, call := range recorded.toolCalls {
		executions[index] = toolExecutionCall{
			call:    call,
			payload: toolCallPayload(call, recorded.iter, index, recorded.messageID),
		}
		if err := e.emit(events.Event{Type: toolevents.RequestedType, TurnID: turnID, Payload: toolevents.Requested(executions[index].payload)}); err != nil {
			return fmt.Errorf("commit tool request: %w", err)
		}
	}
	toolResults := e.runToolCalls(ctx, turnID, executions)
	var fatalErr error
	for index := range toolResults {
		result := &toolResults[index]
		if result.FatalError != nil {
			fatalErr = errors.Join(fatalErr, result.FatalError)
		}
	}
	toolResults = e.normalizeGuidedToolFailureResults(toolResults)
	results := toolResultBlocks(toolResults)
	toolResultMsg := llm.Message{
		ID:     "msg-" + newID(),
		Role:   llm.RoleUser,
		Kind:   llm.MessageKindToolResult,
		Blocks: results,
	}
	contextWindow := recorded.contextWindow
	if contextWindow <= 0 {
		contextWindow = e.ContextWindow
	}
	policy := effectiveCompactionPolicy(e.Compaction, contextWindow)
	projectedToolResultMsg, projection, err := e.projectMessageLocked(toolResultMsg, policy)
	if err != nil {
		return errors.Join(fatalErr, err)
	}
	var outcomeErr error
	for index := range toolResults {
		result := &toolResults[index]
		if result.FatalError != nil || result.Block.Type != llm.BlockToolResult {
			continue
		}
		if emitErr := e.emitToolFinished(
			turnID,
			executions[index],
			projectedToolResultMsg.ID,
			projectedToolResultMsg.Blocks[index],
			result.EventObservation,
			result.Info,
		); emitErr != nil {
			outcomeErr = errors.Join(outcomeErr, fmt.Errorf("commit tool result: %w", emitErr))
		}
	}
	if fatalErr != nil || outcomeErr != nil {
		return errors.Join(fatalErr, outcomeErr)
	}
	if appendErr := e.currentSession().Append(projectedToolResultMsg); appendErr != nil {
		return fmt.Errorf("session append tool result: %w", appendErr)
	}
	e.recordToolFailureBatch(turnID, toolResults)
	if emitErr := e.emitProjectionApplied(turnID, projection); emitErr != nil {
		return fmt.Errorf("commit tool result projection: %w", emitErr)
	}
	return nil
}

func (e *Engine) recordTurnCompletionLocked(turnID string, start time.Time, lastText string) (events.Event, error) {
	event := events.Event{Type: "turn.completed", TurnID: turnID, Payload: TurnCompletedPayload{
		DurationMS: time.Since(start).Milliseconds(),
		OutputLen:  len(lastText),
		TokenUsage: e.currentSession().TokenUsageSnapshot(),
	}}
	if e.Bus == nil {
		return events.Normalize(event), nil
	}
	return e.Bus.Commit(event)
}

type toolCallResult struct {
	Call             llm.Block
	Block            llm.Block
	Observation      tools.Observation
	EventObservation tools.Observation
	Info             tools.CallInfo
	FatalError       error
}

type toolExecutionCall struct {
	call    llm.Block
	payload toolevents.ToolCallPayload
}

// runToolCalls executes one assistant tool-use batch concurrently while
// preserving provider-facing result order. Stateful tool groups are serialized
// so dependent reads, writes, and lifecycle changes observe provider order.
func (e *Engine) runToolCalls(ctx context.Context, turnID string, calls []toolExecutionCall) []toolCallResult {
	results := make([]toolCallResult, len(calls))
	type indexedToolCall struct {
		index int
		call  toolExecutionCall
	}
	var sessionStateCalls []indexedToolCall
	var wg sync.WaitGroup
	for i, tc := range calls {
		if e.isSerializedToolCall(tc.call.ToolName) {
			sessionStateCalls = append(sessionStateCalls, indexedToolCall{index: i, call: tc})
			continue
		}
		wg.Add(1)
		go func(idx int, call toolExecutionCall) {
			defer wg.Done()
			results[idx] = e.runToolCall(ctx, turnID, call)
		}(i, tc)
	}
	if len(sessionStateCalls) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, item := range sessionStateCalls {
				results[item.index] = e.runToolCall(ctx, turnID, item.call)
			}
		}()
	}
	wg.Wait()
	return results
}

func (e *Engine) isSerializedToolCall(name string) bool {
	if e == nil || e.Tools == nil {
		return false
	}
	tool, ok := e.Tools.Get(name)
	if !ok {
		return false
	}
	return tool.Group == tools.ToolGroupSessionState || tool.Group == tools.ToolGroupSideSession
}

func toolResultBlocks(results []toolCallResult) []llm.Block {
	blocks := make([]llm.Block, len(results))
	for i, result := range results {
		blocks[i] = result.Block
	}
	return blocks
}

func (e *Engine) normalizeGuidedToolFailureResults(results []toolCallResult) []toolCallResult {
	if e == nil || e.Tools == nil {
		return results
	}
	for i := range results {
		if !results[i].Block.IsError {
			continue
		}
		toolName := firstNonEmptyString(results[i].Block.ToolName, results[i].Observation.ToolName)
		tool, ok := e.Tools.Get(toolName)
		if !ok {
			continue
		}
		guideSkill, ok := tool.Group.GuideSkill()
		if !ok {
			continue
		}
		hint := fmt.Sprintf(
			`For workflows, constraints, and examples, load the full guide with skill_load("%s").`,
			guideSkill,
		)
		originalBlockContent := results[i].Block.Content
		results[i].Block.Content = appendGuidedToolFailureHint(originalBlockContent, hint)
		observationContent := results[i].Observation.Content
		if observationContent == "" {
			observationContent = originalBlockContent
		}
		results[i].Observation.Content = appendGuidedToolFailureHint(observationContent, hint)
	}
	return results
}

func appendGuidedToolFailureHint(content, hint string) string {
	if strings.Contains(content, hint) {
		return content
	}
	trimmed := strings.TrimRight(content, " \t\r\n")
	if trimmed == "" {
		return hint
	}
	return trimmed + "\n\n" + hint
}

func (e *Engine) runToolCall(ctx context.Context, turnID string, execution toolExecutionCall) toolCallResult {
	call := execution.call
	if err := e.emit(events.Event{Type: toolevents.RunningType, TurnID: turnID, Payload: toolevents.Running(execution.payload)}); err != nil {
		return toolCallResult{FatalError: fmt.Errorf("commit tool started: %w", err)}
	}
	prePolicy, err := runtimemodule.ApplyToolPoliciesWithInputCheckpoint(ctx, runtimemodule.ToolPolicyRequest{
		Runtime:  e.policyRuntimeContext(),
		Session:  e.policySessionContext(),
		TurnID:   turnID,
		Stage:    runtimemodule.ToolPolicyBeforeExecution,
		ToolName: call.ToolName,
		Input:    call.Input,
		Observer: e.policyObserver(turnID),
	}, func(input map[string]any) error {
		effectiveCall := execution.payload
		effectiveCall.Input = input
		return e.emit(events.Event{Type: toolevents.InputResolvedType, TurnID: turnID, Payload: toolevents.InputResolved(effectiveCall)})
	}, e.policySets()...)
	call.Input = prePolicy.Input
	if err != nil {
		if runtimemodule.IsPolicyCheckpointError(err) {
			return toolCallResult{FatalError: err}
		}
		return e.policyToolErrorResult(call, err, prePolicy.Context)
	}
	if prePolicy.Denied {
		return e.policyToolErrorResult(call, fmt.Errorf("tool policy denied %q%s", call.ToolName, policyReasonSuffix(prePolicy.Reason)), prePolicy.Context)
	}
	emitOutputDeltas := runtimemodule.AllowsLiveToolOutput(e.policySets()...)
	toolCtx := tools.WithToolCallEvents(ctx, tools.ToolCallEvents{
		Name:      call.ToolName,
		ToolUseID: call.ToolUseID,
		Iter:      execution.payload.Iter,
		CallIndex: execution.payload.CallIndex,
		MessageID: execution.payload.MessageID,
		Emit: func(delta tools.OutputDelta) {
			if emitOutputDeltas {
				_ = e.emit(toolevents.OutputDeltaEvent(turnID, execution.payload, delta))
			}
		},
	})
	out, info, err := e.Tools.CallWithInfo(toolCtx, call.ToolName, call.Input)
	err = cancellation.NormalizeError(err)
	block := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ToolUseID, ToolName: call.ToolName}
	var toolErr error
	if err != nil {
		if isShellStructuredResult(info.StructuredResult) {
			block.Content = boundedToolErrorContent(out, err)
		} else {
			block.Content = toolErrorContent(out, err)
		}
		block.IsError = true
		toolErr = err
	} else {
		block.Content = out
		if event, ok := chunkedwrite.EventFromStructured(info.StructuredResult); ok {
			block.ChunkedWrite = &event
		}
	}
	shellBaseContent := ""
	isShellCall := isShellStructuredResult(info.StructuredResult)
	if isShellCall {
		shellBaseContent = block.Content
	}
	postPolicy, postErr := runtimemodule.ApplyToolPolicies(ctx, runtimemodule.ToolPolicyRequest{
		Runtime:  e.policyRuntimeContext(),
		Session:  e.policySessionContext(),
		TurnID:   turnID,
		Stage:    runtimemodule.ToolPolicyAfterExecution,
		ToolName: call.ToolName,
		Input:    call.Input,
		Result: runtimemodule.ToolPolicyResult{
			Content: block.Content,
			IsError: block.IsError,
		},
		Observer: e.policyObserver(turnID),
	}, e.policySets()...)
	postErr = cancellation.NormalizeError(postErr)
	if postErr == nil && postPolicy.Denied {
		postErr = fmt.Errorf("tool policy denied %q after execution%s", call.ToolName, policyReasonSuffix(postPolicy.Reason))
	}
	if postPolicy.ResultTransformed {
		block.Content = postPolicy.Result.Content
		block.IsError = postPolicy.Result.IsError
		block.ChunkedWrite = nil
	}
	var fatalErr error
	if postErr != nil {
		if runtimemodule.IsPolicyCheckpointError(postErr) {
			fatalErr = postErr
		} else {
			if isShellStructuredResult(info.StructuredResult) {
				block.Content = appendShellRuntimeErrorContent(block.Content, postErr)
			} else {
				block.Content = toolErrorContent(block.Content, postErr)
			}
			block.IsError = true
			toolErr = postErr
		}
	} else if !postPolicy.ResultTransformed {
		block.Content = postPolicy.Result.Content
		block.IsError = postPolicy.Result.IsError
	}
	appendToolPolicyContext(&block, prePolicy.Context)
	appendToolPolicyContext(&block, postPolicy.Context)
	if isShellCall {
		block.Content = finalizedShellContent(shellBaseContent, block.Content)
	}
	if !block.IsError && !postPolicy.ResultTransformed {
		if media, ok := tools.MediaRefFromStructuredResult(info.StructuredResult); ok {
			block.Media = media
		}
	}
	observation := toolObservationForResult(call, block, info, toolErr, postPolicy.ResultTransformed)
	return toolCallResult{
		Call: call, Block: block, Observation: observation, EventObservation: observation,
		Info: info, FatalError: fatalErr,
	}
}

func toolObservationForResult(call llm.Block, block llm.Block, info tools.CallInfo, err error, resultTransformed bool) tools.Observation {
	if resultTransformed {
		return tools.NewObservation(tools.ObservationOptions{
			ToolName:  call.ToolName,
			ToolUseID: call.ToolUseID,
			Input:     call.Input,
			Content:   block.Content,
		})
	}
	var obs tools.Observation
	if info.Observation != nil {
		obs = info.Observation.Clone()
	}
	obs = obs.WithRuntimeContext(call.ToolName, call.ToolUseID, call.Input, block.Content, err)
	obs.TimedOut = obs.TimedOut || info.TimedOut
	if obs.ErrorKind == "" {
		obs.ErrorKind = info.ErrorKind
	}
	if info.RawCause != "" {
		obs.RawCause = info.RawCause
	}
	if obs.StructuredResult == nil {
		obs.StructuredResult = info.StructuredResult
	}
	if obs.Content == "" {
		obs.Content = block.Content
	}
	if obs.ExitCode == nil {
		obs.ExitCode = firstExitCode(nil, block.Content)
	}
	return obs
}

func (e *Engine) emitToolFinished(
	turnID string,
	execution toolExecutionCall,
	outcomeMessageID string,
	block llm.Block,
	observation tools.Observation,
	info tools.CallInfo,
) error {
	outcome := &toolevents.RecordedOutcome{MessageID: outcomeMessageID, Block: block}
	eventResult := observation.StructuredResult
	terminalContent := ""
	isShellResult := false
	switch shellResult := eventResult.(type) {
	case tools.ShellResult:
		isShellResult = true
		shellResult.Output = ""
		eventResult = shellResult
		terminalContent = block.Content
	case *tools.ShellResult:
		if shellResult != nil {
			isShellResult = true
			metadata := *shellResult
			metadata.Output = ""
			eventResult = &metadata
			terminalContent = block.Content
		}
	}
	if block.IsError {
		opts := toolevents.ErroredOptions{
			Error:          "tool errored",
			TimeoutSeconds: info.TimeoutSeconds,
		}
		if observation.Error != "" {
			opts.Error = observation.Error
		}
		opts.ErrorKind = observation.ErrorKind
		opts.RawCause = observation.RawCause
		if observation.Content != "" && !isShellResult {
			opts.Len = len(observation.Content)
			opts.Preview = truncate(observation.Content, 200)
		}
		if isShellResult {
			opts.Error = boundedRuntimeDiagnostic(opts.Error, maxShellEventDiagnostic)
			opts.RawCause = boundedRuntimeDiagnostic(opts.RawCause, maxShellEventDiagnostic)
			opts.Len = len(terminalContent)
		}
		if observation.TimedOut {
			opts.TimedOut = true
		}
		opts.ExitCode = cloneIntPtr(observation.ExitCode)
		opts.Result = eventResult
		opts.Media = block.Media
		opts.Outcome = outcome
		return e.emit(events.Event{Type: toolevents.ErroredType, TurnID: turnID, Payload: toolevents.Errored(execution.payload, opts)})
	}
	outputLen := len(observation.Content)
	preview := truncate(observation.Content, 200)
	if isShellResult {
		outputLen = len(terminalContent)
		preview = ""
	}
	payload := toolevents.Completed(execution.payload, info.TimeoutSeconds, outputLen, preview, eventResult)
	payload.Media = block.Media
	payload.Outcome = outcome
	return e.emit(events.Event{Type: toolevents.CompletedType, TurnID: turnID, Payload: payload})
}

func (e *Engine) policyToolErrorResult(call llm.Block, err error, contexts []runtimemodule.PolicyContext) toolCallResult {
	err = cancellation.NormalizeError(err)
	publicErr := errorclass.PublicMessage(err, errorclass.MessageOptions{})
	block := llm.Block{
		Type:      llm.BlockToolResult,
		ToolUseID: call.ToolUseID,
		ToolName:  call.ToolName,
		Content:   publicErr,
		IsError:   true,
	}
	appendToolPolicyContext(&block, contexts)
	observation := toolObservationForResult(call, block, tools.CallInfo{}, err, false)
	return toolCallResult{Call: call, Block: block, Observation: observation, EventObservation: observation}
}

func (e *Engine) effectiveMaxPendingInputs() int {
	if e.MaxPendingInputs > 0 {
		return e.MaxPendingInputs
	}
	return DefaultMaxPendingInput
}

func (e *Engine) effectivePendingInputTTL() time.Duration {
	if e.PendingInputTTL > 0 {
		return e.PendingInputTTL
	}
	return DefaultPendingInputTTL
}

func (e *Engine) effectiveExternalEventTTL() time.Duration {
	if e.ExternalEventTTL > 0 {
		return e.ExternalEventTTL
	}
	return DefaultExternalEventTTL
}

func (e *Engine) defaultPendingInputOptions(msg llm.Message, opts PendingInputOptions) PendingInputOptions {
	if opts.TTL <= 0 {
		if msg.Kind == llm.MessageKindMCPEvent {
			opts.TTL = e.effectiveExternalEventTTL()
		} else {
			opts.TTL = e.effectivePendingInputTTL()
		}
	}
	return opts
}

func (e *Engine) hasPendingRecordLocked(id string) bool {
	if id == "" {
		return false
	}
	for _, item := range e.pendingInput {
		if item.RecordID == id {
			return true
		}
	}
	return false
}

func sessionHasMessageID(sess *session.Session, id string) bool {
	return sess != nil && id != "" && sess.HasMessageID(id)
}

func pendingRecordIDs(pending []queuedPendingInput) []string {
	ids := make([]string, 0, len(pending))
	seen := map[string]struct{}{}
	for _, item := range pending {
		if item.RecordID == "" {
			continue
		}
		if _, ok := seen[item.RecordID]; ok {
			continue
		}
		seen[item.RecordID] = struct{}{}
		ids = append(ids, item.RecordID)
	}
	return ids
}

func isReplayablePendingState(state PendingInputState) bool {
	return state == PendingInputStatePending || state == PendingInputStateAdmitted
}

func (e *Engine) beginActiveTurn(turnID string) string {
	e.pendingMu.Lock()
	admitted := false
	if e.activeTurnID == "" {
		e.activeTurnID = turnID
		admitted = true
	}
	turnID = e.activeTurnID
	e.pendingMu.Unlock()
	if admitted {
		_ = e.emit(events.Event{Type: TurnAdmittedType, TurnID: turnID, Payload: TurnAdmittedPayload{}})
	}
	return turnID
}

func (e *Engine) restorePendingInput(ctx context.Context, turnID, skipMessageID string) error {
	if e == nil || turnID == "" {
		return nil
	}
	if err := cancellation.ContextError(ctx); err != nil {
		return err
	}
	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()
	queue := e.currentPendingInputQueue()
	sess := e.currentSession()
	if queue == nil {
		return nil
	}
	records, err := queue.Replayable(turnID, 0)
	if err != nil {
		return err
	}
	var alreadyProcessed []string
	queued := make([]queuedPendingInput, 0, len(records))
	markAlreadyProcessed := func(restoreErr error) error {
		if len(alreadyProcessed) == 0 {
			return restoreErr
		}
		if err := queue.MarkProcessed(alreadyProcessed); err != nil {
			markErr := fmt.Errorf("mark recovered input processed: %w", err)
			if restoreErr != nil {
				return errors.Join(restoreErr, markErr)
			}
			return markErr
		}
		return restoreErr
	}
	reachedCurrentInput := false
	var afterCurrent []queuedPendingInput
	flushQueued := func(tail []PendingInputRecord) error {
		if len(queued) == 0 {
			return nil
		}
		if err := cancellation.ContextError(ctx); err != nil {
			e.prependPendingInput(append(queued, queuedPendingInputsFromRecords(tail)...))
			queued = nil
			return err
		}
		batch := queued
		queued = nil
		if err := e.commitPendingInputBatchLocked(ctx, turnID, batch); err != nil {
			e.prependPendingInput(append(batch, queuedPendingInputsFromRecords(tail)...))
			return err
		}
		return nil
	}
	for i, record := range records {
		if skipMessageID != "" && record.MessageID == skipMessageID {
			if err := flushQueued(records[i:]); err != nil {
				return markAlreadyProcessed(err)
			}
			reachedCurrentInput = true
			continue
		}
		if reachedCurrentInput {
			if sessionHasMessageID(sess, record.MessageID) {
				alreadyProcessed = append(alreadyProcessed, record.ID)
				continue
			}
			afterCurrent = append(afterCurrent, queuedPendingInput{RecordID: record.ID, Message: record.Message, Origin: record.Origin})
			continue
		}
		if sessionHasMessageID(sess, record.MessageID) {
			alreadyProcessed = append(alreadyProcessed, record.ID)
			continue
		}
		if record.Origin == PendingInputOriginTurn {
			if err := flushQueued(records[i:]); err != nil {
				return markAlreadyProcessed(err)
			}
			if err := e.restoreAcceptedTurnInputLocked(ctx, turnID, record); err != nil {
				e.prependPendingInput(queuedPendingInputsFromRecords(records[i:]))
				return markAlreadyProcessed(err)
			}
			continue
		}
		queued = append(queued, queuedPendingInput{RecordID: record.ID, Message: record.Message, Origin: record.Origin})
	}
	if err := flushQueued(nil); err != nil {
		return markAlreadyProcessed(err)
	}
	if err := markAlreadyProcessed(nil); err != nil {
		e.prependPendingInput(afterCurrent)
		return err
	}
	e.mergeRecoveredPendingInputAfterCurrent(sess, afterCurrent)
	return nil
}

func (e *Engine) mergeRecoveredPendingInputAfterCurrent(sess *session.Session, recovered []queuedPendingInput) {
	if len(recovered) == 0 {
		return
	}
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()

	merged := make([]queuedPendingInput, 0, len(recovered)+len(e.pendingInput))
	mergedIDs := make(map[string]struct{}, len(recovered))
	for _, item := range recovered {
		merged = append(merged, item)
		mergedIDs[item.RecordID] = struct{}{}
	}
	for _, item := range e.pendingInput {
		if _, ok := mergedIDs[item.RecordID]; ok && item.RecordID != "" {
			continue
		}
		if sessionHasMessageID(sess, item.Message.ID) {
			continue
		}
		merged = append(merged, item)
	}
	e.pendingInput = merged
}

func queuedPendingInputsFromRecords(records []PendingInputRecord) []queuedPendingInput {
	pending := make([]queuedPendingInput, 0, len(records))
	for _, record := range records {
		pending = append(pending, queuedPendingInput{
			RecordID: record.ID,
			Message:  record.Message,
			Origin:   record.Origin,
		})
	}
	return pending
}

func (e *Engine) prependPendingInput(pending []queuedPendingInput) {
	if len(pending) == 0 {
		return
	}
	e.pendingMu.Lock()
	seenRecords := make(map[string]struct{}, len(pending)+len(e.pendingInput))
	seenMessages := make(map[string]struct{}, len(pending)+len(e.pendingInput))
	merged := make([]queuedPendingInput, 0, len(pending)+len(e.pendingInput))
	appendUnique := func(item queuedPendingInput) {
		if item.RecordID != "" {
			if _, exists := seenRecords[item.RecordID]; exists {
				return
			}
		}
		if item.Message.ID != "" {
			if _, exists := seenMessages[item.Message.ID]; exists {
				return
			}
		}
		merged = append(merged, item)
		if item.RecordID != "" {
			seenRecords[item.RecordID] = struct{}{}
		}
		if item.Message.ID != "" {
			seenMessages[item.Message.ID] = struct{}{}
		}
	}
	for _, item := range pending {
		appendUnique(item)
	}
	for _, item := range e.pendingInput {
		appendUnique(item)
	}
	e.pendingInput = merged
	e.pendingMu.Unlock()
}

func (e *Engine) restoreAcceptedTurnInputLocked(ctx context.Context, turnID string, record PendingInputRecord) error {
	original := record.Message
	policyMessage, err := runtimemodule.ApplyTurnInputPolicies(ctx, runtimemodule.TurnInputRequest{
		Runtime:  e.policyRuntimeContext(),
		Session:  e.policySessionContext(),
		TurnID:   turnID,
		Message:  original,
		Observer: e.policyObserver(turnID),
	}, e.policySets()...)
	if err != nil {
		original.PolicyBlocked = true
		if persistErr := e.appendRecoveredTurnInputLocked(original); persistErr != nil {
			return errors.Join(err, fmt.Errorf("persist recovered accepted input after policy failure: %w", persistErr))
		}
		return err
	}
	projected, projection, err := e.projectMessageLocked(policyMessage, effectiveCompactionPolicy(e.Compaction, e.ContextWindow))
	if err != nil {
		if persistErr := e.appendRecoveredTurnInputLocked(policyMessage); persistErr != nil {
			return errors.Join(err, fmt.Errorf("persist recovered accepted input after projection failure: %w", persistErr))
		}
		return err
	}
	if err := e.emitProjectionApplied(turnID, projection); err != nil {
		projectionErr := fmt.Errorf("commit recovered input projection: %w", err)
		if persistErr := e.appendRecoveredTurnInputLocked(policyMessage); persistErr != nil {
			return errors.Join(projectionErr, fmt.Errorf("persist recovered accepted input after projection failure: %w", persistErr))
		}
		return projectionErr
	}
	return e.appendRecoveredTurnInputLocked(projected)
}

func (e *Engine) appendRecoveredTurnInputLocked(message llm.Message) error {
	persisted, err := e.currentSession().AppendAssigned(message)
	if err != nil {
		return fmt.Errorf("session append recovered user: %w", err)
	}
	if err := e.markPendingInputMessageProcessed(persisted); err != nil {
		return fmt.Errorf("mark recovered user input processed: %w", err)
	}
	return nil
}

func (e *Engine) markPendingInputMessageProcessed(msg llm.Message) error {
	if e == nil || msg.ID == "" {
		return nil
	}
	queue := e.currentPendingInputQueue()
	if queue == nil {
		return nil
	}
	return queue.MarkMessageProcessed(msg.ID)
}

func (e *Engine) drainPendingInputLocked(ctx context.Context, turnID string) error {
	if err := cancellation.ContextError(ctx); err != nil {
		return err
	}
	e.pendingLifecycleMu.Lock()
	defer e.pendingLifecycleMu.Unlock()
	return e.drainPendingInputLifecycleLocked(ctx, turnID)
}

func (e *Engine) drainPendingInputLifecycleLocked(ctx context.Context, turnID string) error {
	e.pendingMu.Lock()
	pending := append([]queuedPendingInput(nil), e.pendingInput...)
	e.pendingInput = nil
	e.pendingMu.Unlock()
	return e.commitPendingInputSequenceLocked(ctx, turnID, pending)
}

func (e *Engine) commitPendingInputSequenceLocked(ctx context.Context, turnID string, pending []queuedPendingInput) error {
	queued := make([]queuedPendingInput, 0, len(pending))
	queuedStart := 0
	flushQueued := func(tailStart int) error {
		if len(queued) == 0 {
			return nil
		}
		batch := queued
		queued = nil
		if err := e.commitPendingInputBatchLocked(ctx, turnID, batch); err != nil {
			e.prependPendingInput(pending[queuedStart:])
			return err
		}
		queuedStart = tailStart
		return nil
	}
	for i, item := range pending {
		if item.Origin != PendingInputOriginTurn {
			if len(queued) == 0 {
				queuedStart = i
			}
			queued = append(queued, item)
			continue
		}
		if err := flushQueued(i); err != nil {
			return err
		}
		max := e.beginPendingInputDrain(turnID, 1)
		if sessionHasMessageID(e.currentSession(), item.Message.ID) {
			if queue := e.currentPendingInputQueue(); queue != nil && item.RecordID != "" {
				if err := queue.MarkProcessed([]string{item.RecordID}); err != nil {
					e.prependPendingInput(pending[i:])
					return fmt.Errorf("mark recovered turn input processed: %w", err)
				}
			}
		} else {
			if err := e.restoreAcceptedTurnInputLocked(ctx, turnID, PendingInputRecord{
				ID:        item.RecordID,
				MessageID: item.Message.ID,
				Message:   item.Message,
				Origin:    item.Origin,
			}); err != nil {
				e.prependPendingInput(pending[i:])
				return err
			}
		}
		e.finishPendingInputDrain(turnID, 1, max)
	}
	return flushQueued(len(pending))
}

func (e *Engine) commitPendingInputBatchLocked(ctx context.Context, turnID string, pending []queuedPendingInput) error {
	if len(pending) == 0 {
		return nil
	}
	queue := e.currentPendingInputQueue()
	sess := e.currentSession()
	max := e.beginPendingInputDrain(turnID, len(pending))
	recordIDs := pendingRecordIDs(pending)
	if queue != nil {
		if err := queue.MarkAdmitted(recordIDs, turnID); err != nil {
			return fmt.Errorf("mark pending input admitted: %w", err)
		}
	}
	e.notifyPendingInputsAdmitted(ctx, turnID, recordIDs)
	var processedIDs []string
	for _, item := range pending {
		msg := item.Message
		if sessionHasMessageID(sess, msg.ID) {
			if item.RecordID != "" {
				processedIDs = append(processedIDs, item.RecordID)
			}
			continue
		}
		policy := effectiveCompactionPolicy(e.Compaction, e.ContextWindow)
		projected, projection, err := e.projectMessageLocked(msg, policy)
		if err != nil {
			return fmt.Errorf("project pending input: %w", err)
		}
		msg = projected
		if err := e.emitProjectionApplied(turnID, projection); err != nil {
			return fmt.Errorf("commit pending input projection: %w", err)
		}
		if err := sess.Append(msg); err != nil {
			return fmt.Errorf("session append pending input: %w", err)
		}
		if item.RecordID != "" {
			processedIDs = append(processedIDs, item.RecordID)
		}
	}
	if queue != nil {
		if err := queue.MarkProcessed(processedIDs); err != nil {
			return fmt.Errorf("mark pending input processed: %w", err)
		}
	}
	e.finishPendingInputDrain(turnID, len(pending), max)
	return nil
}

func (e *Engine) beginPendingInputDrain(turnID string, count int) int {
	e.pendingMu.Lock()
	remaining := len(e.pendingInput)
	max := e.effectiveMaxPendingInputs()
	e.pendingEventAnnouncing = true
	e.pendingMu.Unlock()
	_ = e.emit(events.Event{Type: PendingInputDrainingType, TurnID: turnID, Payload: PendingInputDrainingPayload{
		Count:            count,
		PendingCount:     remaining,
		MaxPendingInputs: max,
	}})
	e.flushPendingEvents()
	return max
}

func (e *Engine) finishPendingInputDrain(turnID string, count, max int) {
	e.pendingMu.Lock()
	remaining := len(e.pendingInput)
	e.pendingMu.Unlock()
	_ = e.emit(events.Event{Type: "pending_input.drained", TurnID: turnID, Payload: PendingInputDrainedPayload{
		Count:            count,
		PendingCount:     remaining,
		MaxPendingInputs: max,
	}})
}

func (e *Engine) notifyPendingInputsAdmitted(ctx context.Context, turnID string, recordIDs []string) {
	if e == nil || len(recordIDs) == 0 {
		return
	}
	runtimemodule.NotifyPendingInputsAdmitted(ctx, runtimemodule.PendingInputAdmission{
		Runtime:   e.policyRuntimeContext(),
		Session:   e.policySessionContext(),
		TurnID:    turnID,
		RecordIDs: recordIDs,
	}, e.policySets()...)
}

func (e *Engine) deferPendingEventLocked(event events.Event) bool {
	if !e.pendingEventAnnouncing {
		return false
	}
	e.pendingDeferredEvents = append(e.pendingDeferredEvents, event)
	return true
}

func (e *Engine) flushPendingEvents() {
	for {
		e.pendingMu.Lock()
		deferred := e.pendingDeferredEvents
		e.pendingDeferredEvents = nil
		if len(deferred) == 0 {
			e.pendingEventAnnouncing = false
			e.pendingMu.Unlock()
			return
		}
		e.pendingMu.Unlock()
		for _, event := range deferred {
			_ = e.emit(event)
		}
	}
}

func (e *Engine) preservePendingInputAfterFailureLocked(turnID string) error {
	repairedTranscript := false
	for {
		e.pendingMu.Lock()
		if e.activeTurnID != turnID {
			e.pendingMu.Unlock()
			return nil
		}
		if len(e.pendingInput) > 0 {
			e.pendingMu.Unlock()
		} else {
			e.activeTurnID = ""
			e.pendingMu.Unlock()
			return nil
		}
		if !repairedTranscript {
			if err := e.repairTranscriptLocked(turnID, "turn_failure_pending_input"); err != nil {
				return err
			}
			repairedTranscript = true
		}
		if err := e.drainPendingInputLifecycleLocked(context.Background(), turnID); err != nil {
			return err
		}
	}
}

func (e *Engine) cachePolicyLocked() llm.CachePolicy {
	if e == nil {
		return llm.CachePolicy{}
	}
	sess := e.currentSession()
	if sess == nil || sess.ID == "" {
		return llm.CachePolicy{}
	}
	return llm.CachePolicy{StablePrefixKey: "juex:" + sess.ID}
}

func (e *Engine) activeTurnHasNoPending(turnID string) bool {
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if e.activeTurnID != turnID {
		return true
	}
	if len(e.pendingInput) > 0 {
		return false
	}
	return true
}

func (e *Engine) finishActiveTurn(turnID string) {
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if e.activeTurnID != turnID {
		return
	}
	e.activeTurnID = ""
}

func (e *Engine) beginTerminalPublication(turnID string) {
	e.pendingMu.Lock()
	if e.activeTurnID == turnID {
		e.activeTurnID = ""
	}
	e.terminalPublishing = turnID
	e.pendingMu.Unlock()
}

func (e *Engine) finishTerminalPublication(turnID string) {
	e.pendingMu.Lock()
	if e.terminalPublishing == turnID {
		e.terminalPublishing = ""
	}
	e.pendingMu.Unlock()
}

func (e *Engine) pendingTerminalPublicationStatus() (PendingInputStatus, bool) {
	if e == nil {
		return PendingInputStatus{}, false
	}
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	status := PendingInputStatus{
		TurnID:           e.terminalPublishing,
		PendingCount:     len(e.pendingInput),
		MaxPendingInputs: e.effectiveMaxPendingInputs(),
	}
	return status, e.terminalPublishing != ""
}

func (e *Engine) publishTerminalEvent(turnID string, event events.Event) {
	defer e.finishTerminalPublication(turnID)
	if e.Bus != nil {
		e.Bus.PublishCommitted(event)
	}
}

func (e *Engine) emit(ev events.Event) error {
	if e.Bus != nil {
		return e.Bus.Emit(ev)
	}
	return nil
}

func (e *Engine) failTurn(turnID string, err error) error {
	if emitErr := e.emit(events.Event{Type: "turn.errored", TurnID: turnID, Payload: NewTurnErroredPayload(err)}); emitErr != nil {
		return errors.Join(err, fmt.Errorf("commit turn error: %w", emitErr))
	}
	return err
}

func (e *Engine) failActiveTurnLocked(turnID string, err error, lifecycleHeld bool) error {
	if !lifecycleHeld {
		e.pendingLifecycleMu.Lock()
	}
	if preserveErr := e.preservePendingInputAfterFailureLocked(turnID); preserveErr != nil {
		err = errors.Join(err, fmt.Errorf("preserve pending input after turn failure: %w", preserveErr))
	}
	event := events.Event{Type: "turn.errored", TurnID: turnID, Payload: NewTurnErroredPayload(err)}
	var (
		committed events.Event
		commitErr error
	)
	if e.Bus == nil {
		committed = events.Normalize(event)
	} else {
		committed, commitErr = e.Bus.Commit(event)
	}
	if commitErr != nil {
		e.finishActiveTurn(turnID)
		e.pendingLifecycleMu.Unlock()
		return errors.Join(err, fmt.Errorf("commit turn error: %w", commitErr))
	}
	e.beginTerminalPublication(turnID)
	e.pendingLifecycleMu.Unlock()
	e.publishTerminalEvent(turnID, committed)
	return err
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// responseText concatenates every text block of an assistant message.
// Used to enrich the llm.responded event payload for verbose UIs.
func responseText(m llm.Message) string {
	var sb strings.Builder
	for _, b := range m.Blocks {
		if b.Type == llm.BlockText {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// responseThinking concatenates every reasoning block (anthropic thinking
// or deepseek reasoning_content). Empty when the model didn't think.
func responseThinking(m llm.Message) string {
	var sb strings.Builder
	for _, b := range m.Blocks {
		if b.Type == llm.BlockReasoning {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// responseToolCalls returns one summary entry per tool_use block in the
// assistant message: name + input map. Used by verbose UIs.
func responseToolCalls(m llm.Message, iter int) []toolevents.ToolCallPayload {
	var out []toolevents.ToolCallPayload
	callIndex := 0
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolUse {
			out = append(out, toolCallPayload(b, iter, callIndex, m.ID))
			callIndex++
		}
	}
	return out
}

func toolCallPayload(call llm.Block, iter, callIndex int, messageID string) toolevents.ToolCallPayload {
	return toolevents.ToolCallPayload{
		ToolUseID:      call.ToolUseID,
		Name:           call.ToolName,
		Input:          call.Input,
		TimeoutSeconds: call.TimeoutSeconds,
		Iter:           iter,
		CallIndex:      callIndex,
		MessageID:      messageID,
	}
}

func prepareToolInputs(blocks []llm.Block, registry *tools.Registry) []llm.Block {
	if len(blocks) == 0 {
		return blocks
	}
	out := append([]llm.Block(nil), blocks...)
	for i := range out {
		if out[i].Type == llm.BlockToolUse {
			out[i].Input = tools.NormalizeCallInput(out[i].Input)
			out[i].TimeoutSeconds = tools.DefaultTimeoutSeconds
			if registry != nil {
				out[i].TimeoutSeconds = registry.TimeoutSecondsFor(out[i].ToolName)
			}
		}
	}
	return out
}

func canContinueAfterAutoCompactError(ctx context.Context, msg llm.Message) bool {
	if ctx.Err() != nil {
		return false
	}
	switch msg.Kind {
	case llm.MessageKindMCPEvent, llm.MessageKindSideSession:
		return true
	default:
		return false
	}
}

func toolErrorContent(out string, err error) string {
	publicErr := errorclass.PublicMessage(err, errorclass.MessageOptions{})
	if out == "" {
		return publicErr
	}
	if len(out) > maxToolErrorOutput {
		limit := maxToolErrorOutput
		for limit > 0 && (out[limit]&0xC0) == 0x80 {
			limit--
		}
		out = out[:limit] + "\n... (remaining output truncated) ..."
	}
	return strings.TrimRight(out, "\n") + "\n\n[tool error]\n" + publicErr
}

func boundedToolErrorContent(out string, err error) string {
	publicErr := errorclass.PublicMessage(err, errorclass.MessageOptions{})
	if out == "" {
		return publicErr
	}
	return strings.TrimRight(out, "\n") + "\n\n[tool error]\n" + publicErr
}

func appendShellRuntimeErrorContent(base string, err error) string {
	publicErr := errorclass.PublicMessage(err, errorclass.MessageOptions{})
	if base == "" {
		return publicErr
	}
	separator := "\n\n"
	if strings.HasSuffix(base, "\n\n") {
		separator = ""
	} else if strings.HasSuffix(base, "\n") {
		separator = "\n"
	}
	return base + separator + "[tool error]\n" + publicErr
}

func finalizedShellContent(base, finalized string) string {
	if finalized == base {
		return base
	}
	suffix, ok := strings.CutPrefix(finalized, base)
	if !ok {
		return tools.BoundShellContent(finalized, maxShellPolicyContent)
	}
	return base + tools.BoundShellContent(suffix, maxShellPolicyContent)
}

func boundedRuntimeDiagnostic(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	limit := validUTF8Cut(value, maxBytes)
	return value[:limit] + "...(truncated, total " + strconv.Itoa(len(value)) + " bytes)"
}

func validUTF8Cut(value string, limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit >= len(value) {
		return len(value)
	}
	for limit > 0 && (value[limit]&0xC0) == 0x80 {
		limit--
	}
	return limit
}

func isShellStructuredResult(result any) bool {
	switch result.(type) {
	case tools.ShellResult, *tools.ShellResult:
		return true
	default:
		return false
	}
}

func rawCauseIfDifferent(rawCause, public string) string {
	if rawCause == "" || rawCause == public {
		return ""
	}
	return rawCause
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated, total " + strconv.Itoa(len(s)) + " bytes)"
}
