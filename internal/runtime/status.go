package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/statusstream"
	"github.com/juex-ai/juex/internal/toolevents"
)

const statusHistoryLimit = 512

type ToolCallState string

const (
	ToolCallRequested ToolCallState = "requested"
	ToolCallRunning   ToolCallState = "running"
	ToolCallStreaming ToolCallState = "streaming"
	ToolCallCompleted ToolCallState = "completed"
	ToolCallErrored   ToolCallState = "errored"
)

type TurnLifecycleState string

const (
	TurnLifecycleAdmitted  TurnLifecycleState = "admitted"
	TurnLifecycleActive    TurnLifecycleState = "active"
	TurnLifecycleCompleted TurnLifecycleState = "completed"
	TurnLifecycleErrored   TurnLifecycleState = "errored"
	TurnLifecycleCancelled TurnLifecycleState = "cancelled"
)

type TurnPhase string

const (
	TurnPhaseProviderIteration TurnPhase = "provider_iteration"
	TurnPhaseToolBatch         TurnPhase = "tool_batch"
	TurnPhaseCompacting        TurnPhase = "compacting"
)

type SessionRuntimeState string

const (
	SessionRuntimeIdle            SessionRuntimeState = "idle"
	SessionRuntimeTurnActive      SessionRuntimeState = "turn_active"
	SessionRuntimeDrainingPending SessionRuntimeState = "draining_pending"
	SessionRuntimeFailed          SessionRuntimeState = "failed"
)

func (state SessionRuntimeState) IsWorking() bool {
	return state == SessionRuntimeTurnActive ||
		state == SessionRuntimeDrainingPending
}

type StatusErrorKind string

const (
	StatusErrorError            StatusErrorKind = "error"
	StatusErrorTimeout          StatusErrorKind = "timeout"
	StatusErrorCancelled        StatusErrorKind = "cancelled"
	StatusErrorInterrupted      StatusErrorKind = "interrupted"
	StatusErrorTerminated       StatusErrorKind = "terminated"
	StatusErrorPermission       StatusErrorKind = "permission"
	StatusErrorAuth             StatusErrorKind = "auth"
	StatusErrorPendingInputFull StatusErrorKind = "pending_input_full"
	StatusErrorCompaction       StatusErrorKind = "compaction"
	StatusErrorRuntimeRestart   StatusErrorKind = "runtime_restart"
)

func (kind StatusErrorKind) IsCancellation() bool {
	switch kind {
	case StatusErrorCancelled, StatusErrorInterrupted, StatusErrorTerminated,
		StatusErrorRuntimeRestart:
		return true
	default:
		return false
	}
}

type StatusSeed struct {
	SessionID        string
	SessionAlias     string
	MaxPendingInputs int
	TokenUsage       llm.Usage
	ContextUsage     *llm.ContextUsage
}

type SessionRuntimeStatus struct {
	ID               string              `json:"id"`
	Alias            string              `json:"alias,omitempty"`
	State            SessionRuntimeState `json:"state"`
	PendingCount     int                 `json:"pending_count"`
	MaxPendingInputs int                 `json:"max_pending_inputs"`
	CanAcceptInput   bool                `json:"can_accept_input"`
}

type StatusError struct {
	Message   string          `json:"message"`
	Kind      StatusErrorKind `json:"kind,omitempty"`
	TimedOut  bool            `json:"timed_out,omitempty"`
	Cancelled bool            `json:"cancelled,omitempty"`
}

type TurnRuntimeStatus struct {
	ID           string             `json:"id"`
	State        TurnLifecycleState `json:"state"`
	Phase        TurnPhase          `json:"phase"`
	Streaming    bool               `json:"streaming"`
	CanInterrupt bool               `json:"can_interrupt"`
	ResumeState  TurnLifecycleState `json:"resume_state,omitempty"`
	ResumePhase  TurnPhase          `json:"resume_phase,omitempty"`
	StartedAt    time.Time          `json:"started_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	Error        *StatusError       `json:"error,omitempty"`
}

type ToolCallStatus struct {
	ToolUseID string        `json:"tool_use_id"`
	Name      string        `json:"name"`
	State     ToolCallState `json:"state"`
	StartedAt time.Time     `json:"started_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Error     *StatusError  `json:"error,omitempty"`
}

type StatusSnapshot struct {
	Cursor       string               `json:"cursor,omitempty"`
	UpdatedAt    time.Time            `json:"updated_at,omitempty"`
	Session      SessionRuntimeStatus `json:"session"`
	Turn         *TurnRuntimeStatus   `json:"turn,omitempty"`
	Tools        []ToolCallStatus     `json:"tools"`
	TokenUsage   llm.Usage            `json:"token_usage"`
	ContextUsage *llm.ContextUsage    `json:"context_usage,omitempty"`
	LastError    *StatusError         `json:"last_error,omitempty"`
}

type StatusStore struct {
	projectionMu sync.Mutex
	stream       *statusstream.Store[StatusSnapshot]
}

type StatusStreamOptions struct {
	After  string
	Follow bool
}

type StatusStream struct {
	stream *statusstream.Stream[StatusSnapshot]
}

func (s *StatusStream) Next(ctx context.Context) (StatusSnapshot, bool) {
	if s == nil || s.stream == nil {
		return StatusSnapshot{}, false
	}
	return s.stream.Next(ctx)
}

func (s *StatusStream) Close() {
	if s != nil && s.stream != nil {
		s.stream.Close()
	}
}

func NewStatusStore(seed StatusSeed) *StatusStore {
	maxPending := seed.MaxPendingInputs
	if maxPending <= 0 {
		maxPending = DefaultMaxPendingInput
	}
	snapshot := StatusSnapshot{
		Session: SessionRuntimeStatus{
			ID:               seed.SessionID,
			Alias:            seed.SessionAlias,
			State:            SessionRuntimeIdle,
			MaxPendingInputs: maxPending,
			CanAcceptInput:   true,
		},
		Tools:        []ToolCallStatus{},
		TokenUsage:   seed.TokenUsage,
		ContextUsage: cloneContextUsage(seed.ContextUsage),
	}
	return newStatusStoreFromSnapshot(snapshot)
}

func NewStatusStoreFromJournal(seed StatusSeed, journal []events.Event) *StatusStore {
	store := NewStatusStore(seed)
	for _, event := range journal {
		store.Publish(event)
	}
	return store
}

func newStatusStoreFromSnapshot(snapshot StatusSnapshot) *StatusStore {
	snapshot = cloneStatusSnapshot(snapshot)
	recomputeCanAcceptInput(&snapshot)
	return &StatusStore{
		stream: statusstream.New(snapshot, statusstream.Options[StatusSnapshot]{
			Clone:  cloneStatusSnapshot,
			Cursor: func(snapshot StatusSnapshot) string { return snapshot.Cursor },
			Equal: func(left, right StatusSnapshot) bool {
				return reflect.DeepEqual(left, right)
			},
			HistoryLimit: statusHistoryLimit,
		}),
	}
}

// Reset replaces the projection with a new session seed and its durable
// journal. Existing subscribers receive the recovered snapshot immediately.
func (s *StatusStore) Reset(seed StatusSeed, journal []events.Event) {
	if s == nil {
		return
	}
	recovered := NewStatusStoreFromJournal(seed, journal)
	current, history := recovered.stream.Values()

	s.projectionMu.Lock()
	s.stream.Replace(current, history)
	s.projectionMu.Unlock()
}

// RecoverAfterRestart closes an interrupted in-memory turn. The event cursor
// remains the last durable cursor because this is a presentation recovery, not
// a new journal fact.
func (s *StatusStore) RecoverAfterRestart() {
	if s == nil {
		return
	}
	s.projectionMu.Lock()
	defer s.projectionMu.Unlock()

	snapshot := s.stream.Snapshot()
	if snapshot.Turn == nil ||
		snapshot.Turn.State == TurnLifecycleCompleted ||
		snapshot.Turn.State == TurnLifecycleErrored ||
		snapshot.Turn.State == TurnLifecycleCancelled {
		return
	}
	statusErr := &StatusError{
		Message:   "turn interrupted by runtime restart",
		Kind:      StatusErrorRuntimeRestart,
		Cancelled: true,
	}
	snapshot.Turn.State = TurnLifecycleCancelled
	snapshot.Turn.Streaming = false
	snapshot.Turn.Error = statusErr
	snapshot.Tools = []ToolCallStatus{}
	snapshot.Session.State = SessionRuntimeFailed
	snapshot.Session.PendingCount = 0
	snapshot.LastError = statusErr
	recomputeCanAcceptInput(&snapshot)
	s.stream.Publish(snapshot, false)
}

func (s *StatusStore) Publish(event events.Event) {
	if s == nil {
		return
	}
	s.projectionMu.Lock()
	snapshot := ProjectStatus(s.stream.Snapshot(), event)
	s.stream.Publish(snapshot, !event.Transient)
	s.projectionMu.Unlock()
}

func (s *StatusStore) Snapshot() StatusSnapshot {
	if s == nil {
		return StatusSnapshot{}
	}
	return s.stream.Snapshot()
}

func (s *StatusStore) OpenStream(options StatusStreamOptions) *StatusStream {
	if s == nil {
		return &StatusStream{}
	}
	return &StatusStream{stream: s.stream.Open(statusstream.OpenOptions{
		After:  options.After,
		Follow: options.Follow,
	})}
}

func ProjectStatus(current StatusSnapshot, event events.Event) StatusSnapshot {
	next := cloneStatusSnapshot(current)
	if !event.Transient {
		next.Cursor = event.ID
	}
	if !event.Timestamp.IsZero() {
		next.UpdatedAt = event.Timestamp
	}

	switch event.Type {
	case TurnAdmittedType:
		payload := payloadAs[TurnAdmittedPayload](event.Payload)
		next.Turn = newTurnStatus(event, TurnLifecycleAdmitted, "")
		next.Turn.CanInterrupt = !payload.NonInterruptible
		next.Tools = []ToolCallStatus{}
		next.Session.State = SessionRuntimeTurnActive
		next.LastError = nil
	case TurnPhaseType:
		payload := payloadAs[TurnPhasePayload](event.Payload)
		turn := ensureTurnStatus(&next, event)
		turn.State = TurnLifecycleActive
		if payload.Phase != "" {
			turn.Phase = payload.Phase
		}
		turn.Streaming = false
	case "llm.requested":
		turn := ensureTurnStatus(&next, event)
		turn.State = TurnLifecycleActive
		turn.Phase = TurnPhaseProviderIteration
		turn.Streaming = true
	case "llm.responded":
		payload := payloadAs[LLMRespondedPayload](event.Payload)
		turn := ensureTurnStatus(&next, event)
		turn.State = TurnLifecycleActive
		turn.Phase = TurnPhaseProviderIteration
		turn.Streaming = false
		if !payload.TokenUsage.IsZero() {
			next.TokenUsage = payload.TokenUsage
		}
		if payload.ContextUsage != nil {
			next.ContextUsage = cloneContextUsage(payload.ContextUsage)
		}
	case toolevents.RequestedType:
		payload := payloadAs[toolevents.RequestedPayload](event.Payload)
		upsertToolStatus(&next, event, payload.ToolUseID, payload.Name, ToolCallRequested, nil)
	case toolevents.RunningType:
		payload := payloadAs[toolevents.RunningPayload](event.Payload)
		upsertToolStatus(&next, event, payload.ToolUseID, payload.Name, ToolCallRunning, nil)
	case toolevents.OutputDeltaType:
		payload := payloadAs[toolevents.OutputDeltaPayload](event.Payload)
		upsertToolStatus(&next, event, payload.ToolUseID, payload.Name, ToolCallStreaming, nil)
	case toolevents.CompletedType:
		payload := payloadAs[toolevents.CompletedPayload](event.Payload)
		upsertToolStatus(&next, event, payload.ToolUseID, payload.Name, ToolCallCompleted, nil)
	case toolevents.ErroredType:
		payload := payloadAs[toolevents.ErroredPayload](event.Payload)
		statusErr := &StatusError{
			Message:  payload.Error,
			Kind:     StatusErrorKind(payload.ErrorKind),
			TimedOut: payload.TimedOut,
		}
		upsertToolStatus(&next, event, payload.ToolUseID, payload.Name, ToolCallErrored, statusErr)
	case "pending_input.queued":
		payload := payloadAs[PendingInputQueuedPayload](event.Payload)
		setPendingStatus(&next, payload.PendingCount, payload.MaxPendingInputs)
	case PendingInputDrainingType:
		payload := payloadAs[PendingInputDrainingPayload](event.Payload)
		setPendingStatus(&next, payload.PendingCount, payload.MaxPendingInputs)
		next.Session.State = SessionRuntimeDrainingPending
	case PendingInputPromotedType:
		payload := payloadAs[PendingInputPromotedPayload](event.Payload)
		setPendingStatus(&next, payload.PendingCount, payload.MaxPendingInputs)
	case "pending_input.drained":
		payload := payloadAs[PendingInputDrainedPayload](event.Payload)
		// pending_input.draining publishes the dequeued count before callbacks
		// can queue more input. Preserve any later queued count here.
		setPendingStatus(&next, -1, payload.MaxPendingInputs)
		if next.Turn != nil {
			next.Session.State = SessionRuntimeTurnActive
		} else {
			next.Session.State = SessionRuntimeIdle
		}
	case "pending_input.dropped":
		payload := payloadAs[PendingInputDroppedPayload](event.Payload)
		setPendingStatus(&next, payload.PendingCount, payload.MaxPendingInputs)
	case "pending_input.rejected":
		payload := payloadAs[PendingInputRejectedPayload](event.Payload)
		setPendingStatus(&next, payload.PendingCount, payload.MaxPendingInputs)
		next.LastError = &StatusError{Message: payload.Reason, Kind: StatusErrorPendingInputFull}
	case "context.compact.started":
		payload := payloadAs[ContextCompactStartedPayload](event.Payload)
		resumable := next.Turn != nil &&
			next.Turn.ID == event.TurnID &&
			(next.Turn.State == TurnLifecycleAdmitted ||
				next.Turn.State == TurnLifecycleActive)
		turn := ensureTurnStatus(&next, event)
		if resumable && turn.Phase != TurnPhaseCompacting {
			turn.ResumeState = turn.State
			turn.ResumePhase = turn.Phase
		} else if !resumable {
			turn.ResumeState = ""
			turn.ResumePhase = ""
		}
		turn.State = TurnLifecycleActive
		turn.Phase = TurnPhaseCompacting
		turn.Streaming = false
		if !payload.Auto {
			turn.CanInterrupt = false
		}
		next.Session.State = SessionRuntimeTurnActive
	case "context.compact.completed":
		payload := payloadAs[ContextCompactCompletedPayload](event.Payload)
		if payload.ContextUsage != nil {
			next.ContextUsage = cloneContextUsage(payload.ContextUsage)
		}
		completeCompactionStatus(&next, nil)
	case "context.compact.errored":
		payload := payloadAs[ContextCompactErroredPayload](event.Payload)
		completeCompactionStatus(&next, &StatusError{Message: payload.Error, Kind: StatusErrorCompaction})
	case "turn.completed":
		payload := payloadAs[TurnCompletedPayload](event.Payload)
		if !payload.TokenUsage.IsZero() {
			next.TokenUsage = payload.TokenUsage
		}
		turn := ensureTurnStatus(&next, event)
		turn.State = TurnLifecycleCompleted
		turn.Streaming = false
		turn.Error = nil
		next.Tools = []ToolCallStatus{}
		next.Session.State = SessionRuntimeIdle
		next.LastError = nil
	case "turn.errored":
		payload := payloadAs[TurnErroredPayload](event.Payload)
		errorKind := StatusErrorKind(payload.ErrorKind)
		cancelled := errorKind.IsCancellation() ||
			(errorKind == "" && payload.Interrupted)
		statusErr := &StatusError{
			Message:   payload.Error,
			Kind:      errorKind,
			TimedOut:  payload.TimedOut,
			Cancelled: cancelled,
		}
		turn := ensureTurnStatus(&next, event)
		turn.Streaming = false
		turn.Error = statusErr
		if cancelled {
			turn.State = TurnLifecycleCancelled
		} else {
			turn.State = TurnLifecycleErrored
		}
		next.Session.State = SessionRuntimeFailed
		next.LastError = statusErr
	}

	if next.Turn != nil && !event.Timestamp.IsZero() {
		next.Turn.UpdatedAt = event.Timestamp
	}
	recomputeCanAcceptInput(&next)
	return next
}

func newTurnStatus(event events.Event, state TurnLifecycleState, phase TurnPhase) *TurnRuntimeStatus {
	return &TurnRuntimeStatus{
		ID:           event.TurnID,
		State:        state,
		Phase:        phase,
		CanInterrupt: true,
		StartedAt:    event.Timestamp,
		UpdatedAt:    event.Timestamp,
	}
}

func ensureTurnStatus(snapshot *StatusSnapshot, event events.Event) *TurnRuntimeStatus {
	if snapshot.Turn == nil || (event.TurnID != "" && snapshot.Turn.ID != event.TurnID) {
		snapshot.Turn = newTurnStatus(event, TurnLifecycleActive, TurnPhaseProviderIteration)
		snapshot.Tools = []ToolCallStatus{}
		snapshot.LastError = nil
	}
	snapshot.Session.State = SessionRuntimeTurnActive
	return snapshot.Turn
}

func upsertToolStatus(snapshot *StatusSnapshot, event events.Event, toolUseID, name string, state ToolCallState, statusErr *StatusError) {
	if !turnAcceptsToolEvent(snapshot, event) {
		return
	}
	turn := ensureTurnStatus(snapshot, event)
	turn.State = TurnLifecycleActive
	turn.Phase = TurnPhaseToolBatch
	turn.Streaming = false
	for i := range snapshot.Tools {
		if snapshot.Tools[i].ToolUseID != toolUseID {
			continue
		}
		snapshot.Tools[i].State = state
		snapshot.Tools[i].UpdatedAt = event.Timestamp
		snapshot.Tools[i].Error = cloneStatusError(statusErr)
		return
	}
	snapshot.Tools = append(snapshot.Tools, ToolCallStatus{
		ToolUseID: toolUseID,
		Name:      name,
		State:     state,
		StartedAt: event.Timestamp,
		UpdatedAt: event.Timestamp,
		Error:     cloneStatusError(statusErr),
	})
}

func turnAcceptsToolEvent(snapshot *StatusSnapshot, event events.Event) bool {
	if snapshot.Turn == nil {
		return false
	}
	if event.TurnID != "" && snapshot.Turn.ID != event.TurnID {
		return false
	}
	return snapshot.Turn.State == TurnLifecycleAdmitted ||
		snapshot.Turn.State == TurnLifecycleActive
}

func setPendingStatus(snapshot *StatusSnapshot, pendingCount, maxPending int) {
	if pendingCount >= 0 {
		snapshot.Session.PendingCount = pendingCount
	}
	if maxPending > 0 {
		snapshot.Session.MaxPendingInputs = maxPending
	}
}

func completeCompactionStatus(snapshot *StatusSnapshot, statusErr *StatusError) {
	if snapshot.Turn == nil {
		return
	}
	turn := snapshot.Turn
	if turn.ResumeState != "" || turn.ResumePhase != "" {
		resumeState := turn.ResumeState
		if resumeState == "" {
			resumeState = TurnLifecycleActive
		}
		turn.State = resumeState
		turn.Phase = turn.ResumePhase
		turn.ResumeState = ""
		turn.ResumePhase = ""
		turn.Error = nil
		snapshot.Session.State = SessionRuntimeTurnActive
		if statusErr != nil {
			snapshot.LastError = cloneStatusError(statusErr)
		}
		return
	}
	if statusErr == nil {
		turn.State = TurnLifecycleCompleted
		turn.Streaming = false
		turn.Error = nil
		snapshot.Tools = []ToolCallStatus{}
		snapshot.Session.State = SessionRuntimeIdle
		return
	}
	turn.State = TurnLifecycleErrored
	turn.Error = cloneStatusError(statusErr)
	snapshot.Session.State = SessionRuntimeFailed
	snapshot.LastError = cloneStatusError(statusErr)
}

func payloadAs[T any](payload any) T {
	var out T
	if payload == nil {
		return out
	}
	if typed, ok := payload.(T); ok {
		return typed
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

func cloneStatusSnapshot(snapshot StatusSnapshot) StatusSnapshot {
	cloned := snapshot
	cloned.ContextUsage = cloneContextUsage(snapshot.ContextUsage)
	cloned.LastError = cloneStatusError(snapshot.LastError)
	if snapshot.Turn != nil {
		turn := *snapshot.Turn
		turn.Error = cloneStatusError(snapshot.Turn.Error)
		cloned.Turn = &turn
	}
	cloned.Tools = make([]ToolCallStatus, len(snapshot.Tools))
	for i := range snapshot.Tools {
		cloned.Tools[i] = snapshot.Tools[i]
		cloned.Tools[i].Error = cloneStatusError(snapshot.Tools[i].Error)
	}
	sort.SliceStable(cloned.Tools, func(i, j int) bool {
		return cloned.Tools[i].StartedAt.Before(cloned.Tools[j].StartedAt)
	})
	return cloned
}

func cloneStatusError(statusErr *StatusError) *StatusError {
	if statusErr == nil {
		return nil
	}
	cloned := *statusErr
	return &cloned
}

func cloneContextUsage(usage *llm.ContextUsage) *llm.ContextUsage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	cloned.Breakdown = append([]llm.ContextUsagePart(nil), usage.Breakdown...)
	return &cloned
}

func recomputeCanAcceptInput(snapshot *StatusSnapshot) {
	maxPending := snapshot.Session.MaxPendingInputs
	if maxPending <= 0 {
		maxPending = DefaultMaxPendingInput
		snapshot.Session.MaxPendingInputs = maxPending
	}
	snapshot.Session.CanAcceptInput = snapshot.Session.PendingCount < maxPending
}
