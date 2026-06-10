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

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	DefaultMaxPendingInput = 16
	maxToolErrorOutput     = 32 * 1024
)

type Engine struct {
	Provider llm.Provider
	Tools    *tools.Registry
	Bus      *events.Bus
	Session  *session.Session
	Prompt   *prompt.Builder
	// MaxPendingInputs caps user or external event messages that can be
	// queued while a turn is active. When omitted, DefaultMaxPendingInput is
	// used. A full queue rejects new input instead of silently dropping it.
	MaxPendingInputs int
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
	pendingInput []llm.Message

	autoCompactFailures int
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
	e.pendingInput = append(e.pendingInput, userMsg)
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
	msg := e.pendingInput[0]
	e.pendingInput[0] = llm.Message{}
	e.pendingInput = e.pendingInput[1:]
	e.activeTurnID = nextTurnID
	return msg, PendingInputStatus{
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
	activeClosed := false
	defer func() {
		if !activeClosed {
			e.finishActiveTurn(turnID, err != nil)
		}
	}()
	start := time.Now()
	turnCtx := ctx

	prepared, err := e.prepareTurnContextLocked(turnCtx, turnID, userMsg)
	if err != nil {
		return "", e.failTurn(turnID, err)
	}

	if err := e.recordTurnStartLocked(turnID, prepared.userMessage); err != nil {
		return "", e.failTurn(turnID, err)
	}

	var lastText string
	retriedOverflow := false
	for iter := 0; ; iter++ {
		if err := turnCtx.Err(); err != nil {
			return "", e.failTurn(turnID, err)
		}

		if err := e.drainPendingInputLocked(turnCtx, turnID); err != nil {
			return "", e.failTurn(turnID, err)
		}

		request, err := e.prepareProviderRequestLocked(turnID, iter, prepared)
		if err != nil {
			return "", e.failTurn(turnID, err)
		}

		resp, err := e.requestProviderTurnLocked(turnCtx, prepared, request)
		if err != nil {
			if llm.IsContextOverflowError(err) && !retriedOverflow {
				if _, compactErr := e.compactLocked(turnCtx, turnID, prepared.systemPrompt, "overflow_retry", true, ""); compactErr != nil {
					return "", e.failTurn(turnID, fmt.Errorf("llm: %w; compact retry failed: %w", err, compactErr))
				}
				retriedOverflow = true
				continue
			}
			return "", e.failTurn(turnID, fmt.Errorf("llm: %w", err))
		}

		recorded, err := e.recordProviderResponseLocked(turnID, prepared, request, resp)
		if err != nil {
			return "", e.failTurn(turnID, err)
		}
		if len(recorded.toolCalls) == 0 {
			lastText = recorded.finalText
			if !e.finishActiveTurnIfNoPending(turnID) {
				continue
			}
			activeClosed = true
			break
		}

		if err := e.recordToolBatchLocked(turnCtx, turnID, prepared.policy, recorded.toolCalls); err != nil {
			return "", e.failTurn(turnID, err)
		}
	}

	e.recordTurnCompletionLocked(turnID, start, lastText)
	return lastText, nil
}

type preparedTurnContext struct {
	promptSections []prompt.Section
	systemPrompt   string
	tools          []llm.ToolSpec
	policy         compactionPolicy
	userMessage    llm.Message
}

type providerTurnRequest struct {
	history []llm.Message
}

type recordedProviderResponse struct {
	finalText string
	toolCalls []llm.Block
}

func (e *Engine) prepareTurnContextLocked(ctx context.Context, turnID string, userMsg llm.Message) (preparedTurnContext, error) {
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
	e.emit(events.Event{Type: "turn.started", TurnID: turnID, Payload: TurnStartedPayload{
		Input: userMsg.FirstText(),
		Kind:  userMsg.Kind,
	}})
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
	return providerTurnRequest{history: projectedHistory}, nil
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
	msg.Blocks = prepareToolInputs(msg.Blocks)
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
	return recordedProviderResponse{finalText: msg.FirstText(), toolCalls: toolCalls}, nil
}

func (e *Engine) recordToolBatchLocked(ctx context.Context, turnID string, policy compactionPolicy, toolCalls []llm.Block) error {
	results := e.runToolCalls(ctx, turnID, toolCalls)
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
	e.emit(events.Event{Type: "tool.requested", TurnID: turnID, Payload: ToolRequestedPayload{
		Name:           call.ToolName,
		Input:          call.Input,
		ToolUseID:      call.ToolUseID,
		TimeoutSeconds: call.TimeoutSeconds,
	}})
	out, info, err := e.Tools.CallWithInfo(ctx, call.ToolName, call.Input)
	block := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ToolUseID, ToolName: call.ToolName}
	if err != nil {
		block.Content = toolErrorContent(out, err)
		block.IsError = true
		payload := ToolErroredPayload{
			Name:           call.ToolName,
			ToolUseID:      call.ToolUseID,
			Error:          err.Error(),
			TimeoutSeconds: info.TimeoutSeconds,
		}
		if out != "" {
			payload.Len = len(out)
			payload.Preview = truncate(out, 200)
		}
		if info.TimedOut {
			payload.TimedOut = true
		}
		e.emit(events.Event{Type: "tool.errored", TurnID: turnID, Payload: payload})
		return block
	}

	block.Content = out
	e.emit(events.Event{Type: "tool.completed", TurnID: turnID, Payload: ToolCompletedPayload{
		Name:           call.ToolName,
		ToolUseID:      call.ToolUseID,
		TimeoutSeconds: info.TimeoutSeconds,
		Len:            len(out),
		// Truncated preview so events.jsonl stays readable for tools that
		// return many KB.
		Preview: truncate(out, 200),
	}})
	return block
}

func (e *Engine) effectiveMaxPendingInputs() int {
	if e.MaxPendingInputs > 0 {
		return e.MaxPendingInputs
	}
	return DefaultMaxPendingInput
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

func (e *Engine) drainPendingInputLocked(ctx context.Context, turnID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.pendingMu.Lock()
	pending := append([]llm.Message(nil), e.pendingInput...)
	e.pendingInput = nil
	max := e.effectiveMaxPendingInputs()
	e.pendingMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	for _, msg := range pending {
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
	if dropPending {
		dropped = len(e.pendingInput)
		e.pendingInput = nil
	}
	max := e.effectiveMaxPendingInputs()
	e.pendingMu.Unlock()
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
func responseToolCalls(m llm.Message) []ToolCallPayload {
	var out []ToolCallPayload
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolUse {
			out = append(out, ToolCallPayload{
				ToolUseID:      b.ToolUseID,
				Name:           b.ToolName,
				Input:          b.Input,
				TimeoutSeconds: b.TimeoutSeconds,
			})
		}
	}
	return out
}

func prepareToolInputs(blocks []llm.Block) []llm.Block {
	if len(blocks) == 0 {
		return blocks
	}
	out := append([]llm.Block(nil), blocks...)
	for i := range out {
		if out[i].Type == llm.BlockToolUse {
			out[i].Input = tools.NormalizeCallInput(out[i].Input)
			out[i].TimeoutSeconds = tools.CallTimeoutSeconds(out[i].Input)
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
