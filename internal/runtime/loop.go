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
	"fmt"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	defaultMaxIters = 25
	defaultMaxDur   = 5 * time.Minute
)

type Engine struct {
	Provider llm.Provider
	Tools    *tools.Registry
	Bus      *events.Bus
	Session  *session.Session
	Prompt   *prompt.Builder
	MaxIters int
	MaxDur   time.Duration
}

// Turn drives one user input to completion. The returned string is the final
// assistant text response (concatenated text blocks). Returns an error on
// budget breach or context cancellation.
func (e *Engine) Turn(ctx context.Context, userInput string) (string, error) {
	maxIters := e.MaxIters
	if maxIters <= 0 {
		maxIters = defaultMaxIters
	}
	maxDur := e.MaxDur
	if maxDur <= 0 {
		maxDur = defaultMaxDur
	}

	turnID := newID()
	start := time.Now()
	turnCtx, cancel := context.WithTimeout(ctx, maxDur)
	defer cancel()

	e.emit(events.Event{Type: "turn.started", TurnID: turnID, Payload: map[string]any{"input": userInput}})

	userMsg := llm.TextMessage(llm.RoleUser, userInput)
	if err := e.Session.Append(userMsg); err != nil {
		return "", e.failTurn(turnID, fmt.Errorf("session append user: %w", err))
	}

	systemPrompt := e.Prompt.Build()
	tools := e.Tools.Specs()

	var lastText string
	for iter := 0; iter < maxIters; iter++ {
		select {
		case <-turnCtx.Done():
			return "", e.failTurn(turnID, fmt.Errorf("turn budget exceeded: %w", turnCtx.Err()))
		default:
		}

		e.emit(events.Event{Type: "llm.requested", TurnID: turnID, Payload: map[string]any{
			"iter": iter, "history_len": len(e.Session.History), "tool_count": len(tools),
		}})

		resp, err := e.Provider.Complete(turnCtx, systemPrompt, e.Session.History, tools)
		if err != nil {
			return "", e.failTurn(turnID, fmt.Errorf("llm: %w", err))
		}

		e.emit(events.Event{Type: "llm.responded", TurnID: turnID, Payload: map[string]any{
			"stop_reason": resp.StopReason, "usage": resp.Usage,
		}})

		// Always persist the assistant response, even if it is empty (so
		// the conversation log shows what the model returned).
		if err := e.Session.Append(resp.Message); err != nil {
			return "", e.failTurn(turnID, fmt.Errorf("session append assistant: %w", err))
		}

		toolCalls := resp.Message.ToolCalls()
		if len(toolCalls) == 0 {
			lastText = resp.Message.FirstText()
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
						"name": call.ToolName, "len": len(out),
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
	}})
	return lastText, nil
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
