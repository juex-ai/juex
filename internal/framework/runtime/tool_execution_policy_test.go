package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/features/contextcontrol"
	"github.com/juex-ai/juex/internal/features/hooks"
	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/tools"
)

func TestRunToolCalls_DeclaredExecutionPolicy(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			eng, _ := newEngine(t, &mockProvider{}, false)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			started := make(chan string, 3)
			release := make(chan struct{})
			firstFinished := make(chan struct{})
			var secondRan atomic.Bool
			eng.Tools.MustRegister(tools.Tool{Name: "first", Group: tools.ToolGroupThreadState, ExecutionPolicy: tools.ToolExecutionSerial, Handler: func(ctx context.Context, _ map[string]any) (string, error) {
				started <- "first"
				defer close(firstFinished)
				select {
				case <-release:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				if outcome == "failure" {
					return "", errors.New("first failed")
				}
				return "first", nil
			}})
			eng.Tools.MustRegister(tools.Tool{Name: "second", Group: "independent-module", ExecutionPolicy: tools.ToolExecutionSerial, Handler: func(context.Context, map[string]any) (string, error) {
				secondRan.Store(true)
				select {
				case <-firstFinished:
					return "second", nil
				default:
					return "", errors.New("second overtook first")
				}
			}})
			eng.Tools.MustRegister(tools.Tool{Name: "parallel", Group: tools.ToolGroupThreadState, Handler: func(ctx context.Context, _ map[string]any) (string, error) {
				started <- "parallel"
				select {
				case <-release:
					return "parallel", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}})
			calls := []llm.Block{
				{Type: llm.BlockToolUse, ToolUseID: "first", ToolName: "first"},
				{Type: llm.BlockToolUse, ToolUseID: "second", ToolName: "second"},
				{Type: llm.BlockToolUse, ToolUseID: "parallel-1", ToolName: "parallel"},
				{Type: llm.BlockToolUse, ToolUseID: "parallel-2", ToolName: "parallel"},
			}
			done := make(chan []toolCallResult, 1)
			go func() { done <- eng.runToolCalls(ctx, "declared-policy", testToolExecutions(calls)) }()
			for range 3 {
				select {
				case <-started:
				case <-ctx.Done():
					<-done
					t.Fatal("serial and both default-parallel handlers did not overlap")
				}
			}
			if outcome == "cancel" {
				cancel()
			} else {
				close(release)
			}
			var results []toolCallResult
			select {
			case results = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("batch did not finish")
			}
			if secondRan.Load() != (outcome != "cancel") {
				t.Fatalf("queued handler ran=%v for %s", secondRan.Load(), outcome)
			}
			for i, result := range results {
				wantError := outcome == "cancel" || (outcome == "failure" && i == 0)
				if result.FatalError != nil || result.Block.ToolUseID != calls[i].ToolUseID || result.Block.IsError != wantError {
					t.Fatalf("result %d = %+v; want error=%v", i, result, wantError)
				}
			}
		})
	}
}

func TestRunToolCalls_ContextTransitionsFollowProviderOrder(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	installModuleTools(t, eng.Tools, contextcontrol.New(eng))
	installHookRunner(t, eng, hookRunnerFunc(func(ctx context.Context, request hooks.Request) ([]hooks.Result, error) {
		if request.EventName == hooks.EventPreToolUse && request.ToolName == contextcontrol.ToolCompact {
			select {
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, nil
	}))
	results := eng.runToolCalls(t.Context(), "context-order", testToolExecutions([]llm.Block{
		{Type: llm.BlockToolUse, ToolUseID: "compact-first", ToolName: contextcontrol.ToolCompact, Input: map[string]any{}},
		{Type: llm.BlockToolUse, ToolUseID: "new-second", ToolName: contextcontrol.ToolNew, Input: map[string]any{}},
	}))
	if len(results) != 2 || results[0].Block.IsError || !results[1].Block.IsError {
		t.Fatalf("context results=%+v", results)
	}
	transition := eng.takeContextTransition()
	if transition == nil || transition.Kind != runtimemodule.ContextTransitionCompact {
		t.Fatalf("context transition=%+v", transition)
	}
}
