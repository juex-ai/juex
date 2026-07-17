package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
)

// MessagePage is a lazily-read transcript page for UI consumers.
type MessagePage struct {
	Messages        []llm.Message
	HasMoreBefore   bool
	OldestMessageID string
}

var ErrBeforeMessageNotFound = errors.New("before message not found")

type transcriptIndex struct {
	entries []transcriptIndexEntry
	turns   int
	preview string
}

type transcriptIndexEntry struct {
	LineIndex          int
	Offset             int64
	Length             int
	ID                 string
	Kind               string
	TailStartMessageID string
}

func scanTranscriptIndex(path string) (transcriptIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return transcriptIndex{}, err
	}
	defer f.Close()

	var idx transcriptIndex
	reader := bufio.NewReader(f)
	var offset int64
	lineIndex := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			entryOffset := offset
			offset += int64(len(line))
			if len(bytes.TrimSuffix(line, []byte{'\n'})) > 0 {
				var msg llm.Message
				if err := json.Unmarshal(line, &msg); err != nil {
					return transcriptIndex{}, fmt.Errorf("session: parse %s:%d: %w", path, lineIndex+1, err)
				}
				msg = normalizeLoadedMessage(msg, lineIndex)
				idx.add(msg, lineIndex, entryOffset, len(line))
			}
			lineIndex++
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return transcriptIndex{}, readErr
		}
	}
	return idx, nil
}

func (idx *transcriptIndex) add(msg llm.Message, lineIndex int, offset int64, length int) {
	tailStartID := ""
	if msg.Compaction != nil {
		tailStartID = msg.Compaction.TailStartMessageID
	}
	idx.entries = append(idx.entries, transcriptIndexEntry{
		LineIndex:          lineIndex,
		Offset:             offset,
		Length:             length,
		ID:                 msg.ID,
		Kind:               msg.Kind,
		TailStartMessageID: tailStartID,
	})
	idx.addSummary(msg)
}

func (idx *transcriptIndex) appendMessage(msg llm.Message, offset int64, length int) {
	lineIndex := 0
	if n := len(idx.entries); n > 0 {
		lineIndex = idx.entries[n-1].LineIndex + 1
	}
	idx.add(msg, lineIndex, offset, length)
}

func (idx *transcriptIndex) addSummary(msg llm.Message) {
	if msg.Role != llm.RoleUser || msg.Kind == llm.MessageKindCompact || msg.Kind == llm.MessageKindModelFallback {
		return
	}
	idx.turns++
	if idx.preview == "" {
		idx.preview = truncateRunes(strings.TrimSpace(msg.FirstText()), previewMaxRunes)
	}
}

func (idx transcriptIndex) activeStart() int {
	compact := idx.latestCompact()
	if compact < 0 {
		return 0
	}
	start := compact
	if tailStartID := idx.entries[compact].TailStartMessageID; tailStartID != "" {
		if tail := idx.indexByIDBefore(tailStartID, compact); tail >= 0 {
			start = tail
		}
	}
	return start
}

func (idx transcriptIndex) initialPageStart() int {
	if compact := idx.latestCompact(); compact >= 0 {
		return compact
	}
	return 0
}

func (idx transcriptIndex) latestCompact() int {
	for i := len(idx.entries) - 1; i >= 0; i-- {
		if idx.entries[i].Kind == llm.MessageKindCompact {
			return i
		}
	}
	return -1
}

func (idx transcriptIndex) indexByID(id string) int {
	if id == "" {
		return -1
	}
	for i, entry := range idx.entries {
		if entry.ID == id {
			return i
		}
	}
	return -1
}

// HasMessageID reports whether id exists anywhere in the persisted transcript,
// including messages outside the active in-memory window.
func (s *Session) HasMessageID(id string) bool {
	if s == nil || id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transcript.indexByID(id) >= 0 {
		return true
	}
	for _, msg := range s.History {
		if msg.ID == id {
			return true
		}
	}
	return false
}

func (idx transcriptIndex) indexByIDBefore(id string, before int) int {
	if id == "" {
		return -1
	}
	if before > len(idx.entries) {
		before = len(idx.entries)
	}
	for i := 0; i < before; i++ {
		if idx.entries[i].ID == id {
			return i
		}
	}
	return -1
}

func readActiveTranscriptWindow(path string, idx transcriptIndex) ([]llm.Message, error) {
	start := idx.activeStart()
	if start >= len(idx.entries) {
		return []llm.Message{}, nil
	}
	return readTranscriptMessages(path, idx.entries[start:])
}

func readTranscriptMessages(path string, entries []transcriptIndexEntry) ([]llm.Message, error) {
	if len(entries) == 0 {
		return []llm.Message{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make([]llm.Message, 0, len(entries))
	for _, entry := range entries {
		if entry.Length <= 0 {
			continue
		}
		buf := make([]byte, entry.Length)
		n, err := f.ReadAt(buf, entry.Offset)
		if err != nil && (!errors.Is(err, io.EOF) || n != entry.Length) {
			return nil, err
		}
		buf = buf[:n]
		var msg llm.Message
		if err := json.Unmarshal(buf, &msg); err != nil {
			return nil, fmt.Errorf("session: parse %s:%d: %w", path, entry.LineIndex+1, err)
		}
		out = append(out, normalizeLoadedMessage(msg, entry.LineIndex))
	}
	return out, nil
}

func transcriptMessagePage(path string, idx transcriptIndex, beforeID string, limit int) (MessagePage, error) {
	start := idx.initialPageStart()
	end := len(idx.entries)
	if beforeID != "" {
		before := idx.indexByID(beforeID)
		if before < 0 {
			return MessagePage{}, fmt.Errorf("%w: %s", ErrBeforeMessageNotFound, beforeID)
		}
		start = 0
		end = before
	}
	if limit > 0 && end-start > limit {
		start = end - limit
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	msgs, err := readTranscriptMessages(path, idx.entries[start:end])
	if err != nil {
		return MessagePage{}, err
	}
	oldestID := ""
	if len(msgs) > 0 {
		oldestID = msgs[0].ID
	}
	return MessagePage{
		Messages:        msgs,
		HasMoreBefore:   start > 0,
		OldestMessageID: oldestID,
	}, nil
}

// TranscriptMessagePage returns one transcript page for a live session.
func (s *Session) TranscriptMessagePage(beforeID string, limit int) (MessagePage, error) {
	if s == nil {
		return MessagePage{Messages: []llm.Message{}}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return transcriptMessagePage(filepath.Join(s.Dir, conversationFile), s.transcript, beforeID, limit)
}

// LoadInfoPage returns the session summary and only the requested transcript
// page. It keeps web session views from loading full long-running transcripts.
func LoadInfoPage(dir string, beforeID string, limit int) (Info, MessagePage, error) {
	info, idx, err := loadInfoSummary(dir)
	if err != nil {
		return Info{}, MessagePage{}, err
	}
	page, err := transcriptMessagePage(filepath.Join(dir, conversationFile), idx, beforeID, limit)
	if err != nil {
		return Info{}, MessagePage{}, err
	}
	return info, page, nil
}

// LoadActiveMessages returns the provider-visible active transcript window for
// an inactive session without materializing the entire transcript in memory.
func LoadActiveMessages(dir string) ([]llm.Message, error) {
	convPath := filepath.Join(dir, conversationFile)
	idx, err := scanTranscriptIndex(convPath)
	if err != nil {
		return nil, err
	}
	return readActiveTranscriptWindow(convPath, idx)
}
