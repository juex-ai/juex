package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

type SourceBufferedWrite struct {
	WriteID string `json:"write_id"`
	Path    string `json:"path"`
	Chunks  int    `json:"chunks"`
}

// InspectSourceBufferedWrites is a read-only inventory of the pinned source
// journal since /new. Compaction seeds are projections, not new lifecycle facts.
// Never turn preserved historical facts into a claim that active bytes migrated.
func InspectSourceBufferedWrites(thread legacy.Thread) ([]SourceBufferedWrite, error) {
	pending := map[string][]llm.Block{}
	active := map[string]SourceBufferedWrite{}
	chunks := map[string]map[int]bool{}
	for _, commit := range thread.Commits {
		for _, fact := range commit.Facts {
			if fact.Type == "context.renewed" {
				pending = map[string][]llm.Block{}
				active = map[string]SourceBufferedWrite{}
				chunks = map[string]map[int]bool{}
			}
			if fact.Message == nil {
				continue
			}
			for _, block := range fact.Message.Blocks {
				if block.Type == llm.BlockToolUse {
					pending[block.ToolUseID] = append(pending[block.ToolUseID], block)
					continue
				}
				if block.Type != llm.BlockToolResult {
					continue
				}
				uses := pending[block.ToolUseID]
				var use llm.Block
				if len(uses) > 0 {
					use = uses[0]
					pending[block.ToolUseID] = uses[1:]
				}
				if block.ResultFact == nil || block.ResultFact.Owner != "chunked-write" {
					continue
				}
				var event struct {
					Kind    string `json:"kind"`
					WriteID string `json:"write_id"`
					Path    string `json:"path"`
					Index   int    `json:"index"`
					SHA256  string `json:"sha256"`
				}
				if json.Unmarshal(block.ResultFact.Data, &event) != nil || event.WriteID == "" || use.ToolName != "write_"+event.Kind {
					return nil, fmt.Errorf("source Thread %s has an unpaired or invalid buffered-write fact", thread.Metadata.ThreadID)
				}
				if event.Kind != "begin" && use.Input["write_id"] != event.WriteID {
					return nil, fmt.Errorf("source Thread %s write handle differs from its receipt", thread.Metadata.ThreadID)
				}
				switch event.Kind {
				case "begin":
					if event.Path == "" {
						return nil, fmt.Errorf("source Thread %s buffered write lacks target identity", thread.Metadata.ThreadID)
					}
					active[event.WriteID] = SourceBufferedWrite{WriteID: event.WriteID, Path: event.Path}
					chunks[event.WriteID] = map[int]bool{}
				case "chunk":
					content, ok := use.Input["content"].(string)
					sum := sha256.Sum256([]byte(content))
					if !ok || event.Index < 0 || event.SHA256 != "" && !strings.EqualFold(event.SHA256, hex.EncodeToString(sum[:])) {
						return nil, fmt.Errorf("source Thread %s buffered chunk content is unproven", thread.Metadata.ThreadID)
					}
					if _, ok := active[event.WriteID]; !ok {
						active[event.WriteID] = SourceBufferedWrite{WriteID: event.WriteID}
						chunks[event.WriteID] = map[int]bool{}
					}
					chunks[event.WriteID][event.Index] = true
				case "commit", "abort":
					delete(active, event.WriteID)
					delete(chunks, event.WriteID)
				default:
					return nil, fmt.Errorf("source Thread %s has an unknown buffered-write lifecycle fact", thread.Metadata.ThreadID)
				}
			}
		}
	}
	result := make([]SourceBufferedWrite, 0, len(active))
	for id, value := range active {
		value.Chunks = len(chunks[id])
		result = append(result, value)
	}
	slices.SortFunc(result, func(a, b SourceBufferedWrite) int { return strings.Compare(a.WriteID, b.WriteID) })
	return result, nil
}

func requireSettledSourceWrites(thread legacy.Thread) error {
	active, err := InspectSourceBufferedWrites(thread)
	if err != nil {
		return err
	}
	if len(active) > 0 {
		ids := make([]string, 0, len(active))
		for _, value := range active {
			ids = append(ids, value.WriteID)
		}
		return fmt.Errorf("source Thread %s has %d active buffered writes (%s); explicit staged-session migration or source completion is required", thread.Metadata.ThreadID, len(active), strings.Join(ids, ","))
	}
	return nil
}
