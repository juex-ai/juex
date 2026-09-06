package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

type factToolModule struct{ testTool tools.Tool }

func (*factToolModule) ID() runtimemodule.ID { return "fact-owner" }
func (m *factToolModule) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return []tools.Tool{m.testTool}, nil
}

type runtimeHistoryModule struct {
	summary string
	cancel  context.CancelFunc
}

func (*runtimeHistoryModule) ID() runtimemodule.ID { return "history-owner" }
func (m *runtimeHistoryModule) ProjectProviderHistory(_ context.Context, pairs []runtimemodule.ToolResultPair, _ runtimemodule.ProviderHistoryBudget) (runtimemodule.ProviderHistoryPlan, error) {
	if m.cancel != nil {
		m.cancel()
	}
	if len(pairs) == 0 {
		return runtimemodule.ProviderHistoryPlan{}, nil
	}
	return runtimemodule.ProviderHistoryPlan{Omit: []string{pairs[0].Use.ToolUseID}, Summaries: []runtimemodule.ToolSummary{{ToolUseID: pairs[0].Use.ToolUseID, Text: m.summary}}}, nil
}

func TestProviderHistoryContributionUsesRuntimeBudgetAndCancellation(t *testing.T) {
	for _, mode := range []string{"bounded", "oversized", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			provider := &mockProvider{script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn}}}
			engine, _ := newEngine(t, provider, false)
			engine.ToolOutput = ToolOutputPolicy{InlineMaxBytes: 64}
			owner := &runtimeHistoryModule{summary: "Owned operation completed."}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "oversized" {
				owner.summary = strings.Repeat("x", 65)
			}
			if mode == "canceled" {
				owner.cancel = cancel
			}
			installRuntimeTestModules(t, engine, owner)
			history := []llm.Message{
				{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "historical", ToolName: "custom_tool", Input: map[string]any{"content": "original arguments"}}}},
				{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "historical", Content: "original result", ResultFact: &llm.ResultFact{Owner: "history-owner"}}}},
			}
			for _, message := range history {
				if err := engine.Thread.Append(message); err != nil {
					t.Fatal(err)
				}
			}
			_, err := engine.Turn(ctx, "Continue.")
			if mode == "bounded" {
				if err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(provider.histories)
				if !strings.Contains(string(encoded), owner.summary) || strings.Contains(string(encoded), "original arguments") {
					t.Fatalf("provider history: %s", encoded)
				}
			} else {
				if err == nil || provider.called != 0 {
					t.Fatalf("invalid projection reached provider: calls=%d err=%v", provider.called, err)
				}
			}
			encoded, _ := json.Marshal(engine.Thread.History)
			if !strings.Contains(string(encoded), "original arguments") || !strings.Contains(string(encoded), "original result") {
				t.Fatal("projection changed durable facts")
			}
		})
	}
}

func TestTurnPersistsOwnedExecutionFactsAcrossResultPolicies(t *testing.T) {
	for _, mode := range []string{"allow", "error", "transform", "deny-before", "handler-error"} {
		t.Run(mode, func(t *testing.T) {
			prov := &mockProvider{script: []llm.Response{
				{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "call", ToolName: "fact_tool", Input: map[string]any{}}}}, StopReason: llm.StopToolUse},
				{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
			}}
			eng, _ := newEngine(t, prov, false)
			called := false
			fact := json.RawMessage(`{"kind":"committed","id":"resource"}`)
			owner := &factToolModule{testTool: tools.Tool{Name: "fact_tool", Schema: map[string]any{"type": "object"}, ResultHandler: func(context.Context, map[string]any) (tools.Result, error) {
				called = true
				if mode == "handler-error" {
					return tools.Result{Text: "failed before execution"}, errors.New("operation failed")
				}
				return tools.Result{Text: "original presentation", Fact: fact}, nil
			}}}
			policy := &runtimeToolPolicyModule{id: "presentation", apply: func(request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
				if mode == "deny-before" && request.Stage == runtimemodule.ToolPolicyBeforeExecution {
					return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyDeny, Reason: "blocked"}, nil
				}
				if request.Stage == runtimemodule.ToolPolicyAfterExecution {
					switch mode {
					case "error":
						return runtimemodule.ToolPolicyDecision{}, errors.New("presentation failed")
					case "transform":
						return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyTransform, Result: runtimemodule.ToolPolicyResult{Content: "replacement presentation"}}, nil
					}
				}
				return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
			}}
			installRuntimeTestModules(t, eng, owner, policy)
			reg, err := runtimemodule.BuildToolRegistry(tools.RegistryOptions{}, eng.RuntimeModules)
			if err != nil {
				t.Fatal(err)
			}
			eng.Tools = reg
			if _, err := eng.Turn(t.Context(), "Execute the operation."); err != nil {
				t.Fatal(err)
			}
			var result llm.Block
			for _, message := range eng.Thread.History {
				for _, block := range message.Blocks {
					if block.Type == llm.BlockToolResult && block.ToolUseID == "call" {
						result = block
					}
				}
			}
			if result.ResultFact == nil || result.ResultFact.Owner != "fact-owner" {
				t.Fatalf("owner=%+v", result.ResultFact)
			}
			if mode == "deny-before" || mode == "handler-error" {
				if len(result.ResultFact.Data) != 0 {
					t.Fatal("failed execution fabricated a fact")
				}
			} else if string(result.ResultFact.Data) != string(fact) {
				t.Fatalf("fact=%s", result.ResultFact.Data)
			}
			if mode == "deny-before" && called {
				t.Fatal("denied tool executed")
			}
			if mode == "transform" && !strings.Contains(result.Content, "replacement presentation") {
				t.Fatalf("transformed content=%s", result.Content)
			}
			if err := llm.ValidateToolTranscript(eng.Thread.History); err != nil {
				t.Fatal(err)
			}
		})
	}
}
