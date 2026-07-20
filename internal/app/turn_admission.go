package app

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
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

type TurnIDAllocator interface {
	NextTurnID(prefix string) string
}

type TurnIDFunc func(prefix string) string

func (f TurnIDFunc) NextTurnID(prefix string) string { return f(prefix) }

type TurnAdmissionRequest struct {
	Prompt      string
	Kind        string
	Attachments []llm.MediaRef
	IDs         TurnIDAllocator
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

type TurnAdmissionSessionChange struct {
	OldID string
	NewID string
}

type TurnAdmissionResult struct {
	Kind             TurnAdmissionKind
	TurnID           string
	Start            *AdmittedTurn
	Queued           bool
	PendingCount     int
	MaxPendingInputs int
	Command          *SlashCommandResult
	SessionChanged   *TurnAdmissionSessionChange
	Warnings         []TurnWarning
	Error            TurnAdmissionErrorInfo
	Err              error
}

type turnAdmissionPhase string

const (
	turnAdmissionIdle       turnAdmissionPhase = ""
	turnAdmissionRunning    turnAdmissionPhase = "running"
	turnAdmissionCompacting turnAdmissionPhase = "compacting"
	turnAdmissionCommand    turnAdmissionPhase = "command"
)

type turnAdmission struct {
	mu     sync.Mutex
	phase  turnAdmissionPhase
	turnID string
}

func (a *App) AdmitTurn(ctx context.Context, req TurnAdmissionRequest) TurnAdmissionResult {
	if a == nil || a.Engine == nil {
		return errorResult(fmt.Errorf("turn admission: app, engine, or session is not initialized"), nil)
	}
	if _, ok := a.SessionIdentity(); !ok {
		return errorResult(fmt.Errorf("turn admission: app, engine, or session is not initialized"), nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" && len(req.Attachments) == 0 {
		return rejectedResult("bad_request", "expected non-empty prompt or attachment", "", false, nil, runtime.PendingInputStatus{})
	}
	if req.IDs == nil {
		return errorResult(fmt.Errorf("turn admission: missing turn id allocator"), nil)
	}
	if req.Kind != "" && req.Kind != llm.MessageKindSystemNotice {
		return rejectedResult("bad_request", "unsupported turn kind", "", false, nil, runtime.PendingInputStatus{})
	}
	if req.Kind == llm.MessageKindSystemNotice {
		if len(req.Attachments) > 0 {
			return rejectedResult("bad_request", "system notices cannot include attachments", "", false, nil, runtime.PendingInputStatus{})
		}
		return a.admitUserTurn(ctx, userTurnMessageWithKind(req.Prompt, nil, req.Kind), req.IDs)
	}

	if len(req.Attachments) > 0 {
		if _, handled, err := ParseSlashCommand(req.Prompt); handled || err != nil {
			return rejectedResult("bad_request", "slash commands cannot include attachments", "send the image as a normal message or run the slash command without attachments", false, nil, runtime.PendingInputStatus{})
		}
		result := a.admitUserTurn(ctx, userTurnMessage(req.Prompt, req.Attachments), req.IDs)
		if result.Kind == TurnAdmissionStarted || result.Kind == TurnAdmissionQueued {
			result.Warnings = a.AttachmentWarnings(len(req.Attachments))
		}
		return result
	}

	cmd, handled, err := ParseSlashCommand(req.Prompt)
	if err != nil {
		return rejectedResult("bad_request", err.Error(), "available slash commands: "+AvailableSlashCommandsText(), false, err, runtime.PendingInputStatus{})
	}
	if handled {
		return a.admitSlashTurn(ctx, cmd, req.IDs)
	}
	return a.admitUserTurn(ctx, userTurnMessage(req.Prompt, nil), req.IDs)
}

func (a *App) CompleteAdmittedTurn(turnID string) {
	a.admissionQueue().complete(turnID)
}

func (a *App) BeginCompactAdmission(turnID string) error {
	return a.beginCompactAdmission(turnID)
}

func (a *App) FinishCompactAdmission(compactTurnID string, ids TurnIDAllocator) *AdmittedTurn {
	return a.finishCompactAdmission(compactTurnID, ids)
}

func (a *App) admitUserTurn(ctx context.Context, msg llm.Message, ids TurnIDAllocator) TurnAdmissionResult {
	return a.admissionQueue().admitUser(ctx, msg, ids)
}

func (a *App) admitSlashTurn(ctx context.Context, cmd SlashCommand, ids TurnIDAllocator) TurnAdmissionResult {
	switch cmd.Name {
	case SlashStatus:
		result, err := a.ExecuteParsedSlashCommand(ctx, cmd)
		if err != nil {
			return errorResult(err, nil)
		}
		return commandResult(result, nil)
	case SlashNew:
		return a.admitNewSlash(ctx, cmd, ids)
	case SlashCompact:
		return a.admitCompactSlash(ctx, cmd, ids)
	case SlashGoal:
		return a.admitUserTurn(ctx, llm.TextMessage(llm.RoleUser, GoalInstructionPrompt(cmd.Args)), ids)
	default:
		return errorResult(&UnknownSlashCommandError{Input: cmd.Name}, nil)
	}
}

func userTurnMessage(prompt string, attachments []llm.MediaRef) llm.Message {
	return userTurnMessageWithKind(prompt, attachments, "")
}

func userTurnMessageWithKind(prompt string, attachments []llm.MediaRef, kind string) llm.Message {
	blocks := make([]llm.Block, 0, 1+len(attachments))
	if prompt = strings.TrimSpace(prompt); prompt != "" {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: prompt})
	}
	for i := range attachments {
		blocks = append(blocks, llm.Block{Type: llm.BlockImage, Media: &attachments[i]})
	}
	return llm.Message{Role: llm.RoleUser, Kind: kind, Blocks: blocks}
}

func (a *App) admitNewSlash(ctx context.Context, cmd SlashCommand, ids TurnIDAllocator) TurnAdmissionResult {
	if !a.beginExclusiveCommand() {
		return conflictResult("session busy", errTurnAdmissionBusy, runtime.PendingInputStatus{})
	}
	oldIdentity, ok := a.SessionIdentity()
	if !ok {
		a.finishExclusiveCommand()
		return errorResult(ErrSessionUnavailable, nil)
	}
	oldID := oldIdentity.ID
	result, err := a.ExecuteParsedSlashCommand(ctx, cmd)
	if err != nil {
		a.finishExclusiveCommand()
		return errorResult(err, nil)
	}

	turnID := ids.NextTurnID("turn")
	if err := a.Engine.ReserveTurnID(turnID); err != nil {
		a.finishExclusiveCommand()
		return errorResult(err, nil)
	}
	start := &AdmittedTurn{TurnID: turnID, Message: NewSessionGreetingMessage()}
	a.finishExclusiveCommandAsRunning(turnID)

	admitted := commandResult(result, start)
	if current, ok := a.SessionIdentity(); ok && current.ID != oldID {
		admitted.SessionChanged = &TurnAdmissionSessionChange{OldID: oldID, NewID: current.ID}
	}
	return admitted
}

func (a *App) admitCompactSlash(ctx context.Context, cmd SlashCommand, ids TurnIDAllocator) TurnAdmissionResult {
	compactTurnID := ids.NextTurnID("compact")
	if err := a.beginCompactAdmission(compactTurnID); err != nil {
		return conflictResult("session busy", err, runtime.PendingInputStatus{})
	}
	result, err := a.executeCompactSlashCommand(ctx, cmd, compactTurnID)
	start := a.finishCompactAdmission(compactTurnID, ids)
	if err != nil {
		return errorResult(err, start)
	}
	return commandResult(result, start)
}

func (a *App) beginCompactAdmission(turnID string) error {
	return a.admissionQueue().beginCompact(turnID)
}

func (a *App) finishCompactAdmission(compactTurnID string, ids TurnIDAllocator) *AdmittedTurn {
	return a.admissionQueue().finishCompact(compactTurnID, ids)
}

func (a *App) beginExclusiveCommand() bool {
	return a.admissionQueue().beginExclusiveCommand()
}

func (a *App) finishExclusiveCommand() {
	a.admissionQueue().finishExclusiveCommand()
}

func (a *App) finishExclusiveCommandAsRunning(turnID string) {
	a.admissionQueue().finishExclusiveCommandAsRunning(turnID)
}

func queuedResult(status runtime.PendingInputStatus) TurnAdmissionResult {
	return TurnAdmissionResult{
		Kind:             TurnAdmissionQueued,
		TurnID:           status.TurnID,
		Queued:           true,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
	}
}

func commandResult(result SlashCommandResult, start *AdmittedTurn) TurnAdmissionResult {
	return TurnAdmissionResult{
		Kind:    TurnAdmissionCommandCompleted,
		TurnID:  turnIDFromStart(start),
		Start:   start,
		Command: &result,
	}
}

func conflictResult(msg string, err error, status runtime.PendingInputStatus) TurnAdmissionResult {
	return TurnAdmissionResult{
		Kind:             TurnAdmissionConflict,
		TurnID:           status.TurnID,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
		Error:            TurnAdmissionErrorInfo{Kind: "conflict", Message: msg},
		Err:              err,
	}
}

func rejectedResult(kind, msg, suggestion string, retryable bool, err error, status runtime.PendingInputStatus) TurnAdmissionResult {
	return TurnAdmissionResult{
		Kind:             TurnAdmissionRejected,
		TurnID:           status.TurnID,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
		Error: TurnAdmissionErrorInfo{
			Kind:       kind,
			Message:    msg,
			Suggestion: suggestion,
			Retryable:  retryable,
		},
		Err: err,
	}
}

func errorResult(err error, start *AdmittedTurn) TurnAdmissionResult {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return TurnAdmissionResult{
		Kind:   TurnAdmissionError,
		TurnID: turnIDFromStart(start),
		Start:  start,
		Error:  TurnAdmissionErrorInfo{Kind: "general_error", Message: msg},
		Err:    err,
	}
}

func turnIDFromStart(start *AdmittedTurn) string {
	if start == nil {
		return ""
	}
	return start.TurnID
}
