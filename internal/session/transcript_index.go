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
	entries          []transcriptIndexEntry
	turns            int
	preview          string
	fingerprint      transcriptFingerprint
	repairSafe       bool
	repairPrefixSafe bool
	repairBroken     bool
	repairPending    []pendingTranscriptToolUse
	complete         bool
	latestCompactAt  int
	hasLatestCompact bool
}

type transcriptIndexEntry struct {
	LineIndex          int
	Offset             int64
	Length             int
	ID                 string
	Role               llm.Role
	Kind               string
	TailStartMessageID string
	RetainedMessageIDs []string
	ToolUseIDs         []string
	ToolResultIDs      []string
}

func scanTranscriptIndex(path string) (transcriptIndex, error) {
	return scanTranscriptIndexFrom(path, 0)
}

func scanTranscriptIndexFrom(path string, start int64) (transcriptIndex, error) {
	snapshot, err := openTranscriptSnapshot(path)
	if err != nil {
		return transcriptIndex{}, err
	}
	defer snapshot.close()
	idx, err := scanTranscriptIndexFromFile(snapshot.file, path, start)
	if err != nil {
		return transcriptIndex{}, err
	}
	if err := snapshot.verify(); err != nil {
		return transcriptIndex{}, err
	}
	idx.fingerprint = snapshot.fingerprint
	return idx, nil
}

func scanTranscriptIndexFromFile(f *os.File, path string, start int64) (transcriptIndex, error) {
	if start < 0 {
		return transcriptIndex{}, fmt.Errorf("session: invalid transcript offset %d", start)
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return transcriptIndex{}, err
	}

	idx := transcriptIndex{repairSafe: true, repairPrefixSafe: true, complete: true}
	reader := bufio.NewReader(f)
	offset := start
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
				normalized, err := normalizeLoadedMessage(path, lineIndex+1, msg)
				if err != nil {
					return transcriptIndex{}, err
				}
				idx.add(normalized, lineIndex, entryOffset, len(line))
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
	var retainedMessageIDs []string
	if msg.Compaction != nil {
		tailStartID = msg.Compaction.TailStartMessageID
		retainedMessageIDs = append([]string(nil), msg.Compaction.RetainedMessageIDs...)
	}
	toolUseIDs, toolResultIDs := transcriptToolIDs(msg)
	idx.entries = append(idx.entries, transcriptIndexEntry{
		LineIndex:          lineIndex,
		Offset:             offset,
		Length:             length,
		ID:                 msg.ID,
		Role:               msg.Role,
		Kind:               msg.Kind,
		TailStartMessageID: tailStartID,
		RetainedMessageIDs: retainedMessageIDs,
		ToolUseIDs:         toolUseIDs,
		ToolResultIDs:      toolResultIDs,
	})
	idx.addSummary(msg)
	idx.addRepairState(msg)
	if msg.Kind == llm.MessageKindCompact {
		idx.latestCompactAt = len(idx.entries) - 1
		idx.hasLatestCompact = true
		idx.repairPrefixSafe = idx.repairSafe
	}
}

func (idx *transcriptIndex) addRepairState(msg llm.Message) {
	pending := idx.repairPending
	if len(pending) > 0 {
		remaining, invalid := consumePendingToolResults(pending, msg)
		if invalid {
			idx.repairBroken = true
		} else {
			pending = remaining
			if len(pending) > 0 {
				idx.repairPending = pending
				idx.repairSafe = false
				return
			}
			pending = append(pending, messageToolUses(msg)...)
			idx.repairPending = pending
			idx.repairSafe = !idx.repairBroken && len(pending) == 0
			return
		}
	}
	pending = messageToolUses(msg)
	idx.repairPending = pending
	idx.repairSafe = !idx.repairBroken && len(pending) == 0
}

func transcriptToolIDs(msg llm.Message) (uses, results []string) {
	for _, block := range msg.Blocks {
		switch block.Type {
		case llm.BlockToolUse:
			if block.ToolUseID != "" {
				uses = append(uses, block.ToolUseID)
			}
		case llm.BlockToolResult:
			if block.ToolUseID != "" {
				results = append(results, block.ToolUseID)
			}
		}
	}
	return uses, results
}

func (idx *transcriptIndex) appendMessage(msg llm.Message, offset int64, length int) {
	lineIndex := 0
	if n := len(idx.entries); n > 0 {
		lineIndex = idx.entries[n-1].LineIndex + 1
	}
	idx.add(msg, lineIndex, offset, length)
}

func (idx *transcriptIndex) addSummary(msg llm.Message) {
	if msg.Role != llm.RoleUser || msg.Kind == llm.MessageKindCompact || msg.Kind == llm.MessageKindModelChange {
		return
	}
	idx.turns++
	if idx.preview == "" {
		idx.preview = truncateRunes(strings.TrimSpace(msg.FirstText()), previewMaxRunes)
	}
}

func (idx transcriptIndex) initialPageStart() int {
	if compact := idx.latestCompact(); compact >= 0 {
		return compact
	}
	return 0
}

func (idx transcriptIndex) latestCompact() int {
	if idx.hasLatestCompact && idx.latestCompactAt >= 0 && idx.latestCompactAt < len(idx.entries) &&
		idx.entries[idx.latestCompactAt].Kind == llm.MessageKindCompact {
		return idx.latestCompactAt
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
	found, _ := transcriptContainsMessageID(filepath.Join(s.Dir, conversationFile), id)
	return found
}

func (idx transcriptIndex) coherentPageStart(start, floor, end int) int {
	if start <= floor || start >= len(idx.entries) {
		return start
	}
	required := make(map[string]struct{})
	for i := start; i < end; i++ {
		entry := idx.entries[i]
		if entry.Kind == llm.MessageKindHookEvent {
			continue
		}
		if entry.Kind != llm.MessageKindToolResult {
			break
		}
		for _, id := range entry.ToolResultIDs {
			required[id] = struct{}{}
		}
	}
	if len(required) == 0 {
		return start
	}
	for i := start - 1; i >= floor; i-- {
		entry := idx.entries[i]
		if entry.Kind == llm.MessageKindHookEvent {
			continue
		}
		if entry.Kind == llm.MessageKindToolResult {
			for _, id := range entry.ToolResultIDs {
				required[id] = struct{}{}
			}
			continue
		}
		if entry.Role == llm.RoleAssistant && entry.Kind == "" && len(entry.ToolUseIDs) > 0 {
			matched := false
			for _, id := range entry.ToolUseIDs {
				if _, ok := required[id]; ok {
					matched = true
					delete(required, id)
				}
			}
			if matched {
				return i
			}
			return start
		}
		return start
	}
	return start
}

func readTranscriptMessages(path string, entries []transcriptIndexEntry) ([]llm.Message, error) {
	return readTranscriptMessagesForFingerprint(path, entries, transcriptFingerprint{})
}

func readTranscriptMessagesForFingerprint(
	path string,
	entries []transcriptIndexEntry,
	expected transcriptFingerprint,
) ([]llm.Message, error) {
	if len(entries) == 0 && expected == (transcriptFingerprint{}) {
		return []llm.Message{}, nil
	}
	snapshot, err := openTranscriptSnapshot(path)
	if err != nil {
		return nil, err
	}
	defer snapshot.close()
	if expected != (transcriptFingerprint{}) && snapshot.fingerprint != expected {
		return nil, ErrTranscriptChanged
	}
	out, err := readTranscriptMessagesFromFile(snapshot.file, path, entries)
	if err != nil {
		return nil, err
	}
	if err := snapshot.verify(); err != nil {
		return nil, err
	}
	return out, nil
}

func readTranscriptMessagesFromFile(f *os.File, path string, entries []transcriptIndexEntry) ([]llm.Message, error) {
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
		msg, err = normalizeLoadedMessage(path, entry.LineIndex+1, msg)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

func transcriptMessagePage(path string, idx transcriptIndex, beforeID string, limit int) (MessagePage, error) {
	floor := idx.initialPageStart()
	start := floor
	end := len(idx.entries)
	if beforeID != "" {
		before := idx.indexByID(beforeID)
		if before < 0 {
			return MessagePage{}, fmt.Errorf("%w: %s", ErrBeforeMessageNotFound, beforeID)
		}
		floor = 0
		start = floor
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
	start = idx.coherentPageStart(start, floor, end)
	msgs, err := readTranscriptMessagesForFingerprint(path, idx.entries[start:end], idx.fingerprint)
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
	if s.convFD == nil && len(s.History) == 0 {
		return MessagePage{Messages: []llm.Message{}}, nil
	}
	path := filepath.Join(s.Dir, conversationFile)
	checkpoint := buildTranscriptCheckpoint(s.transcript, s.transcript.fingerprint)
	if page, ok, err := transcriptMessagePageFromCheckpoint(path, checkpoint, beforeID, limit); ok || err != nil {
		return page, err
	}
	idx, err := scanTranscriptIndex(path)
	if err != nil {
		return MessagePage{}, err
	}
	return transcriptMessagePage(path, idx, beforeID, limit)
}

// LoadInfoPage returns the session summary and only the requested transcript
// page. It keeps web session views from loading full long-running transcripts.
func LoadInfoPage(dir string, beforeID string, limit int) (Info, MessagePage, error) {
	info, idx, err := loadInfoSummary(dir)
	if err != nil {
		return Info{}, MessagePage{}, err
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		return Info{}, MessagePage{}, err
	}
	path := filepath.Join(dir, conversationFile)
	if page, ok, err := transcriptMessagePageFromCheckpoint(path, meta.Transcript, beforeID, limit); ok || err != nil {
		return info, page, err
	}
	if len(idx.entries) == 0 {
		idx, err = scanTranscriptIndex(path)
		if err != nil {
			return Info{}, MessagePage{}, err
		}
	}
	page, err := transcriptMessagePage(path, idx, beforeID, limit)
	if err != nil {
		return Info{}, MessagePage{}, err
	}
	return info, page, nil
}

// LoadActiveMessages returns the provider-visible active transcript window for
// an inactive session without materializing the entire transcript in memory.
func LoadActiveMessages(dir string) ([]llm.Message, error) {
	convPath := filepath.Join(dir, conversationFile)
	if _, err := os.Stat(convPath); errors.Is(err, os.ErrNotExist) {
		if info, dirErr := os.Stat(dir); dirErr == nil && info.IsDir() {
			return []llm.Message{}, nil
		}
		return nil, err
	} else if err != nil {
		return nil, err
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		return nil, err
	}
	idx, _, err := loadActiveTranscriptIndex(convPath, meta.Transcript)
	if err != nil {
		return nil, err
	}
	return readTranscriptMessagesForFingerprint(convPath, idx.entries, idx.fingerprint)
}

func transcriptContainsMessageID(path, id string) (bool, error) {
	found, err := reverseTranscriptContainsMessageID(path, id)
	if err == nil {
		return found, nil
	}
	idx, scanErr := scanTranscriptIndex(path)
	if scanErr != nil {
		return false, scanErr
	}
	return idx.indexByID(id) >= 0, nil
}

func reverseTranscriptContainsMessageID(path, id string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	reader, err := newReverseLineReader(file)
	if err != nil {
		return false, err
	}
	for {
		line, err := reader.next()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		message, err := decodeReverseTranscriptMessage(path, line)
		if err != nil {
			return false, err
		}
		if message.ID == id {
			return true, nil
		}
	}
}
