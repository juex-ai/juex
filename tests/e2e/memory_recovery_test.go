package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/features/memory"
	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

type correctingMemoryProvider struct{}

func (*correctingMemoryProvider) Name() string { return "correcting-memory-fixture" }
func (*correctingMemoryProvider) Complete(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	var proposal mc.Proposal
	for _, message := range history {
		if _, raw, ok := strings.Cut(message.FirstText(), "Proposal JSON:\n"); ok {
			if err := json.Unmarshal([]byte(raw), &proposal); err != nil {
				return llm.Response{}, err
			}
		}
	}
	id, callID := "birthday:example", "invalid-entry"
	for _, message := range history {
		for _, block := range message.Blocks {
			if block.Type != llm.BlockToolResult {
				continue
			}
			switch block.ToolUseID {
			case "invalid-entry":
				if !block.IsError || !strings.Contains(block.Content, "memory entry ID") || !strings.Contains(block.Content, "1-64") {
					return llm.Response{}, fmt.Errorf("cannot correct entry from tool feedback: %s", block.Content)
				}
				id, callID = "birthday-example", "corrected-entry"
			case "corrected-entry":
				var receipt mc.Receipt
				if err := json.Unmarshal([]byte(block.Content), &receipt); err != nil || block.IsError || !receipt.Committed {
					return llm.Response{}, fmt.Errorf("correction did not commit: %s", block.Content)
				}
				return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Committed after correcting the ID"), StopReason: llm.StopEndTurn}, nil
			}
		}
	}
	decision := mc.Decision{Outcome: "applied", Reason: "Explicit durable preference", Changes: []mc.Change{{Entry: mc.Entry{ID: id, Name: "Example preference", Summary: proposal.Text, Type: "reference", Body: proposal.Text, Sources: proposal.Sources}}}}
	raw, err := json.Marshal(decision)
	if err != nil {
		return llm.Response{}, err
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return llm.Response{}, err
	}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{memoryCall(callID, memory.ToolDecide, input)}}, StopReason: llm.StopToolUse}, nil
}

func TestEndToEnd_MemoryWorkerCorrectsDecisionWithoutNewAttempt(t *testing.T) {
	isolateModuleConfig(t)
	home := t.TempDir()
	api, user := startMemoryFixture(t, home, mc.Basic)
	source := mc.Source{FleetID: user.FleetID, AgentID: user.AgentID, ThreadID: user.ThreadID, GenerationID: "g000001", From: 1, Through: 1}
	receipt, err := api.Propose(t.Context(), user, mc.Proposal{Key: "birthday:example", Text: "Keep release notes concise", Reason: "Explicit user request", Sources: []mc.Source{source}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := memoryAgentConfig(t, home, "supervisor")
	cfg.MemoryProfile = mc.ProfileSupervisor
	cfg.Modules["worker-threads"] = config.ModuleSettings{Enabled: true}
	supervisor := memoryApp(t, cfg, &correctingMemoryProvider{})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for {
		receipt, err = api.Result(ctx, user, receipt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Attempts > 1 || receipt.State == "failed" {
			t.Fatalf("correction required another Worker: %+v", receipt)
		}
		if receipt.Committed {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Worker did not correct decision: %+v", receipt)
		case <-time.After(25 * time.Millisecond):
		}
	}
	if got, err := api.Read(ctx, user, mc.ReadRequest{ID: "birthday-example"}); err != nil || got.Body != "Keep release notes concise" || got.Revision != 1 {
		t.Fatalf("committed knowledge: %+v %v", got, err)
	}
	workers, err := supervisor.ThreadStore.List()
	if err != nil || len(workers) != 2 {
		t.Fatalf("expected Main and one Worker: %+v %v", workers, err)
	}
}
