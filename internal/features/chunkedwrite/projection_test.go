package chunkedwrite

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	writefacts "github.com/juex-ai/juex/internal/chunkedwrite"
	"github.com/juex-ai/juex/internal/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/tools"
)

func testResultFact(event writefacts.Event) *llm.ResultFact {
	data, _ := json.Marshal(event)
	return &llm.ResultFact{Owner: string(ModuleID), Data: data}
}
func projectTestHistory(t *testing.T, history []llm.Message) []llm.Message {
	t.Helper()
	for i := range history {
		for j := range history[i].Blocks {
			block := &history[i].Blocks[j]
			if block.Type == llm.BlockToolResult && block.ResultFact == nil {
				block.ResultFact = &llm.ResultFact{Owner: string(ModuleID)}
			}
		}
	}
	tc := runtimemodule.ThreadContext{ID: "test", Dir: t.TempDir()}
	set, err := runtimemodule.BuildAndStartThreadSet(t.Context(), []runtimemodule.ThreadFactorySpec{{ID: ModuleID, Enabled: true, New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
		return New(tools.BuiltinOptions{WorkDir: t.TempDir()}), nil
	}}}, tc, runtimemodule.ToolContext{Thread: &tc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.CloseThread(context.Background()) })
	projected, err := runtimemodule.ProjectProviderHistory(t.Context(), history, runtimemodule.ProviderHistoryBudget{MaxBytes: 1 << 20, MaxTokens: 1 << 20, EstimateTokens: func(text string) int { return len(text) }}, set)
	if err != nil {
		t.Fatal(err)
	}
	return projected
}

func TestProjectProviderTranscriptFoldsCommittedChunkedWriteSession(t *testing.T) {
	const writeID = "write-committed"
	chunks := []string{
		strings.Repeat("alpha ", 20),
		strings.Repeat("beta ", 20),
		strings.Repeat("gamma ", 20),
	}
	history := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "begin_1",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "reports/long.md", "mode": "create"},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "begin_1",
			Content:   "write_begin: write_id=" + writeID + " path=reports/long.md mode=create max_chunk_bytes=4000 max_chunk_chars=2000 recommended_chunk_bytes=4000 recommended_chunk_chars=2000",
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventBegin,
				WriteID: writeID,
				Path:    "reports/long.md",
				Mode:    writefacts.ModeCreate,
			}),
		}}},
	}
	for i, chunk := range chunks {
		toolUseID := fmt.Sprintf("chunk_%d", i)
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: toolUseID,
				ToolName:  "write_chunk",
				Input:     map[string]any{"write_id": writeID, "index": i, "content": chunk},
			}}},
			llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{
				Type:      llm.BlockToolResult,
				ToolUseID: toolUseID,
				Content:   fmt.Sprintf("write_chunk: write_id=%s index=%d bytes=%d chars=%d sha256=hash-%d chunks=%d duplicate=false", writeID, i, len(chunk), len(chunk), i, i+1),
				ResultFact: testResultFact(writefacts.Event{
					Kind:    writefacts.EventChunk,
					WriteID: writeID,
					Index:   i,
					Bytes:   len(chunk),
					Chars:   len(chunk),
					SHA256:  fmt.Sprintf("hash-%d", i),
					Chunks:  i + 1,
				}),
			}}},
		)
	}
	history = append(history,
		llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "commit_1",
			ToolName:  "write_commit",
			Input:     map[string]any{"write_id": writeID, "expected_chunks": len(chunks)},
		}}},
		llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "commit_1",
			Content:   "write_commit: write_id=write-committed path=reports/long.md bytes=320 chars=320 chunks=3 sha256=final-hash",
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventCommit,
				WriteID: writeID,
				Path:    "reports/long.md",
				Bytes:   320,
				Chars:   320,
				Chunks:  3,
				SHA256:  "final-hash",
			}),
		}}},
	)

	projected := projectTestHistory(t, history)
	text := providerProjectionDebugString(projected)
	if !strings.Contains(text, "Chunked write provider replay summary: committed") {
		t.Fatalf("projected history missing committed summary:\n%s", text)
	}
	if !strings.Contains(text, "path=reports/long.md") || !strings.Contains(text, "sha256=final-hash") {
		t.Fatalf("committed summary missing file metadata:\n%s", text)
	}
	for _, chunk := range chunks {
		if strings.Contains(text, chunk) {
			t.Fatalf("committed projection should fold raw chunk content %q:\n%s", chunk, text)
		}
	}
	if got := providerProjectionToolUseNames(projected); len(got) != 0 {
		t.Fatalf("committed projection should omit chunked write tool calls, got %+v", got)
	}
	if strings.Contains(text, "content_omitted") {
		t.Fatalf("committed projection should not use fake tool arguments:\n%s", text)
	}
}

func TestProjectProviderTranscriptFoldsCommittedChunkedWriteFromLifecycleFacts(t *testing.T) {
	const writeID = "write-fact"
	chunks := []string{
		strings.Repeat("alpha ", 20),
		strings.Repeat("beta ", 20),
	}
	history := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "begin_fact",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "reports/fact.md", "mode": "create"},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "begin_fact",
			Content:   "presentation text changed",
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventBegin,
				WriteID: writeID,
				Path:    "reports/fact.md",
				Mode:    writefacts.ModeCreate,
			}),
		}}},
	}
	for i, chunk := range chunks {
		toolUseID := fmt.Sprintf("chunk_fact_%d", i)
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: toolUseID,
				ToolName:  "write_chunk",
				Input:     map[string]any{"write_id": writeID, "index": i, "content": chunk},
			}}},
			llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{
				Type:      llm.BlockToolResult,
				ToolUseID: toolUseID,
				Content:   "chunk accepted",
				ResultFact: testResultFact(writefacts.Event{
					Kind:    writefacts.EventChunk,
					WriteID: writeID,
					Index:   i,
					Bytes:   len(chunk),
					Chars:   len(chunk),
					Chunks:  i + 1,
				}),
			}}},
		)
	}
	history = append(history,
		llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "commit_fact",
			ToolName:  "write_commit",
			Input:     map[string]any{"write_id": writeID, "expected_chunks": len(chunks)},
		}}},
		llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "commit_fact",
			Content:   "commit presentation changed",
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventCommit,
				WriteID: writeID,
				Path:    "reports/fact.md",
				Bytes:   111,
				Chars:   111,
				Chunks:  len(chunks),
				SHA256:  "full-hash",
			}),
		}}},
	)

	projected := projectTestHistory(t, history)
	text := providerProjectionDebugString(projected)
	if !strings.Contains(text, "Chunked write provider replay summary: committed") {
		t.Fatalf("projected history missing committed summary from facts:\n%s", text)
	}
	if !strings.Contains(text, "path=reports/fact.md") || !strings.Contains(text, "sha256=full-hash") {
		t.Fatalf("summary missing fact metadata:\n%s", text)
	}
	for _, chunk := range chunks {
		if strings.Contains(text, chunk) {
			t.Fatalf("fact projection should fold raw chunk content %q:\n%s", chunk, text)
		}
	}
}

func TestProjectProviderTranscriptFoldsFailedChunkAttemptsAfterCommit(t *testing.T) {
	const writeID = "write-failed-chunk"
	failedContent := strings.Repeat("oversized secret chunk ", 20)
	validContent := "final content"
	history := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "begin_failed_chunk",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "reports/failed-chunk.md", "mode": "create"},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "begin_failed_chunk",
			Content:   "begin ok",
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventBegin,
				WriteID: writeID,
				Path:    "reports/failed-chunk.md",
				Mode:    writefacts.ModeCreate,
			}),
		}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "chunk_failed",
			ToolName:  "write_chunk",
			Input:     map[string]any{"write_id": writeID, "index": 0, "content": failedContent},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "chunk_failed",
			Content:   "write_chunk: content exceeds max chunk limits",
			IsError:   true,
		}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "chunk_valid",
			ToolName:  "write_chunk",
			Input:     map[string]any{"write_id": writeID, "index": 0, "content": validContent},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "chunk_valid",
			Content:   "chunk accepted",
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventChunk,
				WriteID: writeID,
				Index:   0,
				Bytes:   len(validContent),
				Chars:   len(validContent),
				Chunks:  1,
			}),
		}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "commit_failed_chunk",
			ToolName:  "write_commit",
			Input:     map[string]any{"write_id": writeID, "expected_chunks": 1},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "commit_failed_chunk",
			Content:   "commit ok",
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventCommit,
				WriteID: writeID,
				Path:    "reports/failed-chunk.md",
				Bytes:   len(validContent),
				Chars:   len(validContent),
				Chunks:  1,
				SHA256:  "final-hash",
			}),
		}}},
	}

	projected := projectTestHistory(t, history)
	text := providerProjectionDebugString(projected)
	if !strings.Contains(text, "Chunked write provider replay summary: committed") {
		t.Fatalf("projected history missing committed summary:\n%s", text)
	}
	if strings.Contains(text, failedContent) || strings.Contains(text, validContent) {
		t.Fatalf("committed projection should fold failed and valid chunk content:\n%s", text)
	}
	if got := providerProjectionToolUseNames(projected); len(got) != 0 {
		t.Fatalf("committed projection should omit all chunked write tool calls, got %+v", got)
	}
}

func TestProjectProviderTranscriptDoesNotParsePlainTextChunkedWriteResults(t *testing.T) {
	const writeID = "plain-text-write"
	history := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "begin_text",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "reports/plain.md", "mode": "create"},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "begin_text",
			Content:   "write_begin: write_id=" + writeID + " path=reports/plain.md mode=create",
		}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "commit_text",
			ToolName:  "write_commit",
			Input:     map[string]any{"write_id": writeID, "expected_chunks": 0},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "commit_text",
			Content:   "write_commit: write_id=plain-text-write path=reports/plain.md bytes=0 chars=0 chunks=0 sha256=text",
		}}},
	}

	projected := projectTestHistory(t, history)
	text := providerProjectionDebugString(projected)
	if strings.Contains(text, "Chunked write provider replay summary") {
		t.Fatalf("plain-text results should not be parsed as lifecycle state:\n%s", text)
	}
	if got := providerProjectionToolUseNames(projected); len(got) != 2 {
		t.Fatalf("plain-text lifecycle tool calls should remain visible, got %+v", got)
	}
}

func TestProjectProviderTranscriptFoldsOnlyOldActiveChunkedWriteChunks(t *testing.T) {
	const writeID = "write-active"
	history := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "begin_1",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "drafts/live.md", "mode": "overwrite"},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "begin_1",
			Content:   "write_begin: write_id=" + writeID + " path=drafts/live.md mode=overwrite max_chunk_bytes=4000 max_chunk_chars=2000 recommended_chunk_bytes=4000 recommended_chunk_chars=2000",
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventBegin,
				WriteID: writeID,
				Path:    "drafts/live.md",
				Mode:    writefacts.ModeOverwrite,
			}),
		}}},
	}
	chunks := make([]string, 0, providerWriteChunkRecentReplayCount+2)
	for i := 0; i < providerWriteChunkRecentReplayCount+2; i++ {
		chunk := strings.Repeat(fmt.Sprintf("chunk-%d ", i), 10)
		chunks = append(chunks, chunk)
		toolUseID := fmt.Sprintf("chunk_%d", i)
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: toolUseID,
				ToolName:  "write_chunk",
				Input:     map[string]any{"write_id": writeID, "index": i, "content": chunk},
			}}},
			llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{
				Type:      llm.BlockToolResult,
				ToolUseID: toolUseID,
				Content:   fmt.Sprintf("write_chunk: write_id=%s index=%d bytes=%d chars=%d sha256=hash-%d chunks=%d duplicate=false", writeID, i, len(chunk), len(chunk), i, i+1),
				ResultFact: testResultFact(writefacts.Event{
					Kind:    writefacts.EventChunk,
					WriteID: writeID,
					Index:   i,
					Bytes:   len(chunk),
					Chars:   len(chunk),
					SHA256:  fmt.Sprintf("hash-%d", i),
					Chunks:  i + 1,
				}),
			}}},
		)
	}

	projected := projectTestHistory(t, history)
	text := providerProjectionDebugString(projected)
	if !strings.Contains(text, "Chunked write provider replay summary: active") {
		t.Fatalf("projected history missing active summary:\n%s", text)
	}
	if !strings.Contains(text, "folded_chunks=2") || !strings.Contains(text, "next_index=2") {
		t.Fatalf("active summary missing fold metadata:\n%s", text)
	}
	for _, oldChunk := range chunks[:2] {
		if strings.Contains(text, oldChunk) {
			t.Fatalf("active projection should fold old chunk content %q:\n%s", oldChunk, text)
		}
	}
	for _, recentChunk := range chunks[2:] {
		if !strings.Contains(text, recentChunk) {
			t.Fatalf("active projection should keep recent chunk content %q:\n%s", recentChunk, text)
		}
	}
	if got := providerProjectionToolUseNames(projected); strings.Count(strings.Join(got, ","), "write_chunk") != providerWriteChunkRecentReplayCount {
		t.Fatalf("active projection should keep %d recent write_chunk calls, got %+v", providerWriteChunkRecentReplayCount, got)
	}
	if !strings.Contains(text, "write_begin") || !strings.Contains(text, writeID) {
		t.Fatalf("active projection should keep begin context and write_id:\n%s", text)
	}
	if strings.Contains(text, "content_omitted") {
		t.Fatalf("active projection should not use fake tool arguments:\n%s", text)
	}
}

func TestProjectProviderTranscriptDoesNotFoldUnresolvedChunkedWriteCommit(t *testing.T) {
	const writeID = "write-unresolved"
	chunks := []string{
		strings.Repeat("alpha ", 20),
		strings.Repeat("beta ", 20),
	}
	history := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "begin_1",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "drafts/unresolved.md", "mode": "overwrite"},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "begin_1",
			Content:   "write_begin: write_id=" + writeID + " path=drafts/unresolved.md mode=overwrite max_chunk_bytes=4000 max_chunk_chars=2000 recommended_chunk_bytes=4000 recommended_chunk_chars=2000",
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventBegin,
				WriteID: writeID,
				Path:    "drafts/unresolved.md",
				Mode:    writefacts.ModeOverwrite,
			}),
		}}},
	}
	for i, chunk := range chunks {
		toolUseID := fmt.Sprintf("chunk_%d", i)
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: toolUseID,
				ToolName:  "write_chunk",
				Input:     map[string]any{"write_id": writeID, "index": i, "content": chunk},
			}}},
			llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{
				Type:      llm.BlockToolResult,
				ToolUseID: toolUseID,
				Content:   fmt.Sprintf("write_chunk: write_id=%s index=%d bytes=%d chars=%d sha256=hash-%d chunks=%d duplicate=false", writeID, i, len(chunk), len(chunk), i, i+1),
				ResultFact: testResultFact(writefacts.Event{
					Kind:    writefacts.EventChunk,
					WriteID: writeID,
					Index:   i,
					Bytes:   len(chunk),
					Chars:   len(chunk),
					SHA256:  fmt.Sprintf("hash-%d", i),
					Chunks:  i + 1,
				}),
			}}},
		)
	}
	history = append(history, llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
		Type:      llm.BlockToolUse,
		ToolUseID: "commit_1",
		ToolName:  "write_commit",
		Input:     map[string]any{"write_id": writeID, "expected_chunks": len(chunks)},
	}}})

	projected := projectTestHistory(t, history)
	text := providerProjectionDebugString(projected)
	if strings.Contains(text, "Chunked write provider replay summary: committed") {
		t.Fatalf("unresolved commit should not be folded as committed:\n%s", text)
	}
	for _, chunk := range chunks {
		if !strings.Contains(text, chunk) {
			t.Fatalf("unresolved commit should keep prior chunk content %q:\n%s", chunk, text)
		}
	}
	if got := providerProjectionToolUseNames(projected); !slices.Contains(got, "write_commit") {
		t.Fatalf("unresolved commit tool call should remain visible, got %+v", got)
	}
}

func TestProjectProviderTranscriptDefersActiveChunkedWriteSummaryUntilToolResultsComplete(t *testing.T) {
	const writeID = "write-batch"
	history := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "begin_1",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "drafts/batch.md", "mode": "overwrite"},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "begin_1",
			Content:   "write_begin: write_id=" + writeID + " path=drafts/batch.md mode=overwrite max_chunk_bytes=4000 max_chunk_chars=2000 recommended_chunk_bytes=4000 recommended_chunk_chars=2000",
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventBegin,
				WriteID: writeID,
				Path:    "drafts/batch.md",
				Mode:    writefacts.ModeOverwrite,
			}),
		}}},
		{Role: llm.RoleAssistant},
		{Role: llm.RoleUser},
	}
	for i := 0; i < providerWriteChunkRecentReplayCount+2; i++ {
		chunk := strings.Repeat(fmt.Sprintf("batch-%d ", i), 10)
		toolUseID := fmt.Sprintf("chunk_%d", i)
		history[2].Blocks = append(history[2].Blocks, llm.Block{
			Type:      llm.BlockToolUse,
			ToolUseID: toolUseID,
			ToolName:  "write_chunk",
			Input:     map[string]any{"write_id": writeID, "index": i, "content": chunk},
		})
		history[3].Blocks = append(history[3].Blocks, llm.Block{
			Type:      llm.BlockToolResult,
			ToolUseID: toolUseID,
			Content:   fmt.Sprintf("write_chunk: write_id=%s index=%d bytes=%d chars=%d sha256=hash-%d chunks=%d duplicate=false", writeID, i, len(chunk), len(chunk), i, i+1),
			ResultFact: testResultFact(writefacts.Event{
				Kind:    writefacts.EventChunk,
				WriteID: writeID,
				Index:   i,
				Bytes:   len(chunk),
				Chars:   len(chunk),
				SHA256:  fmt.Sprintf("hash-%d", i),
				Chunks:  i + 1,
			}),
		})
	}

	projected := projectTestHistory(t, history)
	if err := llm.ValidateToolTranscript(projected); err != nil {
		t.Fatalf("projected transcript should remain valid after active chunk folding: %v\n%s", err, providerProjectionDebugString(projected))
	}

	var userBlocks []llm.Block
	for _, message := range projected {
		if message.Role == llm.RoleUser {
			userBlocks = message.Blocks
		}
	}
	if len(userBlocks) != providerWriteChunkRecentReplayCount+1 {
		t.Fatalf("projected user blocks = %d, want %d: %+v", len(userBlocks), providerWriteChunkRecentReplayCount+1, userBlocks)
	}
	for i := 0; i < providerWriteChunkRecentReplayCount; i++ {
		if userBlocks[i].Type != llm.BlockToolResult {
			t.Fatalf("retained tool_result %d should precede summary: %+v", i, userBlocks)
		}
	}
	last := userBlocks[len(userBlocks)-1]
	if last.Type != llm.BlockText || !strings.Contains(last.Text, "Chunked write provider replay summary: active") {
		t.Fatalf("active summary should be deferred after retained tool_results: %+v", userBlocks)
	}
}

func providerProjectionDebugString(messages []llm.Message) string {
	var out strings.Builder
	for _, message := range messages {
		fmt.Fprintf(&out, "role=%s\n", message.Role)
		for _, block := range message.Blocks {
			switch block.Type {
			case llm.BlockText:
				fmt.Fprintf(&out, "text=%s\n", block.Text)
			case llm.BlockToolUse:
				fmt.Fprintf(&out, "tool_use=%s id=%s input=%+v\n", block.ToolName, block.ToolUseID, block.Input)
			case llm.BlockToolResult:
				fmt.Fprintf(&out, "tool_result id=%s error=%t content=%s\n", block.ToolUseID, block.IsError, block.Content)
			default:
				fmt.Fprintf(&out, "block=%s text=%s content=%s input=%+v\n", block.Type, block.Text, block.Content, block.Input)
			}
		}
	}
	return out.String()
}

func providerProjectionToolUseNames(messages []llm.Message) []string {
	var out []string
	for _, message := range messages {
		for _, block := range message.Blocks {
			if block.Type == llm.BlockToolUse {
				out = append(out, block.ToolName)
			}
		}
	}
	return out
}

func TestProviderIndexRangeDoesNotCollapseDuplicateIndices(t *testing.T) {
	if got := providerIndexRange([]int{0, 2, 2}); got != "0,2,2" {
		t.Fatalf("providerIndexRange duplicate indices = %q, want 0,2,2", got)
	}
}
