// Package statusapi defines the transport contract for runtime status APIs.
package statusapi

import (
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

type ActivityState string

const (
	ActivityIdle    ActivityState = "idle"
	ActivityWorking ActivityState = "working"
)

type SessionState string

const (
	SessionIdle            SessionState = "idle"
	SessionTurnActive      SessionState = "turn_active"
	SessionDrainingPending SessionState = "draining_pending"
	SessionFailed          SessionState = "failed"
)

type TurnState string

const (
	TurnAdmitted  TurnState = "admitted"
	TurnActive    TurnState = "active"
	TurnCompleted TurnState = "completed"
	TurnErrored   TurnState = "errored"
	TurnCancelled TurnState = "cancelled"
)

type TurnPhase string

const (
	TurnPhaseAdmitted          TurnPhase = "admitted"
	TurnPhaseProviderIteration TurnPhase = "provider_iteration"
	TurnPhaseToolBatch         TurnPhase = "tool_batch"
	TurnPhaseCompacting        TurnPhase = "compacting"
)

type ToolCallState string

const (
	ToolCallRequested ToolCallState = "requested"
	ToolCallRunning   ToolCallState = "running"
	ToolCallStreaming ToolCallState = "streaming"
	ToolCallCompleted ToolCallState = "completed"
	ToolCallErrored   ToolCallState = "errored"
)

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

type Usage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
}

type ContextUsagePart struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Tokens int    `json:"tokens"`
}

type ContextUsage struct {
	Model             string             `json:"model,omitempty"`
	ContextWindow     int                `json:"context_window,omitempty"`
	InputTokens       int                `json:"input_tokens"`
	OutputTokens      int                `json:"output_tokens"`
	CachedInputTokens int                `json:"cached_input_tokens,omitempty"`
	TotalTokens       int                `json:"total_tokens"`
	Breakdown         []ContextUsagePart `json:"breakdown,omitempty"`
}

type StatusError struct {
	Message   string          `json:"message"`
	Kind      StatusErrorKind `json:"kind,omitempty"`
	TimedOut  bool            `json:"timed_out,omitempty"`
	Cancelled bool            `json:"cancelled,omitempty"`
}

type SessionStatus struct {
	ID               string       `json:"id"`
	Alias            string       `json:"alias,omitempty"`
	State            SessionState `json:"state"`
	Working          bool         `json:"working"`
	PendingCount     int          `json:"pending_count"`
	MaxPendingInputs int          `json:"max_pending_inputs"`
	CanAcceptInput   bool         `json:"can_accept_input"`
}

type TurnStatus struct {
	ID           string       `json:"id"`
	State        TurnState    `json:"state"`
	Phase        TurnPhase    `json:"phase"`
	Streaming    bool         `json:"streaming"`
	CanInterrupt bool         `json:"can_interrupt"`
	StartedAt    time.Time    `json:"started_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	Error        *StatusError `json:"error,omitempty"`
}

type ToolCallStatus struct {
	ToolUseID string        `json:"tool_use_id"`
	Name      string        `json:"name"`
	State     ToolCallState `json:"state"`
	StartedAt time.Time     `json:"started_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Error     *StatusError  `json:"error,omitempty"`
}

type Snapshot struct {
	Cursor       string           `json:"cursor,omitempty"`
	UpdatedAt    time.Time        `json:"updated_at,omitempty"`
	Session      SessionStatus    `json:"session"`
	Turn         *TurnStatus      `json:"turn,omitempty"`
	Tools        []ToolCallStatus `json:"tools"`
	TokenUsage   Usage            `json:"token_usage"`
	ContextUsage *ContextUsage    `json:"context_usage,omitempty"`
	LastError    *StatusError     `json:"last_error,omitempty"`
}

type AgentActivity struct {
	State             ActivityState `json:"state"`
	PendingInputCount int           `json:"pending_input_count"`
	SelectedStatus    *Snapshot     `json:"selected_status,omitempty"`
}

func FromRuntime(source runtime.StatusSnapshot) Snapshot {
	result := Snapshot{
		Cursor:    source.Cursor,
		UpdatedAt: source.UpdatedAt,
		Session: SessionStatus{
			ID:               source.Session.ID,
			Alias:            source.Session.Alias,
			State:            SessionState(source.Session.State),
			Working:          source.Session.State.IsWorking(),
			PendingCount:     source.Session.PendingCount,
			MaxPendingInputs: source.Session.MaxPendingInputs,
			CanAcceptInput:   source.Session.CanAcceptInput,
		},
		Tools: make([]ToolCallStatus, len(source.Tools)),
		TokenUsage: Usage{
			InputTokens:       source.TokenUsage.InputTokens,
			OutputTokens:      source.TokenUsage.OutputTokens,
			CachedInputTokens: source.TokenUsage.CachedInputTokens,
		},
		ContextUsage: contextUsageFromRuntime(source.ContextUsage),
		LastError:    statusErrorFromRuntime(source.LastError),
	}
	if source.Turn != nil {
		result.Turn = &TurnStatus{
			ID:           source.Turn.ID,
			State:        TurnState(source.Turn.State),
			Phase:        TurnPhase(source.Turn.Phase),
			Streaming:    source.Turn.Streaming,
			CanInterrupt: source.Turn.CanInterrupt,
			StartedAt:    source.Turn.StartedAt,
			UpdatedAt:    source.Turn.UpdatedAt,
			Error:        statusErrorFromRuntime(source.Turn.Error),
		}
	}
	for index := range source.Tools {
		result.Tools[index] = ToolCallStatus{
			ToolUseID: source.Tools[index].ToolUseID,
			Name:      source.Tools[index].Name,
			State:     ToolCallState(source.Tools[index].State),
			StartedAt: source.Tools[index].StartedAt,
			UpdatedAt: source.Tools[index].UpdatedAt,
			Error:     statusErrorFromRuntime(source.Tools[index].Error),
		}
	}
	return result
}

func statusErrorFromRuntime(source *runtime.StatusError) *StatusError {
	if source == nil {
		return nil
	}
	return &StatusError{
		Message:   source.Message,
		Kind:      StatusErrorKind(source.Kind),
		TimedOut:  source.TimedOut,
		Cancelled: source.Cancelled,
	}
}

func contextUsageFromRuntime(source *llm.ContextUsage) *ContextUsage {
	if source == nil {
		return nil
	}
	result := &ContextUsage{
		Model:             source.Model,
		ContextWindow:     source.ContextWindow,
		InputTokens:       source.InputTokens,
		OutputTokens:      source.OutputTokens,
		CachedInputTokens: source.CachedInputTokens,
		TotalTokens:       source.TotalTokens,
		Breakdown:         make([]ContextUsagePart, len(source.Breakdown)),
	}
	for index := range source.Breakdown {
		result.Breakdown[index] = ContextUsagePart{
			Key:    source.Breakdown[index].Key,
			Label:  source.Breakdown[index].Label,
			Tokens: source.Breakdown[index].Tokens,
		}
	}
	return result
}
