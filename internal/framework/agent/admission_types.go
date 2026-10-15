package agent

import (
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type TurnAdmissionKind string

const (
	TurnAdmissionStarted          TurnAdmissionKind = "started"
	TurnAdmissionQueued           TurnAdmissionKind = "queued"
	TurnAdmissionCommandCompleted TurnAdmissionKind = "command_completed"
	TurnAdmissionConflict         TurnAdmissionKind = "conflict"
	TurnAdmissionRejected         TurnAdmissionKind = "rejected"
	TurnAdmissionError            TurnAdmissionKind = "error"
)

type TurnAdmissionRequest struct {
	Prompt      string
	Kind        string
	Attachments []llm.MediaRef
	RetryTurnID string
}

type AdmittedTurn struct {
	TurnID  string
	Message llm.Message
}

type TurnAdmissionErrorInfo struct {
	Kind       string
	Message    string
	Suggestion string
	Retryable  bool
}

type TurnAdmissionResult struct {
	Kind             TurnAdmissionKind
	InputID          string
	TurnID           string
	Start            *AdmittedTurn
	Queued           bool
	PendingCount     int
	MaxPendingInputs int
	Command          *CommandResult
	Warnings         []TurnWarning
	Error            TurnAdmissionErrorInfo
	Err              error
}
