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
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type reusableMemoryProvider struct{}

func (*reusableMemoryProvider) Name() string { return "memory-reuse-fixture" }
func (*reusableMemoryProvider) Complete(_ context.Context, _ string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	var proposal mc.Proposal
	var scope mc.Scope
	assignments, results := 0, 0
	for _, msg := range history {
		if _, payload, ok := strings.Cut(msg.FirstText(), "Proposal JSON:\n"); ok {
			assignments++
			if err := json.Unmarshal([]byte(payload), &proposal); err != nil {
				return llm.Response{}, err
			}
			_, raw, _ := strings.Cut(msg.FirstText(), "Proposal context JSON:\n")
			raw, _, _ = strings.Cut(raw, "\n\nProposal JSON:")
			if err := json.Unmarshal([]byte(raw), &scope); err != nil {
				return llm.Response{}, err
			}
		}
		for _, b := range msg.Blocks {
			if b.Type == llm.BlockToolResult {
				if b.IsError {
					return llm.Response{}, fmt.Errorf("Memory tool: %s", b.Content)
				}
				results++
			}
		}
	}
	if assignments != 1 {
		return llm.Response{}, fmt.Errorf("assignment context count %d, want 1", assignments)
	}
	if len(tools) != 6 {
		return llm.Response{}, fmt.Errorf("maintenance tools %d, want 6", len(tools))
	}
	for _, tool := range tools {
		if tool.Name != memory.ToolDomains && tool.Name != memory.ToolFacts && tool.Name != memory.ToolSearch && tool.Name != memory.ToolRead && tool.Name != memory.ToolHistory && tool.Name != memory.ToolDecide {
			return llm.Response{}, fmt.Errorf("unexpected tool %s", tool.Name)
		}
	}
	response := llm.Response{Usage: llm.Usage{InputTokens: 10, OutputTokens: 2}}
	// Six Provider requests per assignment exceed one shared eight-call budget.
	if results < 4 {
		response.Message = llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{memoryCall(fmt.Sprint("search-", results), memory.ToolSearch, map[string]any{"query": proposal.Text})}}
		response.StopReason = llm.StopToolUse
	} else if results == 4 {
		decision := mc.Decision{Outcome: "applied", Reason: "Explicit preference", Changes: []mc.Change{{Entry: mc.Entry{ID: proposal.Key, Name: proposal.Key, Summary: proposal.Text, Body: proposal.Text, Type: "reference", Sources: proposal.Sources, Scope: scope}}}}
		raw, _ := json.Marshal(decision)
		var input map[string]any
		_ = json.Unmarshal(raw, &input)
		response.Message = llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{memoryCall("decision", memory.ToolDecide, input)}}
		response.StopReason = llm.StopToolUse
	} else {
		response.Message = llm.TextMessage(llm.RoleAssistant, "Committed")
		response.StopReason = llm.StopEndTurn
	}
	return response, nil
}

func TestEndToEnd_MemoryWorkerReusesIdentityWithFreshAssignments(t *testing.T) {
	isolateModuleConfig(t)
	home := t.TempDir()
	api, user := startMemoryFixture(t, home, mc.Basic)
	cfg := memoryAgentConfig(t, home, "supervisor")
	cfg.MemoryProfile = mc.ProfileSupervisor
	cfg.Modules["worker-threads"] = config.ModuleSettings{Enabled: true}
	supervisor := memoryApp(t, cfg, &reusableMemoryProvider{})
	var firstID, archivedID string
	var previous *mc.Assignment
	for i := 1; i <= 5; i++ {
		caller := user
		caller.Scope.Workspace = fmt.Sprintf("/workspace/%d", i)
		source := mc.Source{FleetID: user.FleetID, AgentID: user.AgentID, ThreadID: user.ThreadID, GenerationID: "g000001", From: uint64(i), Through: uint64(i)}
		sourceAPI := mc.New(serviceendpoint.FileResolver{Home: home, Fleet: user.FleetID}, "memory", caller)
		receipt, err := sourceAPI.Propose(t.Context(), caller, mc.Proposal{Key: fmt.Sprintf("preference-%d", i), Text: fmt.Sprintf("Preference %d", i), Reason: "Explicit request", Sources: []mc.Source{source}})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(15 * time.Second)
		var workerID string
		for {
			receipt, err = api.Result(t.Context(), user, receipt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.State == "failed" || receipt.Attempts > 1 {
				t.Fatalf("assignment failed: %+v", receipt)
			}
			workers, err := supervisor.Workers().List()
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Committed {
				for _, w := range workers {
					assignment, err := memory.LoadWorkerAssignment(cfg.AgentStateDir, w.ThreadID)
					if err != nil {
						t.Fatal(err)
					}
					if assignment != nil && assignment.ID == receipt.ID && w.State == agent.WorkerThreadStateIdle {
						workerID = w.ThreadID
						break
					}
				}
			}
			if workerID != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("assignment never settled: %+v", receipt)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if firstID == "" {
			if workerID == archivedID {
				t.Fatal("archived Worker was reused")
			}
			firstID = workerID
		} else if workerID != firstID {
			t.Fatalf("created Worker %s for assignment %d; want reuse %s", workerID, i, firstID)
		}
		assignment, err := memory.LoadWorkerAssignment(cfg.AgentStateDir, workerID)
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && (assignment.Token == previous.Token || assignment.ID == previous.ID) {
			t.Fatal("assignment authority was reused")
		}
		previous = assignment
		entries, err := supervisor.ThreadStore.List()
		if err != nil {
			t.Fatal(err)
		}
		turns := i
		if i > 3 {
			turns = i - 3
		}
		for _, entry := range entries {
			if entry.ThreadID == workerID && (entry.TurnCount != turns || entry.GenerationCount != turns || entry.TokenUsage.Total.InputTokens != 60*turns) {
				t.Fatalf("history/usage not retained: %+v", entry)
			}
		}
		if i == 3 {
			if err := supervisor.Workers().Archive(t.Context(), firstID); err != nil {
				t.Fatal(err)
			}
			archivedID, firstID = firstID, ""
		}
	}
	inspected, err := supervisor.ThreadStore.Inspect(archivedID)
	if err != nil || inspected.Projection.RetentionState != thread.RetentionArchived {
		t.Fatalf("archive: %+v %v", inspected, err)
	}
}
