package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

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

func (a *Agent) AdmitTurn(ctx context.Context, req TurnAdmissionRequest) TurnAdmissionResult {
	if a == nil || a.Engine == nil {
		return errorResult(fmt.Errorf("turn admission: app, engine, or Thread is not initialized"), nil)
	}
	identity, ok := a.ThreadIdentity()
	if !ok {
		return errorResult(fmt.Errorf("turn admission: app, engine, or Thread is not initialized"), nil)
	}
	if err := a.checkInput(identity.ID, req); err != nil {
		return moduleUnavailableResult(err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		return errorResult(err, nil)
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.RetryTurnID = strings.TrimSpace(req.RetryTurnID)
	if req.Prompt == "" && len(req.Attachments) == 0 {
		return rejectedResult("bad_request", "expected non-empty prompt or attachment", "", false, nil, runtime.PendingInputStatus{})
	}
	if req.Kind != "" && req.Kind != llm.MessageKindSystemNotice {
		return rejectedResult("bad_request", "unsupported turn kind", "", false, nil, runtime.PendingInputStatus{})
	}
	if req.RetryTurnID != "" && req.Kind != llm.MessageKindSystemNotice {
		return rejectedResult("bad_request", "retry turn id requires a system notice", "", false, nil, runtime.PendingInputStatus{})
	}
	if req.Kind == llm.MessageKindSystemNotice {
		if len(req.Attachments) > 0 {
			return rejectedResult("bad_request", "system notices cannot include attachments", "", false, nil, runtime.PendingInputStatus{})
		}
		return a.admissionQueue().admitUserWithRetry(ctx, userTurnMessageWithKind(req.Prompt, nil, req.Kind), req.RetryTurnID)
	}

	if len(req.Attachments) > 0 {
		if _, handled, err := a.parseCommand(req.Prompt); handled || err != nil {
			return rejectedResult("bad_request", "slash commands cannot include attachments", "send the image as a normal message or run the slash command without attachments", false, nil, runtime.PendingInputStatus{})
		}
		result := a.admitUserTurn(ctx, userTurnMessage(req.Prompt, req.Attachments))
		if result.Kind == TurnAdmissionStarted || result.Kind == TurnAdmissionQueued {
			result.Warnings = a.attachmentWarnings(len(req.Attachments))
		}
		return result
	}

	cmd, handled, err := a.parseCommand(req.Prompt)
	if err != nil {
		return rejectedResult("bad_request", err.Error(), "available slash commands: "+a.executionPolicy.CommandHelp, false, err, runtime.PendingInputStatus{})
	}
	if handled {
		return a.admitCommand(ctx, cmd)
	}
	return a.admitUserTurn(ctx, userTurnMessage(req.Prompt, nil))
}

func (a *Agent) BeginCompactAdmission(ctx context.Context) (string, error) {
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		return "", err
	}
	return a.beginCompactAdmission()
}

func (a *Agent) FinishCompactAdmission(compactTurnID string) (*AdmittedTurn, error) {
	return a.finishCompactAdmission(compactTurnID)
}

func (a *Agent) admitUserTurn(ctx context.Context, msg llm.Message) TurnAdmissionResult {
	return a.admissionQueue().admitUser(ctx, msg)
}

func (a *Agent) admitCommand(ctx context.Context, cmd Command) TurnAdmissionResult {
	switch cmd.Kind {
	case CommandKindStatus:
		result, err := a.ExecuteCommand(ctx, cmd)
		if err != nil {
			return errorResult(err, nil)
		}
		return commandResult(result, nil)
	case CommandKindNew:
		return a.admitNewCommand(ctx, cmd)
	case CommandKindCompact:
		return a.admitCompactCommand(ctx, cmd)
	case CommandKindPrompt:
		msg := llm.TextMessage(llm.RoleUser, cmd.Prompt)
		msg.Kind = llm.MessageKindDirect
		return a.admitUserTurn(ctx, msg)
	default:
		return errorResult(fmt.Errorf("unknown command %q", cmd.Name), nil)
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

func (a *Agent) admitNewCommand(ctx context.Context, cmd Command) TurnAdmissionResult {
	if !a.beginExclusiveCommand() {
		return conflictResult("Thread busy", errTurnAdmissionBusy, runtime.PendingInputStatus{})
	}
	defer a.finishExclusiveCommand()
	result, err := a.ExecuteCommand(ctx, cmd)
	if err != nil {
		return errorResult(err, nil)
	}
	return commandResult(result, nil)
}

func (a *Agent) admitCompactCommand(ctx context.Context, cmd Command) TurnAdmissionResult {
	compactTurnID, err := a.beginCompactAdmission()
	if err != nil {
		return conflictResult("Thread busy", err, runtime.PendingInputStatus{})
	}
	result, err := a.executeCompactCommand(ctx, cmd, compactTurnID)
	start, promotionErr := a.finishCompactAdmission(compactTurnID)
	if err := errors.Join(err, promotionErr); err != nil {
		return errorResult(err, start)
	}
	return commandResult(result, start)
}

func (a *Agent) beginCompactAdmission() (string, error) {
	return a.admissionQueue().beginCompact()
}

func (a *Agent) finishCompactAdmission(compactTurnID string) (*AdmittedTurn, error) {
	return a.admissionQueue().finishCompact(compactTurnID)
}

func (a *Agent) beginExclusiveCommand() bool {
	return a.admissionQueue().beginExclusiveCommand()
}

func (a *Agent) finishExclusiveCommand() {
	a.admissionQueue().finishExclusiveCommand()
}

func queuedResult(inputID string, status runtime.PendingInputStatus) TurnAdmissionResult {
	return TurnAdmissionResult{
		Kind:             TurnAdmissionQueued,
		InputID:          inputID,
		Queued:           true,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
	}
}

func commandResult(result CommandResult, start *AdmittedTurn) TurnAdmissionResult {
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
