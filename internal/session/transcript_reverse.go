package session

import (
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
	snapshot, err := openTranscriptSnapshot(path)
	if err != nil {
		return MessagePage{}, false, err
	}
	defer snapshot.close()
	if !transcriptCheckpointMatchesSnapshot(checkpoint, snapshot) {
		return MessagePage{}, false, nil
	}
	if checkpoint.LatestCompact != nil {
		entry := checkpointIndexEntry(*checkpoint.LatestCompact)
		messages, err := readTranscriptMessagesFromFile(snapshot.file, path, []transcriptIndexEntry{entry})
		if err != nil || len(messages) != 1 || messages[0].ID != entry.ID || messages[0].Kind != llm.MessageKindCompact {
			return MessagePage{}, false, nil
		}
	}
	page, err := reverseTranscriptMessagePageFromFile(snapshot.file, path, checkpoint, beforeID, limit)
	if err != nil {
		return MessagePage{}, false, nil
	}
	if err := snapshot.verify(); err != nil {
		return MessagePage{}, false, nil
	}
	if !transcriptCheckpointMatchesSnapshot(checkpoint, snapshot) {
		return MessagePage{}, false, nil
	}
	return page, true, nil
}

func reverseTranscriptMessagePageFromFile(
	file *os.File,
	path string,
	checkpoint *transcriptCheckpoint,
	beforeID string,
	limit int,
) (MessagePage, error) {
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
	var newerSequence uint64

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
		message, header, err := decodeReverseTranscriptMessage(path, line)
		if err != nil {
			return MessagePage{}, err
		}
		if newerSequence != 0 && header.Sequence+1 != newerSequence {
			return MessagePage{}, fmt.Errorf(
				"session: parse %s from tail: journal sequence %d does not precede %d",
				path, header.Sequence, newerSequence,
			)
		}
		newerSequence = header.Sequence
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

func decodeReverseTranscriptMessage(path string, line []byte) (llm.Message, journalRecordHeader, error) {
	message, header, err := decodeTranscriptJournalLine(line, journalRecordExpectation{
		kind:      journalKindConversation,
		sessionID: journalSessionID(path),
	})
	if err != nil {
		return llm.Message{}, journalRecordHeader{}, fmt.Errorf("session: parse %s from tail: %w", path, err)
	}
	message, err = normalizeLoadedMessage(path, 1, message)
	return message, header, err
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
