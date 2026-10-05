package managedruntime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func validImport(t *testing.T) (string, AgentImport) {
	t.Helper()
	agent, thread := uuid.NewString(), uuid.NewString()
	at := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	message := llm.TextMessage(llm.RoleUser, "retained")
	message.ID = uuid.NewString()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return agent, AgentImport{Source: "fixed-source/agent", SourceSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Threads: []ImportedThread{{
		Thread: Thread{ID: thread, AgentID: agent, Kind: "main", Name: "Main", Retention: "active", State: "idle", Generation: 1, Sequence: 1, CreatedAt: at, UpdatedAt: at},
		Events: []Event{{ID: uuid.NewString(), ThreadID: thread, Sequence: 1, Generation: 1, Kind: "message.appended", Data: encoded, CreatedAt: at}}, Context: []string{message.ID},
		Inputs: []ImportedInput{{InputReceipt: InputReceipt{ID: uuid.NewString(), ThreadID: thread, RequestID: "terminal", State: "completed", AcceptedAt: at}, Text: "finished"}},
	}}}
}

func TestAgentImportRejectsIncompleteContextOrLiveWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*AgentImport)
	}{
		{"missing message", func(v *AgentImport) { v.Threads[0].Context[0] = uuid.NewString() }},
		{"duplicate context reference", func(v *AgentImport) { v.Threads[0].Context = append(v.Threads[0].Context, v.Threads[0].Context[0]) }},
		{"cross-thread event", func(v *AgentImport) { v.Threads[0].Events[0].ThreadID = uuid.NewString() }},
		{"sequence gap", func(v *AgentImport) { v.Threads[0].Events[0].Sequence++ }},
		{"unfinished input", func(v *AgentImport) { v.Threads[0].Inputs[0].State = "queued" }},
		{"running Thread", func(v *AgentImport) { v.Threads[0].Thread.State = "running" }},
		{"different Agent", func(v *AgentImport) { v.Threads[0].Thread.AgentID = uuid.NewString() }},
		{"orphan Worker", func(v *AgentImport) {
			v.Threads[0].Thread.Kind = "worker"
			v.Threads[0].Thread.ParentID = uuid.NewString()
		}},
		{"unbound application purpose", func(v *AgentImport) { v.Threads[0].Thread.Application = "memory" }},
		{"incomplete tool pair", func(v *AgentImport) {
			m := llm.Message{ID: v.Threads[0].Context[0], Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "pending", ToolName: "shell"}}}
			v.Threads[0].Events[0].Data, _ = json.Marshal(m)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent, value := validImport(t)
			if err := value.Validate(agent); err != nil {
				t.Fatal("invalid initial fixture", err)
			}
			test.change(&value)
			if err := value.Validate(agent); err == nil {
				t.Fatal("unsafe import accepted")
			}
		})
	}
}

func TestAgentImportPreservesEmptyRenewedContext(t *testing.T) {
	agent, value := validImport(t)
	value.Threads[0].Thread.Generation = 2
	value.Threads[0].Context = nil
	if err := value.Validate(agent); err != nil {
		t.Fatal("empty renewal context rejected", err)
	}
}
