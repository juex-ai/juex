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

type TurnIDAllocator interface {
	NextTurnID(prefix string) string
}

type TurnIDFunc func(prefix string) string

func (f TurnIDFunc) NextTurnID(prefix string) string { return f(prefix) }

type TurnAdmissionRequest struct {
	Prompt string
	IDs    TurnIDAllocator
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

var errTurnAdmissionBusy = errors.New("app: session busy")

type turnAdmission struct {
	mu     sync.Mutex
	phase  turnAdmissionPhase
	turnID string
}

func (a *App) AdmitTurn(ctx context.Context, req TurnAdmissionRequest) TurnAdmissionResult {
	if a == nil || a.Engine == nil || a.Session == nil {
		return errorResult(fmt.Errorf("turn admission: app, engine, or session is not initialized"), nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return rejectedResult("bad_request", "expected non-empty prompt", "", false, nil, runtime.PendingInputStatus{})
	}
	if req.IDs == nil {
		return errorResult(fmt.Errorf("turn admission: missing turn id allocator"), nil)
	}

	cmd, handled, err := ParseSlashCommand(req.Prompt)
	if err != nil {
		return rejectedResult("bad_request", err.Error(), "available slash commands: "+AvailableSlashCommandsText(), false, err, runtime.PendingInputStatus{})
	}
	if handled {
		return a.admitSlashTurn(ctx, cmd, req.IDs)
	}
	return a.admitUserTurn(ctx, req.Prompt, req.IDs)
}

func (a *App) CompleteAdmittedTurn(turnID string) {
	if a == nil || turnID == "" {
		return
	}
	a.turnAdmission.mu.Lock()
	defer a.turnAdmission.mu.Unlock()
	if a.turnAdmission.phase == turnAdmissionRunning && a.turnAdmission.turnID == turnID {
		a.turnAdmission.phase = turnAdmissionIdle
		a.turnAdmission.turnID = ""
	}
}

func (a *App) BeginCompactAdmission(turnID string) error {
	return a.beginCompactAdmission(turnID)
}

func (a *App) FinishCompactAdmission(compactTurnID string, ids TurnIDAllocator) *AdmittedTurn {
	return a.finishCompactAdmission(compactTurnID, ids)
}

func (a *App) admitUserTurn(ctx context.Context, prompt string, ids TurnIDAllocator) TurnAdmissionResult {
	phase, activeTurnID := a.admissionSnapshot()
	if phase != turnAdmissionIdle {
		return a.queuePendingAdmission(ctx, prompt, activeTurnID)
	}

	turnID := ids.NextTurnID("turn")
	a.turnAdmission.mu.Lock()
	if a.turnAdmission.phase != turnAdmissionIdle {
		activeTurnID = a.turnAdmission.turnID
		a.turnAdmission.mu.Unlock()
		return a.queuePendingAdmission(ctx, prompt, activeTurnID)
	}
	if err := a.Engine.ReserveTurnID(turnID); err != nil {
		a.turnAdmission.mu.Unlock()
		return conflictResult(err.Error(), err, runtime.PendingInputStatus{})
	}
	a.turnAdmission.phase = turnAdmissionRunning
	a.turnAdmission.turnID = turnID
	a.turnAdmission.mu.Unlock()
	msg := llm.TextMessage(llm.RoleUser, prompt)
	return TurnAdmissionResult{
		Kind:   TurnAdmissionStarted,
		TurnID: turnID,
		Start:  &AdmittedTurn{TurnID: turnID, Message: msg},
	}
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
	default:
		return errorResult(&UnknownSlashCommandError{Input: cmd.Name}, nil)
	}
}

func (a *App) admitNewSlash(ctx context.Context, cmd SlashCommand, ids TurnIDAllocator) TurnAdmissionResult {
	if !a.beginExclusiveCommand() {
		return conflictResult("session busy", errTurnAdmissionBusy, runtime.PendingInputStatus{})
	}
	oldID := a.Session.ID
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
	if a.Session.ID != oldID {
		admitted.SessionChanged = &TurnAdmissionSessionChange{OldID: oldID, NewID: a.Session.ID}
	}
	return admitted
}

func (a *App) admitCompactSlash(ctx context.Context, cmd SlashCommand, ids TurnIDAllocator) TurnAdmissionResult {
	compactTurnID := ids.NextTurnID("compact")
	if err := a.beginCompactAdmission(compactTurnID); err != nil {
		return conflictResult("session busy", err, runtime.PendingInputStatus{})
	}
	result, err := a.ExecuteParsedSlashCommand(ctx, cmd)
	start := a.finishCompactAdmission(compactTurnID, ids)
	if err != nil {
		return errorResult(err, start)
	}
	return commandResult(result, start)
}

func (a *App) beginCompactAdmission(turnID string) error {
	if a == nil || a.Engine == nil {
		return runtime.ErrNoActiveTurn
	}
	a.turnAdmission.mu.Lock()
	defer a.turnAdmission.mu.Unlock()
	if a.turnAdmission.phase != turnAdmissionIdle {
		return errTurnAdmissionBusy
	}
	if err := a.Engine.ReserveTurnID(turnID); err != nil {
		return err
	}
	a.turnAdmission.phase = turnAdmissionCompacting
	a.turnAdmission.turnID = turnID
	return nil
}

func (a *App) finishCompactAdmission(compactTurnID string, ids TurnIDAllocator) *AdmittedTurn {
	if a == nil || a.Engine == nil || ids == nil {
		return nil
	}
	nextTurnID := ids.NextTurnID("turn")
	msg, _, promoted := a.Engine.PromotePendingInputTurn(compactTurnID, nextTurnID)
	a.turnAdmission.mu.Lock()
	defer a.turnAdmission.mu.Unlock()
	if promoted {
		a.turnAdmission.phase = turnAdmissionRunning
		a.turnAdmission.turnID = nextTurnID
		return &AdmittedTurn{TurnID: nextTurnID, Message: msg}
	}
	if a.turnAdmission.phase == turnAdmissionCompacting && a.turnAdmission.turnID == compactTurnID {
		a.turnAdmission.phase = turnAdmissionIdle
		a.turnAdmission.turnID = ""
	}
	return nil
}

func (a *App) beginExclusiveCommand() bool {
	if a == nil {
		return false
	}
	a.turnAdmission.mu.Lock()
	defer a.turnAdmission.mu.Unlock()
	if a.turnAdmission.phase != turnAdmissionIdle {
		return false
	}
	a.turnAdmission.phase = turnAdmissionCommand
	return true
}

func (a *App) finishExclusiveCommand() {
	if a == nil {
		return
	}
	a.turnAdmission.mu.Lock()
	defer a.turnAdmission.mu.Unlock()
	if a.turnAdmission.phase == turnAdmissionCommand {
		a.turnAdmission.phase = turnAdmissionIdle
		a.turnAdmission.turnID = ""
	}
}

func (a *App) finishExclusiveCommandAsRunning(turnID string) {
	if a == nil {
		return
	}
	a.turnAdmission.mu.Lock()
	defer a.turnAdmission.mu.Unlock()
	if a.turnAdmission.phase == turnAdmissionCommand {
		a.turnAdmission.phase = turnAdmissionRunning
		a.turnAdmission.turnID = turnID
	}
}

func (a *App) admissionSnapshot() (turnAdmissionPhase, string) {
	if a == nil {
		return turnAdmissionIdle, ""
	}
	a.turnAdmission.mu.Lock()
	defer a.turnAdmission.mu.Unlock()
	return a.turnAdmission.phase, a.turnAdmission.turnID
}

func (a *App) queuePendingAdmission(ctx context.Context, prompt, fallbackTurnID string) TurnAdmissionResult {
	status, err := a.Engine.EnqueuePendingInput(ctx, prompt)
	if status.TurnID == "" {
		status.TurnID = fallbackTurnID
	}
	switch {
	case err == nil:
		return queuedResult(status)
	case errors.Is(err, runtime.ErrPendingInputQueueFull):
		return rejectedResult(
			"pending_input_full",
			fmt.Sprintf("pending input queue full (%d/%d)", status.PendingCount, status.MaxPendingInputs),
			"wait for the active turn to drain pending input before sending more",
			true,
			err,
			status,
		)
	case errors.Is(err, runtime.ErrNoActiveTurn):
		return conflictResult("turn is not accepting pending input", err, status)
	default:
		return errorResult(err, nil)
	}
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
