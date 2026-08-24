package app

import (
	"context"
	"errors"
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

type TurnAdmissionRequest struct {
	Prompt      string
	Kind        string
	Attachments []llm.MediaRef
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
	turnAdmissionCompacting turnAdmissionPhase = "compacting"
	turnAdmissionCommand    turnAdmissionPhase = "command"
)

type turnAdmission struct {
	transitionMu sync.Mutex
	mu           sync.Mutex
	phase        turnAdmissionPhase
	turnID       string
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
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		return errorResult(err, nil)
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" && len(req.Attachments) == 0 {
		return rejectedResult("bad_request", "expected non-empty prompt or attachment", "", false, nil, runtime.PendingInputStatus{})
	}
	if req.Kind != "" && req.Kind != llm.MessageKindSystemNotice {
		return rejectedResult("bad_request", "unsupported turn kind", "", false, nil, runtime.PendingInputStatus{})
	}
	if req.Kind == llm.MessageKindSystemNotice {
		if len(req.Attachments) > 0 {
			return rejectedResult("bad_request", "system notices cannot include attachments", "", false, nil, runtime.PendingInputStatus{})
		}
		return a.admitUserTurn(ctx, userTurnMessageWithKind(req.Prompt, nil, req.Kind))
	}

	if len(req.Attachments) > 0 {
		if _, handled, err := ParseSlashCommand(req.Prompt); handled || err != nil {
			return rejectedResult("bad_request", "slash commands cannot include attachments", "send the image as a normal message or run the slash command without attachments", false, nil, runtime.PendingInputStatus{})
		}
		result := a.admitUserTurn(ctx, userTurnMessage(req.Prompt, req.Attachments))
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
		return a.admitSlashTurn(ctx, cmd)
	}
	return a.admitUserTurn(ctx, userTurnMessage(req.Prompt, nil))
}

func (a *App) BeginCompactAdmission(ctx context.Context) (string, error) {
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		return "", err
	}
	return a.beginCompactAdmission()
}

func (a *App) FinishCompactAdmission(compactTurnID string) (*AdmittedTurn, error) {
	return a.finishCompactAdmission(compactTurnID)
}

func (a *App) admitUserTurn(ctx context.Context, msg llm.Message) TurnAdmissionResult {
	return a.admissionQueue().admitUser(ctx, msg)
}

func (a *App) admitSlashTurn(ctx context.Context, cmd SlashCommand) TurnAdmissionResult {
	switch cmd.Name {
	case SlashStatus:
		result, err := a.ExecuteParsedSlashCommand(ctx, cmd)
		if err != nil {
			return errorResult(err, nil)
		}
		return commandResult(result, nil)
	case SlashNew:
		return a.admitNewSlash(ctx, cmd)
	case SlashCompact:
		return a.admitCompactSlash(ctx, cmd)
	case SlashGoal:
		msg := llm.TextMessage(llm.RoleUser, GoalInstructionPrompt(cmd.Args))
		msg.Kind = llm.MessageKindDirect
		return a.admitUserTurn(ctx, msg)
	default:
		return errorResult(&UnknownSlashCommandError{Input: cmd.Name}, nil)
	}
}

func userTurnMessage(prompt string, attachments []llm.MediaRef) llm.Message {
	return userTurnMessageWithKind(prompt, attachments, llm.MessageKindDirect)
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

func (a *App) admitNewSlash(ctx context.Context, cmd SlashCommand) TurnAdmissionResult {
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

	admission := admissionResultFromPendingInput(a.Engine.ReceivePendingInput(ctx, runtime.PendingInputRequest{Message: NewSessionGreetingMessage()}))
	a.finishExclusiveCommand()
	if admission.Kind != TurnAdmissionStarted || admission.Start == nil {
		return admission
	}
	start := admission.Start

	admitted := commandResult(result, start)
	if current, ok := a.SessionIdentity(); ok && current.ID != oldID {
		admitted.SessionChanged = &TurnAdmissionSessionChange{OldID: oldID, NewID: current.ID}
	}
	return admitted
}

func (a *App) admitCompactSlash(ctx context.Context, cmd SlashCommand) TurnAdmissionResult {
	compactTurnID, err := a.beginCompactAdmission()
	if err != nil {
		return conflictResult("session busy", err, runtime.PendingInputStatus{})
	}
	result, err := a.executeCompactSlashCommand(ctx, cmd, compactTurnID)
	start, promotionErr := a.finishCompactAdmission(compactTurnID)
	if err := errors.Join(err, promotionErr); err != nil {
		return errorResult(err, start)
	}
	return commandResult(result, start)
}

func (a *App) beginCompactAdmission() (string, error) {
	return a.admissionQueue().beginCompact()
}

func (a *App) finishCompactAdmission(compactTurnID string) (*AdmittedTurn, error) {
	return a.admissionQueue().finishCompact(compactTurnID)
}

func (a *App) beginExclusiveCommand() bool {
	return a.admissionQueue().beginExclusiveCommand()
}

func (a *App) finishExclusiveCommand() {
	a.admissionQueue().finishExclusiveCommand()
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
		Error:            TurnAdmissionErrorInfo{Kind: "conflict", Message: msg, Retryable: true},
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
