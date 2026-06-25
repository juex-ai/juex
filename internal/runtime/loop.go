// Package runtime implements the synchronous turn loop that drives user or
// system-originated input through repeated LLM calls and tool dispatches until
// the model stops requesting tools.
//
// Behaviour highlights:
//
//   - System prompt sections are rebuilt every turn so memory, skills, and
//     AGENTS.md changes propagate immediately.
//   - Context projection externalizes oversized user inputs and tool results
//     before provider submission while preserving recoverable session history.
//   - Automatic and manual compaction keep active context bounded with compact
//     summary markers and retained recent tail messages.
//   - tool_use blocks within a single LLM response run in parallel; results
//     are collected and reattached to history in the original order.
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
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/toolevents"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	DefaultMaxPendingInput  = 16
	DefaultPendingInputTTL  = 15 * time.Minute
	DefaultExternalEventTTL = 24 * time.Hour
	maxToolErrorOutput      = 32 * 1024
)

type Engine struct {
	Provider llm.Provider
	Tools    *tools.Registry
	Bus      *events.Bus
	Session  *session.Session
	Prompt   *prompt.Builder
	Hooks    HookRunner
	// HookContext carries process/session metadata included in every hook
	// command input. Event-specific fields are filled by the runtime.
	HookContext hooks.Request
	// MaxPendingInputs caps user or external event messages that can be
	// queued while a turn is active. When omitted, DefaultMaxPendingInput is
	// used. A full queue rejects new input instead of silently dropping it.
	MaxPendingInputs int
	// PendingInputQueue persists pending input records in the session
	// directory. When omitted, the engine creates a session-local queue on
	// first use.
	PendingInputQueue *PendingInputQueue
	// WorkingState persists generic session working memory in the session
	// directory. When omitted, the engine creates it lazily unless disabled.
	WorkingState *WorkingStateStore
	// GoalState persists the current session goal and latest completion check.
	GoalState *GoalStateStore
	// DisableWorkingState prevents sidecar persistence, updates, and provider
	// context injection.
	DisableWorkingState bool
	// ShowBuiltinHookTraces includes built-in runtime gates in UI-only hook
	// trace messages. Command hook traces are always shown.
	ShowBuiltinHookTraces bool
	// PendingInputTTL controls generated-id user steer records.
	PendingInputTTL time.Duration
	// ExternalEventTTL controls MCP/external event records when the caller
	// does not pass a TTL.
	ExternalEventTTL time.Duration
	// ContextWindow is the provider context window in tokens. When omitted,
	// the engine uses DefaultContextWindowTokens.
	ContextWindow int
	Compaction    CompactionPolicy

	// mu serializes turns for one Engine. MCP notifications can arrive while
	// a user turn is running, and both paths append to the same session
	// history; queuing them preserves the provider-facing transcript order.
	mu sync.Mutex

	pendingMu    sync.Mutex
	activeTurnID string
	pendingInput []queuedPendingInput

	autoCompactFailures int
	toolFailures        *toolFailureLedger
}

type HookRunner interface {
	Run(context.Context, hooks.Request) ([]hooks.Result, error)
}

var (
	ErrNoActiveTurn          = errors.New("runtime: no active turn accepting pending input")
	ErrActiveTurnExists      = errors.New("runtime: active turn already accepting pending input")
	ErrPendingInputQueueFull = errors.New("runtime: pending input queue full")
)

type PendingInputStatus struct {
	TurnID           string `json:"turn_id,omitempty"`
	PendingCount     int    `json:"pending_count"`
	MaxPendingInputs int    `json:"max_pending_inputs"`
}

type queuedPendingInput struct {
	RecordID string
	Message  llm.Message
}

// Turn drives one user input to completion. The returned string is the final
// assistant text response (concatenated text blocks). Returns an error when
// cancellation or provider/tool/context failure stops the turn.
func (e *Engine) Turn(ctx context.Context, userInput string) (string, error) {
	return e.TurnMessage(ctx, llm.TextMessage(llm.RoleUser, userInput))
}

func (e *Engine) ReserveTurnID(turnID string) error {
	if e == nil {
		return ErrNoActiveTurn
	}
	if turnID == "" {
		return fmt.Errorf("runtime: empty turn id")
	}
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if e.activeTurnID != "" && e.activeTurnID != turnID {
		return ErrActiveTurnExists
	}
	e.activeTurnID = turnID
	return nil
}

func (e *Engine) EnqueuePendingInput(ctx context.Context, userInput string) (PendingInputStatus, error) {
	return e.EnqueuePendingMessage(ctx, llm.TextMessage(llm.RoleUser, userInput))
}

func (e *Engine) EnqueuePendingMessage(ctx context.Context, userMsg llm.Message) (PendingInputStatus, error) {
	return e.EnqueuePendingMessageWithOptions(ctx, userMsg, PendingInputOptions{})
}

func (e *Engine) EnqueuePendingMessageWithOptions(ctx context.Context, userMsg llm.Message, opts PendingInputOptions) (PendingInputStatus, error) {
	if e == nil {
		return PendingInputStatus{}, ErrNoActiveTurn
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PendingInputStatus{}, err
	}
	max := e.effectiveMaxPendingInputs()
	e.pendingMu.Lock()
	turnID := e.activeTurnID
	status := PendingInputStatus{TurnID: turnID, PendingCount: len(e.pendingInput), MaxPendingInputs: max}
	if turnID == "" {
		e.pendingMu.Unlock()
		return status, ErrNoActiveTurn
	}
	if len(e.pendingInput) >= max {
		e.pendingMu.Unlock()
		e.emit(events.Event{Type: "pending_input.rejected", TurnID: turnID, Payload: PendingInputRejectedPayload{
			Input:            userMsg.FirstText(),
			Kind:             userMsg.Kind,
			PendingCount:     status.PendingCount,
			MaxPendingInputs: status.MaxPendingInputs,
			Reason:           "queue_full",
		}})
		return status, ErrPendingInputQueueFull
	}
	recordID := ""
	if queue := e.pendingInputQueueLocked(); queue != nil {
		opts = e.defaultPendingInputOptions(userMsg, opts)
		record, err := queue.Enqueue(userMsg, opts, turnID)
		if err != nil {
			e.pendingMu.Unlock()
			return status, err
		}
		recordID = record.ID
		userMsg = record.Message
		if !isReplayablePendingState(record.State) {
			status.PendingCount = len(e.pendingInput)
			e.pendingMu.Unlock()
			return status, nil
		}
		if e.hasPendingRecordLocked(record.ID) {
			status.PendingCount = len(e.pendingInput)
			e.pendingMu.Unlock()
			return status, nil
		}
	}
	e.pendingInput = append(e.pendingInput, queuedPendingInput{RecordID: recordID, Message: userMsg})
	status.PendingCount = len(e.pendingInput)
	e.pendingMu.Unlock()
	e.emit(events.Event{Type: "pending_input.queued", TurnID: turnID, Payload: PendingInputQueuedPayload{
		Input:            userMsg.FirstText(),
		Kind:             userMsg.Kind,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
	}})
	return status, nil
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
func (e *Engine) PromotePendingInputTurn(currentTurnID, nextTurnID string) (llm.Message, PendingInputStatus, bool) {
	if e == nil || nextTurnID == "" {
		return llm.Message{}, PendingInputStatus{}, false
	}
	max := e.effectiveMaxPendingInputs()
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if e.activeTurnID != currentTurnID || len(e.pendingInput) == 0 {
		if e.activeTurnID == currentTurnID {
			e.activeTurnID = ""
		}
		return llm.Message{}, PendingInputStatus{
			TurnID:           e.activeTurnID,
			PendingCount:     len(e.pendingInput),
			MaxPendingInputs: max,
		}, false
	}
	item := e.pendingInput[0]
	e.pendingInput[0] = queuedPendingInput{}
	e.pendingInput = e.pendingInput[1:]
	e.activeTurnID = nextTurnID
	return item.Message, PendingInputStatus{
		TurnID:           nextTurnID,
		PendingCount:     len(e.pendingInput),
		MaxPendingInputs: max,
	}, true
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
		turnID = newID()
	}
	turnID = e.beginActiveTurn(turnID)
	previousFailures := e.toolFailures
	var sessionDir string
	if e.Session != nil {
		sessionDir = e.Session.Dir
	}
	e.toolFailures = newToolFailureLedger(sessionDir)
	lifecycle := turnLifecycle{
		engine:  e,
		turnID:  turnID,
		userMsg: userMsg,
		start:   time.Now(),
	}
	defer func() {
		e.toolFailures = previousFailures
		if !lifecycle.activeClosed {
			e.finishActiveTurn(turnID, err != nil)
		}
	}()
	var result turnLifecycleResult
	result, err = lifecycle.runLocked(ctx)
	if err != nil {
		return "", e.failTurn(turnID, cancellation.NormalizeError(err))
	}
	return result.output, nil
}

type preparedTurnContext struct {
	promptSections []prompt.Section
	systemPrompt   string
	tools          []llm.ToolSpec
	policy         compactionPolicy
	userMessage    llm.Message
}

type providerTurnRequest struct {
	iter    int
	history []llm.Message
}

type recordedProviderResponse struct {
	finalText  string
	stopReason llm.StopReason
	toolCalls  []llm.Block
}

func (e *Engine) prepareTurnContextLocked(ctx context.Context, turnID string, userMsg llm.Message) (preparedTurnContext, error) {
	userHookReq := e.newHookRequest(hooks.EventUserPromptSubmit, turnID)
	userHookReq.UserInput = userMsg.FirstText()
	userHookResults, err := e.runHooks(ctx, userHookReq)
	if err != nil {
		return preparedTurnContext{}, err
	}
	if denied, reason := hookDenied(userHookResults); denied {
		return preparedTurnContext{}, hookDeniedError(hooks.EventUserPromptSubmit, reason)
	}
	userMsg = appendHookAdditionalContext(userMsg, userHookResults)

	prepared := preparedTurnContext{
		promptSections: e.Prompt.Sections(),
		tools:          e.Tools.Specs(),
		policy:         effectiveCompactionPolicy(e.Compaction, e.ContextWindow),
	}
	prepared.systemPrompt = prompt.JoinSections(prepared.promptSections)
	projectedUserMsg, projection, err := e.projectMessageLocked(userMsg, prepared.policy)
	if err != nil {
		return preparedTurnContext{}, err
	}
	prepared.userMessage = projectedUserMsg
	e.emitProjectionApplied(turnID, projection)

	if err := e.maybeCompact(ctx, turnID, prepared.systemPrompt, prepared.tools, prepared.userMessage); err != nil {
		if !canContinueAfterAutoCompactError(ctx, prepared.userMessage) {
			return preparedTurnContext{}, err
		}
	}
	return prepared, nil
}

func (e *Engine) recordTurnStartLocked(turnID string, userMsg llm.Message) error {
	if err := e.Session.Append(userMsg); err != nil {
		return fmt.Errorf("session append user: %w", err)
	}
	if err := e.markPendingInputMessageProcessed(userMsg); err != nil {
		return fmt.Errorf("mark pending input user processed: %w", err)
	}
	e.emit(events.Event{Type: "turn.started", TurnID: turnID, Payload: TurnStartedPayload{
		Input: userMsg.FirstText(),
		Kind:  userMsg.Kind,
	}})
	return nil
}

func (e *Engine) repairTranscriptLocked(turnID, reason string) error {
	repairs, err := e.Session.RepairTranscript(reason)
	if err != nil {
		return fmt.Errorf("session repair transcript: %w", err)
	}
	if len(repairs) > 0 {
		e.emit(events.Event{Type: "transcript.repaired", TurnID: turnID, Payload: session.TranscriptRepairedPayload{
			Reason:  reason,
			Repairs: repairs,
		}})
	}
	return nil
}

func (e *Engine) prepareProviderRequestLocked(turnID string, iter int, prepared preparedTurnContext) (providerTurnRequest, error) {
	e.emit(events.Event{Type: "llm.requested", TurnID: turnID, Payload: LLMRequestedPayload{
		Iter:       iter,
		HistoryLen: len(e.Session.History),
		ToolCount:  len(prepared.tools),
	}})

	requestHistory := e.activeContextLocked().Messages
	projectedHistory, projection, err := e.projectMessagesForProviderLocked(requestHistory, prepared.policy)
	if err != nil {
		return providerTurnRequest{}, err
	}
	e.emitProjectionApplied(turnID, projection)
	projectedHistory, projection = stripRedactedReasoningForProviderBudget(prepared.systemPrompt, prepared.tools, projectedHistory, prepared.policy)
	e.emitProjectionApplied(turnID, projection)
	return providerTurnRequest{iter: iter, history: projectedHistory}, nil
}

func (e *Engine) requestProviderTurnLocked(ctx context.Context, prepared preparedTurnContext, request providerTurnRequest) (llm.Response, error) {
	return llm.CompleteWithOptions(ctx, e.Provider, prepared.systemPrompt, request.history, prepared.tools, llm.CompleteOptions{
		Purpose:     "turn",
		CachePolicy: e.cachePolicyLocked(),
	})
}

func (e *Engine) recordProviderResponseLocked(turnID string, prepared preparedTurnContext, request providerTurnRequest, resp llm.Response) (recordedProviderResponse, error) {
	msg := resp.Message
	if msg.Model == "" && e.Provider != nil {
		msg.Model = e.Provider.Name()
	}
	msg.Blocks = prepareToolInputs(msg.Blocks, e.Tools)
	var contextUsage *llm.ContextUsage
	if !resp.Usage.IsZero() {
		snapshot := contextUsageSnapshot(msg.Model, e.ContextWindow, resp.Usage, prepared.promptSections, prepared.tools, request.history)
		contextUsage = &snapshot
	}
	totalUsage := e.Session.RecordResponseUsage(resp.Usage, contextUsage)

	// Enrich the responded event with the assistant's text + thinking +
	// tool calls so verbose UIs can render them without subscribing to
	// the conversation log. Bounded by what the LLM returned in this
	// single turn, so payload size is reasonable.
	payload := LLMRespondedPayload{
		Iter:         request.iter,
		StopReason:   resp.StopReason,
		Usage:        resp.Usage,
		TokenUsage:   totalUsage,
		Blocks:       msg.Blocks,
		Text:         responseText(msg),
		Thinking:     responseThinking(msg),
		ToolCalls:    responseToolCalls(msg),
		Model:        msg.Model,
		ContextUsage: contextUsage,
	}
	e.emit(events.Event{Type: "llm.responded", TurnID: turnID, Payload: payload})

	toolCalls := msg.ToolCalls()
	if err := e.Session.Append(msg); err != nil {
		return recordedProviderResponse{}, fmt.Errorf("session append assistant: %w", err)
	}
	return recordedProviderResponse{finalText: msg.FirstText(), stopReason: resp.StopReason, toolCalls: toolCalls}, nil
}

func (e *Engine) recordToolBatchLocked(ctx context.Context, turnID string, policy compactionPolicy, toolCalls []llm.Block) error {
	results := e.runToolCalls(ctx, turnID, toolCalls)
	e.recordToolFailureBatch(turnID, toolCalls, results)
	if err := e.recordWorkingStateToolBatch(toolCalls, results); err != nil {
		return err
	}
	toolResultMsg := llm.Message{Role: llm.RoleUser, Blocks: results}
	projectedToolResultMsg, projection, err := e.projectMessageLocked(toolResultMsg, policy)
	if err != nil {
		return err
	}
	toolResultMsg = projectedToolResultMsg
	e.emitProjectionApplied(turnID, projection)
	if err := e.Session.Append(toolResultMsg); err != nil {
		return fmt.Errorf("session append tool result: %w", err)
	}
	return nil
}

func (e *Engine) recordTurnCompletionLocked(turnID string, start time.Time, lastText string) {
	e.emit(events.Event{Type: "turn.completed", TurnID: turnID, Payload: TurnCompletedPayload{
		DurationMS: time.Since(start).Milliseconds(),
		OutputLen:  len(lastText),
		TokenUsage: e.Session.TokenUsageSnapshot(),
	}})
}

// runToolCalls executes one assistant tool-use batch concurrently while
// preserving provider-facing result order.
func (e *Engine) runToolCalls(ctx context.Context, turnID string, calls []llm.Block) []llm.Block {
	results := make([]llm.Block, len(calls))
	var wg sync.WaitGroup
	for i, tc := range calls {
		wg.Add(1)
		go func(idx int, call llm.Block) {
			defer wg.Done()
			results[idx] = e.runToolCall(ctx, turnID, call)
		}(i, tc)
	}
	wg.Wait()
	return results
}

func (e *Engine) runToolCall(ctx context.Context, turnID string, call llm.Block) llm.Block {
	preReq := e.newHookRequest(hooks.EventPreToolUse, turnID)
	preReq.ToolName = call.ToolName
	preReq.ToolInput = call.Input
	preResults, err := e.runHooks(ctx, preReq)
	if err != nil {
		return e.hookToolErrorBlock(turnID, call, err)
	}
	if denied, reason := hookDenied(preResults); denied {
		return e.hookToolErrorBlock(turnID, call, fmt.Errorf("hooks: tool %q denied%s", call.ToolName, hookReasonSuffix(reason)))
	}

	callPayload := toolCallPayload(call)
	e.emit(events.Event{Type: toolevents.RequestedType, TurnID: turnID, Payload: toolevents.Requested(callPayload)})
	toolCtx := tools.WithToolCallEvents(ctx, tools.ToolCallEvents{
		Name:      call.ToolName,
		ToolUseID: call.ToolUseID,
		Emit: func(delta tools.OutputDelta) {
			e.emit(events.Event{Type: toolevents.OutputDeltaType, TurnID: turnID, Payload: toolevents.Delta(callPayload, delta)})
		},
	})
	out, info, err := e.Tools.CallWithInfo(toolCtx, call.ToolName, call.Input)
	err = cancellation.NormalizeError(err)
	block := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ToolUseID, ToolName: call.ToolName}
	var toolErr error
	if err != nil {
		block.Content = toolErrorContent(out, err)
		block.IsError = true
		toolErr = err
	} else {
		block.Content = out
	}

	postReq := e.newHookRequest(hooks.EventPostToolUse, turnID)
	postReq.ToolName = call.ToolName
	postReq.ToolInput = call.Input
	postReq.ToolResult = block.Content
	postResults, postErr := e.runHooks(ctx, postReq)
	postErr = cancellation.NormalizeError(postErr)
	if postErr != nil {
		block.Content = toolErrorContent(block.Content, postErr)
		block.IsError = true
		toolErr = postErr
	}
	if denied, reason := hookDenied(postResults); denied {
		toolErr = fmt.Errorf("hooks: tool %q denied after use%s", call.ToolName, hookReasonSuffix(reason))
		block.Content = toolErrorContent(block.Content, toolErr)
		block.IsError = true
	}
	e.emitToolFinished(turnID, call, block, out, info, toolErr)
	return block
}

func (e *Engine) emitToolFinished(turnID string, call llm.Block, block llm.Block, out string, info tools.CallInfo, err error) {
	if block.IsError {
		opts := toolevents.ErroredOptions{
			Error:          "tool errored",
			TimeoutSeconds: info.TimeoutSeconds,
		}
		if err != nil {
			opts.Error = err.Error()
		}
		if out != "" {
			opts.Len = len(out)
			opts.Preview = truncate(out, 200)
		}
		if info.TimedOut {
			opts.TimedOut = true
		}
		if code, ok := tools.ExitCodeFromError(err); ok {
			opts.ExitCode = intPtr(code)
		} else if code := firstExitCode(nil, block.Content); code != nil {
			opts.ExitCode = code
		}
		opts.Result = info.StructuredResult
		e.emit(events.Event{Type: toolevents.ErroredType, TurnID: turnID, Payload: toolevents.Errored(toolCallPayload(call), opts)})
		return
	}
	e.emit(events.Event{Type: toolevents.CompletedType, TurnID: turnID, Payload: toolevents.Completed(toolCallPayload(call), info.TimeoutSeconds, len(out), truncate(out, 200), info.StructuredResult)})
}

func (e *Engine) hookToolErrorBlock(turnID string, call llm.Block, err error) llm.Block {
	err = cancellation.NormalizeError(err)
	block := llm.Block{
		Type:      llm.BlockToolResult,
		ToolUseID: call.ToolUseID,
		ToolName:  call.ToolName,
		Content:   err.Error(),
		IsError:   true,
	}
	e.emit(events.Event{Type: toolevents.ErroredType, TurnID: turnID, Payload: toolevents.Errored(toolCallPayload(call), toolevents.ErroredOptions{
		Error: err.Error(),
	})})
	return block
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

func (e *Engine) pendingInputQueueLocked() *PendingInputQueue {
	if e == nil {
		return nil
	}
	if e.PendingInputQueue != nil {
		return e.PendingInputQueue
	}
	if e.Session == nil || e.Session.Dir == "" {
		return nil
	}
	e.PendingInputQueue = NewPendingInputQueue(e.Session.Dir, PendingInputQueueOptions{})
	return e.PendingInputQueue
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

func (e *Engine) sessionHasMessageIDLocked(id string) bool {
	if e == nil || e.Session == nil || id == "" {
		return false
	}
	for _, msg := range e.Session.History {
		if msg.ID == id {
			return true
		}
	}
	return false
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
	if e.activeTurnID == "" {
		e.activeTurnID = turnID
	}
	turnID = e.activeTurnID
	e.pendingMu.Unlock()
	return turnID
}

func (e *Engine) restorePendingInput(turnID, skipMessageID string) error {
	if e == nil || turnID == "" {
		return nil
	}
	e.pendingMu.Lock()
	queue := e.pendingInputQueueLocked()
	if queue == nil {
		e.pendingMu.Unlock()
		return nil
	}
	max := e.effectiveMaxPendingInputs()
	remaining := max - len(e.pendingInput)
	if remaining <= 0 {
		e.pendingMu.Unlock()
		return nil
	}
	records, err := queue.Replayable(turnID, remaining)
	if err != nil {
		e.pendingMu.Unlock()
		return err
	}
	var alreadyProcessed []string
	for _, record := range records {
		if e.hasPendingRecordLocked(record.ID) {
			continue
		}
		if skipMessageID != "" && record.MessageID == skipMessageID {
			continue
		}
		if e.sessionHasMessageIDLocked(record.MessageID) {
			alreadyProcessed = append(alreadyProcessed, record.ID)
			continue
		}
		e.pendingInput = append(e.pendingInput, queuedPendingInput{RecordID: record.ID, Message: record.Message})
	}
	e.pendingMu.Unlock()
	if len(alreadyProcessed) > 0 {
		return queue.MarkProcessed(alreadyProcessed)
	}
	return nil
}

func (e *Engine) markPendingInputMessageProcessed(msg llm.Message) error {
	if e == nil || msg.ID == "" {
		return nil
	}
	e.pendingMu.Lock()
	queue := e.pendingInputQueueLocked()
	e.pendingMu.Unlock()
	if queue == nil {
		return nil
	}
	records, err := queue.Records()
	if err != nil {
		return err
	}
	var ids []string
	for _, record := range records {
		if record.MessageID == msg.ID && isReplayablePendingState(record.State) {
			ids = append(ids, record.ID)
		}
	}
	return queue.MarkProcessed(ids)
}

func (e *Engine) drainPendingInputLocked(ctx context.Context, turnID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.pendingMu.Lock()
	pending := append([]queuedPendingInput(nil), e.pendingInput...)
	e.pendingInput = nil
	max := e.effectiveMaxPendingInputs()
	queue := e.pendingInputQueueLocked()
	e.pendingMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	recordIDs := pendingRecordIDs(pending)
	if queue != nil {
		if err := queue.MarkAdmitted(recordIDs, turnID); err != nil {
			return fmt.Errorf("mark pending input admitted: %w", err)
		}
	}
	var processedIDs []string
	for _, item := range pending {
		msg := item.Message
		if msg.ID != "" && e.sessionHasMessageIDLocked(msg.ID) {
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
		e.emitProjectionApplied(turnID, projection)
		if err := e.Session.Append(msg); err != nil {
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
	e.emit(events.Event{Type: "pending_input.drained", TurnID: turnID, Payload: PendingInputDrainedPayload{
		Count:            len(pending),
		PendingCount:     0,
		MaxPendingInputs: max,
	}})
	return nil
}

func (e *Engine) cachePolicyLocked() llm.CachePolicy {
	if e == nil || e.Session == nil || e.Session.ID == "" {
		return llm.CachePolicy{}
	}
	return llm.CachePolicy{StablePrefixKey: "juex:" + e.Session.ID}
}

func (e *Engine) finishActiveTurnIfNoPending(turnID string) bool {
	e.pendingMu.Lock()
	defer e.pendingMu.Unlock()
	if e.activeTurnID != turnID {
		return true
	}
	if len(e.pendingInput) > 0 {
		return false
	}
	e.activeTurnID = ""
	return true
}

func (e *Engine) finishActiveTurn(turnID string, dropPending bool) {
	e.pendingMu.Lock()
	if e.activeTurnID != turnID {
		e.pendingMu.Unlock()
		return
	}
	e.activeTurnID = ""
	dropped := 0
	var droppedIDs []string
	queue := e.pendingInputQueueLocked()
	if dropPending {
		dropped = len(e.pendingInput)
		droppedIDs = pendingRecordIDs(e.pendingInput)
		e.pendingInput = nil
	}
	max := e.effectiveMaxPendingInputs()
	e.pendingMu.Unlock()
	if queue != nil && len(droppedIDs) > 0 {
		_ = queue.MarkDropped(droppedIDs)
	}
	if dropped > 0 {
		e.emit(events.Event{Type: "pending_input.dropped", TurnID: turnID, Payload: PendingInputDroppedPayload{
			Count:            dropped,
			PendingCount:     0,
			MaxPendingInputs: max,
		}})
	}
}

func (e *Engine) emit(ev events.Event) {
	if e.Bus != nil {
		e.Bus.Emit(ev)
	}
}

func (e *Engine) failTurn(turnID string, err error) error {
	payload := TurnErroredPayload{Error: err.Error()}
	e.emit(events.Event{Type: "turn.errored", TurnID: turnID, Payload: payload})
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
func responseToolCalls(m llm.Message) []toolevents.ToolCallPayload {
	var out []toolevents.ToolCallPayload
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolUse {
			out = append(out, toolCallPayload(b))
		}
	}
	return out
}

func toolCallPayload(call llm.Block) toolevents.ToolCallPayload {
	return toolevents.ToolCallPayload{
		ToolUseID:      call.ToolUseID,
		Name:           call.ToolName,
		Input:          call.Input,
		TimeoutSeconds: call.TimeoutSeconds,
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
	return msg.Kind == llm.MessageKindMCPEvent && ctx.Err() == nil
}

func toolErrorContent(out string, err error) string {
	if out == "" {
		return err.Error()
	}
	if len(out) > maxToolErrorOutput {
		limit := maxToolErrorOutput
		for limit > 0 && (out[limit]&0xC0) == 0x80 {
			limit--
		}
		out = out[:limit] + "\n... (remaining output truncated) ..."
	}
	return strings.TrimRight(out, "\n") + "\n\n[tool error]\n" + err.Error()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated, total " + strconv.Itoa(len(s)) + " bytes)"
}
