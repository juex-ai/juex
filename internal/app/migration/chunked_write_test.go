package migration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func sourceWritePair(kind string) legacy.Commit {
	input := map[string]any{"write_id": "write-one"}
	if kind == "begin" {
		input = map[string]any{"path": "file"}
	}
	data, _ := json.Marshal(map[string]any{"kind": kind, "write_id": "write-one", "path": "file"})
	return legacy.Commit{Facts: []legacy.Fact{
		{Type: "message.appended", Message: &llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "reused", ToolName: "write_" + kind, Input: input}}}},
		{Type: "message.appended", Message: &llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "reused", ToolName: "write_" + kind, IsError: true, ResultFact: &llm.ResultFact{Owner: "chunked-write", Data: data}}}}},
	}}
}

func TestSourceBufferedWriteInventoryDistinguishesResetFromCompaction(t *testing.T) {
	thread := legacy.Thread{Metadata: legacy.ThreadMetadata{ThreadID: "source-thread"}, Commits: []legacy.Commit{sourceWritePair("begin")}}
	thread.Commits = append(thread.Commits, legacy.Commit{Facts: []legacy.Fact{{Type: "context.compacted"}}})
	active, err := InspectSourceBufferedWrites(thread)
	if err != nil || len(active) != 1 || active[0].WriteID != "write-one" {
		t.Fatal(active, err)
	}
	if err := requireSettledSourceWrites(thread); err == nil || !strings.Contains(err.Error(), "source-thread") || !strings.Contains(err.Error(), "write-one") {
		t.Fatal("active source was silently archived", err)
	}
	thread.Commits = append(thread.Commits, sourceWritePair("commit"))
	if err := requireSettledSourceWrites(thread); err != nil {
		t.Fatal("hook rejection undid typed commit", err)
	}
	thread.Commits = append(thread.Commits, sourceWritePair("begin"), legacy.Commit{Facts: []legacy.Fact{{Type: "context.renewed"}}})
	if active, err := InspectSourceBufferedWrites(thread); err != nil || len(active) != 0 {
		t.Fatal("reset retained old buffer", active, err)
	}
}

func TestSourceBufferedWriteInventoryRejectsUnpairedFacts(t *testing.T) {
	pair := sourceWritePair("commit")
	pair.Facts = pair.Facts[1:]
	if _, err := InspectSourceBufferedWrites(legacy.Thread{Commits: []legacy.Commit{pair}}); err == nil {
		t.Fatal("unpaired terminal receipt proved no active buffers")
	}
	pair.Facts[0].Message.Blocks[0].ResultFact = nil
	pair.Facts[0].Message.Blocks[0].Content = `{"kind":"begin","write_id":"not-a-fact"}`
	if active, err := InspectSourceBufferedWrites(legacy.Thread{Commits: []legacy.Commit{pair}}); err != nil || len(active) != 0 {
		t.Fatal("display text treated as authority", active, err)
	}
}
