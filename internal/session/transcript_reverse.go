package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/juex-ai/juex/internal/llm"
)

func transcriptMessagePageFromCheckpoint(
	path string,
	checkpoint *transcriptCheckpoint,
	beforeID string,
	limit int,
) (MessagePage, bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return MessagePage{}, false, err
	}
	if !transcriptCheckpointValid(checkpoint, fingerprintFromFileInfo(st)) {
		return MessagePage{}, false, nil
	}
	if checkpoint.LatestCompact != nil {
		entry := checkpointIndexEntry(*checkpoint.LatestCompact)
		messages, err := readTranscriptMessages(path, []transcriptIndexEntry{entry})
		if err != nil || len(messages) != 1 || messages[0].ID != entry.ID || messages[0].Kind != llm.MessageKindCompact {
			return MessagePage{}, false, nil
		}
	}
	page, err := reverseTranscriptMessagePage(path, checkpoint, beforeID, limit)
	if err != nil {
		return MessagePage{}, false, nil
	}
	return page, true, nil
}

func reverseTranscriptMessagePage(path string, checkpoint *transcriptCheckpoint, beforeID string, limit int) (MessagePage, error) {
	file, err := os.Open(path)
	if err != nil {
		return MessagePage{}, err
	}
	defer file.Close()

	floor := int64(0)
	if beforeID == "" && checkpoint != nil && checkpoint.LatestCompact != nil {
		floor = checkpoint.LatestCompact.Offset
	}
	reader, err := newUncappedReverseLineReaderAt(file, floor)
	if err != nil {
		return MessagePage{}, err
	}
	searching := beforeID != ""
	reversed := make([]llm.Message, 0, max(limit, 0))
	hasMore := false

	for {
		line, err := reader.next()
		if errors.Is(err, io.EOF) {
			if searching {
				return MessagePage{}, fmt.Errorf("%w: %s", ErrBeforeMessageNotFound, beforeID)
			}
			hasMore = floor > 0
			break
		}
		if err != nil {
			return MessagePage{}, err
		}
		message, err := decodeReverseTranscriptMessage(path, line)
		if err != nil {
			return MessagePage{}, err
		}
		if searching {
			if message.ID == beforeID {
				searching = false
			}
			continue
		}
		if limit <= 0 || len(reversed) < limit {
			reversed = append(reversed, message)
			continue
		}

		required := pageStartToolResultIDs(reversed)
		if len(required) == 0 {
			hasMore = true
			break
		}
		switch {
		case message.Kind == llm.MessageKindHookEvent:
			reversed = append(reversed, message)
		case message.Kind == llm.MessageKindToolResult:
			reversed = append(reversed, message)
		case message.Role == llm.RoleAssistant && message.Kind == "" && messageHasAnyToolUse(message, required):
			reversed = append(reversed, message)
			hasMore = floor > 0 || reverseReaderHasMore(reader)
			goto complete
		default:
			hasMore = true
			goto complete
		}
	}

complete:
	messages := make([]llm.Message, len(reversed))
	for i := range reversed {
		messages[len(reversed)-1-i] = reversed[i]
	}
	oldestID := ""
	if len(messages) > 0 {
		oldestID = messages[0].ID
	}
	return MessagePage{Messages: messages, HasMoreBefore: hasMore, OldestMessageID: oldestID}, nil
}

func decodeReverseTranscriptMessage(path string, line []byte) (llm.Message, error) {
	var message llm.Message
	if err := json.Unmarshal(line, &message); err != nil {
		return llm.Message{}, fmt.Errorf("session: parse %s from tail: %w", path, err)
	}
	return normalizeLoadedMessage(path, 1, message)
}

// reversed is ordered newest to oldest. Only a provider-visible Tool result
// sequence at the oldest page edge requires extending the page.
func pageStartToolResultIDs(reversed []llm.Message) map[string]struct{} {
	required := make(map[string]struct{})
	for i := len(reversed) - 1; i >= 0; i-- {
		message := reversed[i]
		if message.Kind == llm.MessageKindHookEvent {
			continue
		}
		if message.Kind != llm.MessageKindToolResult {
			break
		}
		for _, block := range message.Blocks {
			if block.Type == llm.BlockToolResult && block.ToolUseID != "" {
				required[block.ToolUseID] = struct{}{}
			}
		}
	}
	return required
}

func messageHasAnyToolUse(message llm.Message, required map[string]struct{}) bool {
	for _, block := range message.Blocks {
		if block.Type != llm.BlockToolUse {
			continue
		}
		if _, ok := required[block.ToolUseID]; ok {
			return true
		}
	}
	return false
}

func reverseReaderHasMore(reader *reverseLineReader) bool {
	if reader == nil {
		return false
	}
	_, err := reader.next()
	return !errors.Is(err, io.EOF)
}
