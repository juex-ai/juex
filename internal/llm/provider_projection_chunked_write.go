package llm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/chunkedwrite"
)

const providerWriteChunkRecentReplayCount = 4

// FoldChunkedWriteHistoryForProvider removes provider-heavy chunked write
// tool-call pairs from replay while preserving durable conversation history.
// Active sessions keep the most recent chunks visible so the model can
// continue; committed or aborted sessions collapse to a text summary.
func FoldChunkedWriteHistoryForProvider(history []Message) []Message {
	return foldChunkedWriteHistoryForProvider(history)
}

type providerChunkedWriteProjectionPlan struct {
	omitToolCalls map[string]bool
	summaries     map[string]string
}

type providerChunkedWriteSession struct {
	writeID       string
	path          string
	mode          string
	beginCallID   string
	commitCallID  string
	abortCallID   string
	committed     bool
	aborted       bool
	commitBytes   int
	commitChars   int
	commitChunks  int
	commitSHA256  string
	abortedChunks int
	chunkCalls    []string
	chunks        []providerChunkedWriteChunk
}

type providerChunkedWriteChunk struct {
	toolUseID string
	index     int
	bytes     int
	chars     int
}

func foldChunkedWriteHistoryForProvider(history []Message) []Message {
	plan := buildChunkedWriteProjectionPlan(history)
	if len(plan.omitToolCalls) == 0 {
		return history
	}
	out := make([]Message, 0, len(history))
	for _, m := range history {
		projected := m
		projected.Blocks = make([]Block, 0, len(m.Blocks))
		var deferredSummaries []Block
		for _, b := range m.Blocks {
			if isProviderChunkedWriteOmittedBlock(b, plan.omitToolCalls) {
				if b.Type == BlockToolResult {
					if summary := plan.summaries[b.ToolUseID]; summary != "" {
						deferredSummaries = append(deferredSummaries, Block{Type: BlockText, Text: summary})
					}
				}
				continue
			}
			projected.Blocks = append(projected.Blocks, b)
		}
		projected.Blocks = append(projected.Blocks, deferredSummaries...)
		out = append(out, projected)
	}
	return out
}

func isProviderChunkedWriteOmittedBlock(b Block, omitToolCalls map[string]bool) bool {
	switch b.Type {
	case BlockToolUse, BlockToolResult:
		return omitToolCalls[b.ToolUseID]
	default:
		return false
	}
}

func buildChunkedWriteProjectionPlan(history []Message) providerChunkedWriteProjectionPlan {
	toolUses := map[string]Block{}
	toolResults := map[string]Block{}
	for _, m := range history {
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockToolUse:
				toolUses[b.ToolUseID] = b
			case BlockToolResult:
				toolResults[b.ToolUseID] = b
			}
		}
	}

	sessions := map[string]*providerChunkedWriteSession{}
	getSession := func(writeID string) *providerChunkedWriteSession {
		session := sessions[writeID]
		if session == nil {
			session = &providerChunkedWriteSession{writeID: writeID}
			sessions[writeID] = session
		}
		return session
	}

	for toolUseID, use := range toolUses {
		if toolUseID == "" {
			continue
		}
		result, hasResult := toolResults[toolUseID]
		if !hasResult {
			continue
		}
		event := result.ChunkedWrite
		if event == nil || event.WriteID == "" || result.IsError {
			continue
		}
		switch event.Kind {
		case chunkedwrite.EventBegin:
			if result.IsError {
				continue
			}
			writeID := event.WriteID
			if writeID == "" {
				continue
			}
			session := getSession(writeID)
			session.beginCallID = toolUseID
			session.path = providerProjectionFirstNonEmpty(event.Path, providerToolInputString(use.Input, "path"), session.path)
			session.mode = providerProjectionFirstNonEmpty(event.Mode, providerToolInputString(use.Input, "mode"), session.mode)
		case chunkedwrite.EventChunk:
			writeID := event.WriteID
			if writeID == "" {
				continue
			}
			session := getSession(writeID)
			session.chunkCalls = append(session.chunkCalls, toolUseID)
			chunk := providerChunkedWriteChunk{toolUseID: toolUseID, index: event.Index, bytes: event.Bytes, chars: event.Chars}
			if content, ok := use.Input["content"].(string); ok {
				if chunk.bytes == 0 {
					chunk.bytes = len(content)
				}
				if chunk.chars == 0 {
					chunk.chars = utf8.RuneCountInString(content)
				}
			}
			session.chunks = append(session.chunks, chunk)
		case chunkedwrite.EventCommit:
			writeID := event.WriteID
			if writeID == "" {
				continue
			}
			session := getSession(writeID)
			session.commitCallID = toolUseID
			session.committed = true
			session.path = providerProjectionFirstNonEmpty(event.Path, session.path)
			session.commitSHA256 = event.SHA256
			session.commitBytes = event.Bytes
			session.commitChars = event.Chars
			session.commitChunks = event.Chunks
		case chunkedwrite.EventAbort:
			writeID := event.WriteID
			if writeID == "" {
				continue
			}
			session := getSession(writeID)
			session.abortCallID = toolUseID
			session.aborted = true
			session.abortedChunks = event.Chunks
		}
	}

	plan := providerChunkedWriteProjectionPlan{
		omitToolCalls: map[string]bool{},
		summaries:     map[string]string{},
	}
	for _, session := range sessions {
		sort.Slice(session.chunks, func(i, j int) bool {
			if session.chunks[i].index == session.chunks[j].index {
				return session.chunks[i].toolUseID < session.chunks[j].toolUseID
			}
			return session.chunks[i].index < session.chunks[j].index
		})

		switch {
		case session.committed && session.commitCallID != "":
			omitProviderChunkedWriteSession(plan, session, session.commitCallID)
			plan.summaries[session.commitCallID] = providerCommittedChunkedWriteSummary(session)
		case session.aborted && session.abortCallID != "":
			omitProviderChunkedWriteSession(plan, session, session.abortCallID)
			plan.summaries[session.abortCallID] = providerAbortedChunkedWriteSummary(session)
		case len(session.chunks) > providerWriteChunkRecentReplayCount:
			foldCount := len(session.chunks) - providerWriteChunkRecentReplayCount
			folded := session.chunks[:foldCount]
			for _, chunk := range folded {
				plan.omitToolCalls[chunk.toolUseID] = true
			}
			anchor := folded[len(folded)-1].toolUseID
			plan.summaries[anchor] = providerActiveChunkedWriteSummary(session, folded)
		}
	}
	return plan
}

func omitProviderChunkedWriteSession(plan providerChunkedWriteProjectionPlan, session *providerChunkedWriteSession, anchor string) {
	if session.beginCallID != "" {
		plan.omitToolCalls[session.beginCallID] = true
	}
	for _, toolUseID := range session.chunkCalls {
		plan.omitToolCalls[toolUseID] = true
	}
	for _, chunk := range session.chunks {
		plan.omitToolCalls[chunk.toolUseID] = true
	}
	if session.commitCallID != "" {
		plan.omitToolCalls[session.commitCallID] = true
	}
	if session.abortCallID != "" {
		plan.omitToolCalls[session.abortCallID] = true
	}
	plan.omitToolCalls[anchor] = true
}

func providerCommittedChunkedWriteSummary(session *providerChunkedWriteSession) string {
	parts := []string{
		"Chunked write provider replay summary:",
		"committed",
		kv("write_id", session.writeID),
		kv("path", session.path),
		kvInt("chunks", session.commitChunks),
		kvInt("bytes", session.commitBytes),
		kvInt("chars", session.commitChars),
		kv("sha256", session.commitSHA256),
		"folded_tool_calls=write_begin,write_chunk,write_commit",
		"note=full content is on disk; use read/edit/apply_patch if needed",
	}
	return strings.Join(nonEmptyParts(parts), " ")
}

func providerAbortedChunkedWriteSummary(session *providerChunkedWriteSession) string {
	parts := []string{
		"Chunked write provider replay summary:",
		"aborted",
		kv("write_id", session.writeID),
		kv("path", session.path),
		kvInt("chunks", session.abortedChunks),
		"folded_tool_calls=write_begin,write_chunk,write_abort",
		"note=aborted content was discarded",
	}
	return strings.Join(nonEmptyParts(parts), " ")
}

func providerActiveChunkedWriteSummary(session *providerChunkedWriteSession, folded []providerChunkedWriteChunk) string {
	var bytesTotal, charsTotal int
	indices := make([]int, 0, len(folded))
	for _, chunk := range folded {
		bytesTotal += chunk.bytes
		charsTotal += chunk.chars
		indices = append(indices, chunk.index)
	}
	nextIndex := maxProviderProjectionInt(indices) + 1
	parts := []string{
		"Chunked write provider replay summary:",
		"active",
		kv("write_id", session.writeID),
		kv("path", session.path),
		kvInt("folded_chunks", len(folded)),
		kv("folded_indices", providerIndexRange(indices)),
		kvInt("folded_bytes", bytesTotal),
		kvInt("folded_chars", charsTotal),
		kvInt("next_index", nextIndex),
		fmt.Sprintf("recent_chunks_visible=%d", providerWriteChunkRecentReplayCount),
		"note=continue with the next chunk index; recent chunk content remains visible",
	}
	return strings.Join(nonEmptyParts(parts), " ")
}

func providerToolInputString(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

func providerIndexRange(indices []int) string {
	if len(indices) == 0 {
		return ""
	}
	sort.Ints(indices)
	if len(indices) == 1 {
		return strconv.Itoa(indices[0])
	}
	contiguous := true
	for i := 1; i < len(indices); i++ {
		if indices[i] != indices[i-1]+1 {
			contiguous = false
			break
		}
	}
	if contiguous {
		return fmt.Sprintf("%d-%d", indices[0], indices[len(indices)-1])
	}
	parts := make([]string, 0, len(indices))
	for _, index := range indices {
		parts = append(parts, strconv.Itoa(index))
	}
	return strings.Join(parts, ",")
}

func maxProviderProjectionInt(values []int) int {
	max := -1
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func kv(key, value string) string {
	if value == "" {
		return ""
	}
	return key + "=" + value
}

func kvInt(key string, value int) string {
	if value == 0 {
		return ""
	}
	return fmt.Sprintf("%s=%d", key, value)
}

func nonEmptyParts(parts []string) []string {
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func providerProjectionFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
