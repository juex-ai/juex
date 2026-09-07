package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
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

func (a *App) AdmitTurn(ctx context.Context, req agent.TurnAdmissionRequest) agent.TurnAdmissionResult {
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
		if result.Kind == agent.TurnAdmissionStarted || result.Kind == agent.TurnAdmissionQueued {
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

func (a *App) BeginCompactAdmission(ctx context.Context) (string, error) {
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		return "", err
	}
	return a.beginCompactAdmission()
}

func (a *App) FinishCompactAdmission(compactTurnID string) (*agent.AdmittedTurn, error) {
	return a.finishCompactAdmission(compactTurnID)
}

func (a *App) admitUserTurn(ctx context.Context, msg llm.Message) agent.TurnAdmissionResult {
	return a.admissionQueue().admitUser(ctx, msg)
}

func (a *App) admitCommand(ctx context.Context, cmd agent.Command) agent.TurnAdmissionResult {
	switch cmd.Kind {
	case agent.CommandKindStatus:
		result, err := a.ExecuteCommand(ctx, cmd)
		if err != nil {
			return errorResult(err, nil)
		}
		return commandResult(result, nil)
	case agent.CommandKindNew:
		return a.admitNewCommand(ctx, cmd)
	case agent.CommandKindCompact:
		return a.admitCompactCommand(ctx, cmd)
	case agent.CommandKindPrompt:
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

func (a *App) admitNewCommand(ctx context.Context, cmd agent.Command) agent.TurnAdmissionResult {
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

func (a *App) admitCompactCommand(ctx context.Context, cmd agent.Command) agent.TurnAdmissionResult {
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

func (a *App) beginCompactAdmission() (string, error) {
	return a.admissionQueue().beginCompact()
}

func (a *App) finishCompactAdmission(compactTurnID string) (*agent.AdmittedTurn, error) {
	return a.admissionQueue().finishCompact(compactTurnID)
}

func (a *App) beginExclusiveCommand() bool {
	return a.admissionQueue().beginExclusiveCommand()
}

func (a *App) finishExclusiveCommand() {
	a.admissionQueue().finishExclusiveCommand()
}

func queuedResult(inputID string, status runtime.PendingInputStatus) agent.TurnAdmissionResult {
	return agent.TurnAdmissionResult{
		Kind:             agent.TurnAdmissionQueued,
		InputID:          inputID,
		Queued:           true,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
	}
}

func commandResult(result agent.CommandResult, start *agent.AdmittedTurn) agent.TurnAdmissionResult {
	return agent.TurnAdmissionResult{
		Kind:    agent.TurnAdmissionCommandCompleted,
		TurnID:  turnIDFromStart(start),
		Start:   start,
		Command: &result,
	}
}

func conflictResult(msg string, err error, status runtime.PendingInputStatus) agent.TurnAdmissionResult {
	return agent.TurnAdmissionResult{
		Kind:             agent.TurnAdmissionConflict,
		TurnID:           status.TurnID,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
		Error:            agent.TurnAdmissionErrorInfo{Kind: "conflict", Message: msg, Retryable: true},
		Err:              err,
	}
}

func rejectedResult(kind, msg, suggestion string, retryable bool, err error, status runtime.PendingInputStatus) agent.TurnAdmissionResult {
	return agent.TurnAdmissionResult{
		Kind:             agent.TurnAdmissionRejected,
		TurnID:           status.TurnID,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
		Error: agent.TurnAdmissionErrorInfo{
			Kind:       kind,
			Message:    msg,
			Suggestion: suggestion,
			Retryable:  retryable,
		},
		Err: err,
	}
}

func errorResult(err error, start *agent.AdmittedTurn) agent.TurnAdmissionResult {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return agent.TurnAdmissionResult{
		Kind:   agent.TurnAdmissionError,
		TurnID: turnIDFromStart(start),
		Start:  start,
		Error:  agent.TurnAdmissionErrorInfo{Kind: "general_error", Message: msg},
		Err:    err,
	}
}

func turnIDFromStart(start *agent.AdmittedTurn) string {
	if start == nil {
		return ""
	}
	return start.TurnID
}
