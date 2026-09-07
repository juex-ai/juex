package chunkedwrite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	writefacts "github.com/juex-ai/juex/internal/chunkedwrite"
	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/tools"
)

func startTestModule(t *testing.T, work string, history []llm.Message) (*Module, map[string]tools.Tool) {
	t.Helper()
	m := New(tools.BuiltinOptions{WorkDir: work})
	if err := m.StartThread(t.Context(), runtimemodule.ThreadContext{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.CloseThread(context.Background()) })
	if _, err := m.ApplyThreadStart(t.Context(), runtimemodule.ThreadStartRequest{History: history}); err != nil {
		t.Fatal(err)
	}
	definitions, err := m.Tools(t.Context(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	catalog := map[string]tools.Tool{}
	for _, tool := range definitions {
		catalog[tool.Name] = tool
	}
	return m, catalog
}

func activeTestHistory(content string) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "begin", ToolName: "write_begin", Input: map[string]any{"path": "out.txt"}}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "begin", ResultFact: testResultFact(writefacts.Event{Kind: writefacts.EventBegin, WriteID: "same-id", Path: "out.txt", Mode: writefacts.ModeOverwrite})}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "chunk", ToolName: "write_chunk", Input: map[string]any{"write_id": "same-id", "index": 0, "content": content}}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "chunk", ResultFact: testResultFact(writefacts.Event{Kind: writefacts.EventChunk, WriteID: "same-id", Index: 0})}}},
	}
}

func TestRecoveryAndCloseKeepThreadsIsolated(t *testing.T) {
	firstDir, secondDir := t.TempDir(), t.TempDir()
	first, firstTools := startTestModule(t, firstDir, activeTestHistory("first"))
	_, secondTools := startTestModule(t, secondDir, activeTestHistory("second"))
	if err := first.CloseThread(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := firstTools["write_commit"].ResultHandler(t.Context(), map[string]any{"write_id": "same-id"}); err == nil {
		t.Fatal("closed Thread retained buffered session")
	}
	if _, err := secondTools["write_commit"].ResultHandler(t.Context(), map[string]any{"write_id": "same-id", "expected_chunks": 1}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(secondDir, "out.txt"))
	if err != nil || string(data) != "second" {
		t.Fatalf("second Thread result %q: %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(firstDir, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("closed Thread published a file: %v", err)
	}
}

func TestRecoveryUsesExecutionFactsIncludingPostExecutionErrors(t *testing.T) {
	for _, mode := range []string{"active", "commit", "abort", "foreign", "bad-hash"} {
		t.Run(mode, func(t *testing.T) {
			history := activeTestHistory("content")
			switch mode {
			case "commit", "abort":
				kind := writefacts.EventCommit
				if mode == "abort" {
					kind = writefacts.EventAbort
				}
				history = append(history,
					llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "terminal", ToolName: "write_" + mode}}},
					llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "terminal", IsError: true, Content: "post-execution policy failed", ResultFact: testResultFact(writefacts.Event{Kind: kind, WriteID: "same-id"})}}})
			case "foreign":
				history[1].Blocks[0].ResultFact.Owner = "another-module"
			case "bad-hash":
				history[3].Blocks[0].ResultFact = testResultFact(writefacts.Event{Kind: writefacts.EventChunk, WriteID: "same-id", SHA256: "incorrect"})
			}
			_, catalog := startTestModule(t, t.TempDir(), history)
			_, err := catalog["write_commit"].ResultHandler(t.Context(), map[string]any{"write_id": "same-id", "expected_chunks": 1})
			if (err == nil) != (mode == "active") {
				t.Fatalf("mode=%s commit error=%v", mode, err)
			}
		})
	}
}

func TestNewGenerationDiscardsBufferedSession(t *testing.T) {
	m, catalog := startTestModule(t, t.TempDir(), activeTestHistory("old generation"))
	observer, ok := any(m).(runtimemodule.ContextRenewalObserver)
	if !ok {
		t.Fatal("chunked-write does not observe Generation renewal")
	}
	observer.ContextRenewed(t.Context())
	if _, err := catalog["write_commit"].ResultHandler(t.Context(), map[string]any{"write_id": "same-id"}); err == nil {
		t.Fatal("new Generation retained an old buffered session")
	}
	if _, err := catalog["write_begin"].ResultHandler(t.Context(), map[string]any{"path": "new.txt"}); err != nil {
		t.Fatalf("new Generation cannot start a write: %v", err)
	}
}

func TestRecoveryPairsRepeatedToolIDsInResultOrder(t *testing.T) {
	history := activeTestHistory("first")
	history[2].Blocks = append(history[2].Blocks, llm.Block{Type: llm.BlockToolUse, ToolUseID: "chunk", ToolName: "write_chunk", Input: map[string]any{"write_id": "same-id", "index": 1, "content": "second"}})
	history[3].Blocks = append(history[3].Blocks, llm.Block{Type: llm.BlockToolResult, ToolUseID: "chunk", ResultFact: testResultFact(writefacts.Event{Kind: writefacts.EventChunk, WriteID: "same-id", Index: 1})})
	if err := llm.ValidateToolTranscript(history); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	_, catalog := startTestModule(t, work, history)
	if _, err := catalog["write_commit"].ResultHandler(t.Context(), map[string]any{"write_id": "same-id", "expected_chunks": 2}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(work, "out.txt"))
	if err != nil || string(data) != "firstsecond" {
		t.Fatalf("paired content=%q err=%v", data, err)
	}
}
