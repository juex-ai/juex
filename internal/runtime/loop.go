// Package runtime implements the synchronous turn loop that drives a single
// user input through repeated LLM calls + tool dispatches until the model
// stops requesting tools.
//
// Behaviour highlights:
//
//   - System prompt is rebuilt every turn from prompt.Builder so memory,
//     skills, and AGENTS.md changes propagate immediately.
//   - tool_use blocks within a single LLM response run in parallel; results
//     are collected and reattached to history in the original order.
//   - A budget of MaxIters tool/llm round-trips and a MaxDur wall-clock cap
//     guard against runaway loops.
//   - Every state transition emits an event with a stable TurnID so
//     downstream consumers (session jsonl, future hooks) can stitch a
//     transcript.
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

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	defaultMaxIters        = 25
	defaultMaxDur          = 5 * time.Minute
	DefaultMaxPendingInput = 16
)

type Engine struct {
	Provider llm.Provider
	Tools    *tools.Registry
	Bus      *events.Bus
	Session  *session.Session
	Prompt   *prompt.Builder
	MaxIters int
	MaxDur   time.Duration
	// MaxPendingInputs caps user or external event messages that can be
	// queued while a turn is active. When omitted, DefaultMaxPendingInput is
	// used. A full queue rejects new input instead of silently dropping it.
	MaxPendingInputs int
	// ContextWindow is the provider context window in tokens. When omitted,
	// the engine uses DefaultContextWindowTokens.
	ContextWindow int
	Compaction    config.CompactionConfig

	// mu serializes turns for one Engine. MCP notifications can arrive while
	// a user turn is running, and both paths append to the same session
	// history; queuing them preserves the provider-facing transcript order.
	mu sync.Mutex

	pendingMu    sync.Mutex
	activeTurnID string
	pendingInput []llm.Message
}

var (
	ErrNoActiveTurn          = errors.New("runtime: no active turn accepting pending input")
	ErrPendingInputQueueFull = errors.New("runtime: pending input queue full")
)

type PendingInputStatus struct {
	TurnID           string `json:"turn_id,omitempty"`
	PendingCount     int    `json:"pending_count"`
	MaxPendingInputs int    `json:"max_pending_inputs"`
}

// Turn drives one user input to completion. The returned string is the final
// assistant text response (concatenated text blocks). Returns an error on
// budget breach or context cancellation.
func (e *Engine) Turn(ctx context.Context, userInput string) (string, error) {
	return e.TurnMessage(ctx, llm.TextMessage(llm.RoleUser, userInput))
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
		return status, ErrPendingInputQueueFull
	}
	e.pendingInput = append(e.pendingInput, userMsg)
	status.PendingCount = len(e.pendingInput)
	e.pendingMu.Unlock()
	e.emit(events.Event{Type: "pending_input.queued", TurnID: turnID, Payload: map[string]any{
		"input":              userMsg.FirstText(),
		"kind":               userMsg.Kind,
		"pending_count":      status.PendingCount,
		"max_pending_inputs": status.MaxPendingInputs,
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

// TurnMessage drives one already-constructed user message to completion.
// It exists for system-originated user turns, such as MCP channel events,
// that need app metadata while still reaching the provider as normal text.
func (e *Engine) TurnMessage(ctx context.Context, userMsg llm.Message) (out string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	maxIters := e.MaxIters
	if maxIters <= 0 {
		maxIters = defaultMaxIters
	}
	maxDur := e.MaxDur
	if maxDur <= 0 {
		maxDur = defaultMaxDur
	}

	turnID := newID()
	e.beginActiveTurn(turnID)
	activeClosed := false
	defer func() {
		if !activeClosed {
			e.finishActiveTurn(turnID, err != nil)
		}
	}()
	start := time.Now()
	turnCtx, cancel := context.WithTimeout(ctx, maxDur)
	defer cancel()

	promptSections := e.Prompt.Sections()
	systemPrompt := prompt.JoinSections(promptSections)
	tools := e.Tools.Specs()

	if err := e.maybeCompact(turnCtx, turnID, systemPrompt, tools, userMsg); err != nil {
		return "", e.failTurn(turnID, err)
	}

	if err := e.Session.Append(userMsg); err != nil {
		return "", e.failTurn(turnID, fmt.Errorf("session append user: %w", err))
	}
	startPayload := map[string]any{"input": userMsg.FirstText()}
	if userMsg.Kind != "" {
		startPayload["kind"] = userMsg.Kind
	}
	e.emit(events.Event{Type: "turn.started", TurnID: turnID, Payload: startPayload})

	var lastText string
	retriedOverflow := false
	for iter := 0; iter < maxIters; iter++ {
		select {
		case <-turnCtx.Done():
			return "", e.failTurn(turnID, fmt.Errorf("turn budget exceeded: %w", turnCtx.Err()))
		default:
		}

		if err := e.drainPendingInputLocked(turnCtx, turnID); err != nil {
			return "", e.failTurn(turnID, err)
		}

		e.emit(events.Event{Type: "llm.requested", TurnID: turnID, Payload: map[string]any{
			"iter": iter, "history_len": len(e.Session.History), "tool_count": len(tools),
		}})

		requestHistory := e.activeContextLocked().Messages
		resp, err := e.Provider.Complete(turnCtx, systemPrompt, requestHistory, tools)
		if err != nil {
			if llm.IsContextOverflowError(err) && !retriedOverflow {
				if _, compactErr := e.compactLocked(turnCtx, turnID, systemPrompt, "overflow_retry", true); compactErr != nil {
					return "", e.failTurn(turnID, fmt.Errorf("llm: %w; compact retry failed: %w", err, compactErr))
				}
				retriedOverflow = true
				continue
			}
			return "", e.failTurn(turnID, fmt.Errorf("llm: %w", err))
		}
		msg := resp.Message
		if msg.Model == "" && e.Provider != nil {
			msg.Model = e.Provider.Name()
		}
		var contextUsage *llm.ContextUsage
		if !resp.Usage.IsZero() {
			snapshot := contextUsageSnapshot(msg.Model, e.ContextWindow, resp.Usage, promptSections, tools, requestHistory)
			contextUsage = &snapshot
		}
		totalUsage := e.Session.RecordResponseUsage(resp.Usage, contextUsage)

		// Enrich the responded event with the assistant's text + thinking +
		// tool calls so verbose UIs can render them without subscribing to
		// the conversation log. Bounded by what the LLM returned in this
		// single turn, so payload size is reasonable.
		payload := map[string]any{
			"stop_reason": resp.StopReason,
			"usage":       resp.Usage,
			"token_usage": totalUsage,
			"text":        responseText(resp.Message),
			"thinking":    responseThinking(resp.Message),
			"tool_calls":  responseToolCalls(resp.Message),
			"model":       msg.Model,
		}
		if contextUsage != nil {
			payload["context_usage"] = *contextUsage
		}
		e.emit(events.Event{Type: "llm.responded", TurnID: turnID, Payload: payload})

		toolCalls := msg.ToolCalls()
		if err := e.Session.Append(msg); err != nil {
			return "", e.failTurn(turnID, fmt.Errorf("session append assistant: %w", err))
		}
		if len(toolCalls) == 0 {
			lastText = msg.FirstText()
			if !e.finishActiveTurnIfNoPending(turnID) {
				if iter == maxIters-1 {
					return "", e.failTurn(turnID, fmt.Errorf("turn iterations exceeded (%d)", maxIters))
				}
				continue
			}
			activeClosed = true
			break
		}

		// Run tool calls in parallel; preserve order in the resulting blocks.
		results := make([]llm.Block, len(toolCalls))
		var wg sync.WaitGroup
		for i, tc := range toolCalls {
			wg.Add(1)
			go func(idx int, call llm.Block) {
				defer wg.Done()
				e.emit(events.Event{Type: "tool.requested", TurnID: turnID, Payload: map[string]any{
					"name": call.ToolName, "input": call.Input, "tool_use_id": call.ToolUseID,
				}})
				out, err := e.Tools.Call(turnCtx, call.ToolName, call.Input)
				block := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ToolUseID}
				if err != nil {
					block.Content = err.Error()
					block.IsError = true
					e.emit(events.Event{Type: "tool.errored", TurnID: turnID, Payload: map[string]any{
						"name": call.ToolName, "error": err.Error(),
					}})
				} else {
					block.Content = out
					e.emit(events.Event{Type: "tool.completed", TurnID: turnID, Payload: map[string]any{
						"name":        call.ToolName,
						"tool_use_id": call.ToolUseID,
						"len":         len(out),
						// Truncated preview so events.jsonl stays readable
						// for tools that return many KB.
						"preview": truncate(out, 200),
					}})
				}
				results[idx] = block
			}(i, tc)
		}
		wg.Wait()

		toolResultMsg := llm.Message{Role: llm.RoleUser, Blocks: results}
		if err := e.Session.Append(toolResultMsg); err != nil {
			return "", e.failTurn(turnID, fmt.Errorf("session append tool result: %w", err))
		}

		if iter == maxIters-1 {
			return "", e.failTurn(turnID, fmt.Errorf("turn iterations exceeded (%d)", maxIters))
		}
	}

	e.emit(events.Event{Type: "turn.completed", TurnID: turnID, Payload: map[string]any{
		"duration_ms": time.Since(start).Milliseconds(),
		"output_len":  len(lastText),
		"token_usage": e.Session.TokenUsageSnapshot(),
	}})
	return lastText, nil
}

func (e *Engine) effectiveMaxPendingInputs() int {
	if e.MaxPendingInputs > 0 {
		return e.MaxPendingInputs
	}
	return DefaultMaxPendingInput
}

func (e *Engine) beginActiveTurn(turnID string) {
	e.pendingMu.Lock()
	e.activeTurnID = turnID
	e.pendingMu.Unlock()
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
		if err := e.Session.Append(msg); err != nil {
			return fmt.Errorf("session append pending input: %w", err)
		}
	}
	e.emit(events.Event{Type: "pending_input.drained", TurnID: turnID, Payload: map[string]any{
		"count":              len(pending),
		"pending_count":      0,
		"max_pending_inputs": max,
	}})
	return nil
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
		e.emit(events.Event{Type: "pending_input.dropped", TurnID: turnID, Payload: map[string]any{
			"count":              dropped,
			"pending_count":      0,
			"max_pending_inputs": max,
		}})
	}
}

func (e *Engine) emit(ev events.Event) {
	if e.Bus != nil {
		e.Bus.Emit(ev)
	}
}

func (e *Engine) failTurn(turnID string, err error) error {
	e.emit(events.Event{Type: "turn.errored", TurnID: turnID, Payload: map[string]any{"error": err.Error()}})
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
func responseToolCalls(m llm.Message) []map[string]any {
	var out []map[string]any
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolUse {
			out = append(out, map[string]any{
				"tool_use_id": b.ToolUseID,
				"name":        b.ToolName,
				"input":       b.Input,
			})
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated, total " + strconv.Itoa(len(s)) + " bytes)"
}
