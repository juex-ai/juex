package session

import "github.com/juex-ai/juex/internal/llm"

const transcriptCheckpointVersion = 2

// transcriptCheckpoint is a bounded, derived index over conversation.jsonl.
// The transcript fingerprint makes the JSONL file authoritative whenever the
// two disagree.
type transcriptCheckpoint struct {
	Version          int                         `json:"version"`
	Fingerprint      transcriptFingerprint       `json:"fingerprint"`
	Turns            int                         `json:"turns"`
	Preview          string                      `json:"preview,omitempty"`
	RepairSafe       bool                        `json:"repair_safe"`
	RepairPrefixSafe bool                        `json:"repair_prefix_safe"`
	LatestCompact    *transcriptCheckpointEntry  `json:"latest_compact,omitempty"`
	Retained         []transcriptCheckpointEntry `json:"retained,omitempty"`
}

type transcriptCheckpointEntry struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
	Length int    `json:"length"`
}

func checkpointEntry(entry transcriptIndexEntry) transcriptCheckpointEntry {
	return transcriptCheckpointEntry{ID: entry.ID, Offset: entry.Offset, Length: entry.Length}
}

func checkpointIndexEntry(entry transcriptCheckpointEntry) transcriptIndexEntry {
	return transcriptIndexEntry{ID: entry.ID, Offset: entry.Offset, Length: entry.Length}
}

func transcriptCheckpointValid(checkpoint *transcriptCheckpoint, fingerprint transcriptFingerprint) bool {
	if checkpoint == nil || checkpoint.Version != transcriptCheckpointVersion || !fingerprint.strong() ||
		checkpoint.Fingerprint != fingerprint ||
		checkpoint.Turns < 0 || checkpoint.Fingerprint.Size < 0 {
		return false
	}
	if checkpoint.LatestCompact == nil {
		return len(checkpoint.Retained) == 0
	}
	compact := *checkpoint.LatestCompact
	if !validCheckpointEntry(compact, checkpoint.Fingerprint.Size) {
		return false
	}
	seen := make(map[string]struct{}, len(checkpoint.Retained))
	lastOffset := int64(-1)
	for _, entry := range checkpoint.Retained {
		if !validCheckpointEntry(entry, compact.Offset) || entry.Offset <= lastOffset {
			return false
		}
		if _, ok := seen[entry.ID]; ok {
			return false
		}
		seen[entry.ID] = struct{}{}
		lastOffset = entry.Offset
	}
	return true
}

func validCheckpointEntry(entry transcriptCheckpointEntry, ceiling int64) bool {
	return entry.ID != "" && entry.Offset >= 0 && entry.Length > 0 &&
		entry.Offset+int64(entry.Length) <= ceiling
}

func buildTranscriptCheckpoint(idx transcriptIndex, fingerprint transcriptFingerprint) *transcriptCheckpoint {
	if !fingerprint.strong() {
		return nil
	}
	checkpoint := &transcriptCheckpoint{
		Version:          transcriptCheckpointVersion,
		Fingerprint:      fingerprint,
		Turns:            idx.turns,
		Preview:          idx.preview,
		RepairSafe:       idx.repairSafe,
		RepairPrefixSafe: idx.repairPrefixSafe,
	}
	compactIndex := idx.latestCompact()
	if compactIndex < 0 {
		return checkpoint
	}
	compact := idx.entries[compactIndex]
	checkpoint.LatestCompact = pointerToCheckpointEntry(checkpointEntry(compact))

	retained, ok := retainedTranscriptEntries(idx.entries[:compactIndex], compact)
	if !ok {
		return nil
	}
	checkpoint.Retained = make([]transcriptCheckpointEntry, 0, len(retained))
	for _, entry := range retained {
		checkpoint.Retained = append(checkpoint.Retained, checkpointEntry(entry))
	}
	if !transcriptCheckpointValid(checkpoint, fingerprint) {
		return nil
	}
	return checkpoint
}

func pointerToCheckpointEntry(entry transcriptCheckpointEntry) *transcriptCheckpointEntry {
	return &entry
}

func retainedTranscriptEntries(entries []transcriptIndexEntry, compact transcriptIndexEntry) ([]transcriptIndexEntry, bool) {
	if len(compact.RetainedMessageIDs) > 0 {
		wanted := make(map[string]struct{}, len(compact.RetainedMessageIDs))
		for _, id := range compact.RetainedMessageIDs {
			if id == "" {
				return nil, false
			}
			wanted[id] = struct{}{}
		}
		retained := make([]transcriptIndexEntry, 0, len(wanted))
		for _, entry := range entries {
			if _, ok := wanted[entry.ID]; ok {
				retained = append(retained, entry)
				delete(wanted, entry.ID)
			}
		}
		return retained, len(wanted) == 0
	}
	if compact.TailStartMessageID == "" {
		return nil, true
	}
	for i, entry := range entries {
		if entry.ID == compact.TailStartMessageID {
			return append([]transcriptIndexEntry(nil), entries[i:]...), true
		}
	}
	return nil, false
}

// loadActiveTranscriptIndex restores only the provider-relevant compacted
// window when a current checkpoint is available. The bool reports whether the
// full transcript scan was avoided.
func loadActiveTranscriptIndex(path string, checkpoint *transcriptCheckpoint) (transcriptIndex, bool, error) {
	snapshot, err := openTranscriptSnapshot(path)
	if err != nil {
		return transcriptIndex{}, false, err
	}
	defer snapshot.close()
	fingerprint := snapshot.fingerprint
	if !transcriptCheckpointValid(checkpoint, fingerprint) || checkpoint.LatestCompact == nil {
		idx, err := scanTranscriptIndex(path)
		if err != nil {
			return transcriptIndex{}, false, err
		}
		return activeTranscriptIndex(idx), false, nil
	}

	var idx transcriptIndex
	for _, retained := range checkpoint.Retained {
		entry := checkpointIndexEntry(retained)
		messages, err := readTranscriptMessagesFromFile(snapshot.file, path, []transcriptIndexEntry{entry})
		if err != nil {
			return scanActiveTranscriptIndex(path)
		}
		if len(messages) != 1 || messages[0].ID != retained.ID {
			return scanActiveTranscriptIndex(path)
		}
		idx.add(messages[0], 0, retained.Offset, retained.Length)
	}
	suffix, err := scanTranscriptIndexFromFile(snapshot.file, path, checkpoint.LatestCompact.Offset)
	if err != nil {
		return scanActiveTranscriptIndex(path)
	}
	if len(suffix.entries) == 0 || suffix.entries[0].ID != checkpoint.LatestCompact.ID ||
		suffix.entries[0].Kind != llm.MessageKindCompact {
		return scanActiveTranscriptIndex(path)
	}
	if !retainedEntriesMatchCompact(idx.entries, suffix.entries[0]) {
		return scanActiveTranscriptIndex(path)
	}
	repairBroken := suffix.repairBroken || !checkpoint.RepairPrefixSafe
	repairPending := append([]pendingTranscriptToolUse(nil), suffix.repairPending...)
	repairSafe := !repairBroken && len(repairPending) == 0
	if repairSafe != checkpoint.RepairSafe {
		return scanActiveTranscriptIndex(path)
	}
	latestCompactAt := len(idx.entries)
	idx.entries = append(idx.entries, suffix.entries...)
	idx.turns = checkpoint.Turns
	idx.preview = checkpoint.Preview
	idx.fingerprint = fingerprint
	idx.repairSafe = repairSafe
	idx.repairPrefixSafe = checkpoint.RepairPrefixSafe
	idx.repairBroken = repairBroken
	idx.repairPending = repairPending
	idx.complete = false
	idx.latestCompactAt = latestCompactAt
	idx.hasLatestCompact = true
	if err := snapshot.verify(); err != nil {
		return scanActiveTranscriptIndex(path)
	}
	return idx, true, nil
}

func retainedEntriesMatchCompact(entries []transcriptIndexEntry, compact transcriptIndexEntry) bool {
	expected, ok := retainedTranscriptEntries(entries, compact)
	if !ok || len(expected) != len(entries) {
		return false
	}
	for i := range entries {
		if entries[i].ID != expected[i].ID {
			return false
		}
	}
	return true
}

func scanActiveTranscriptIndex(path string) (transcriptIndex, bool, error) {
	idx, err := scanTranscriptIndex(path)
	if err != nil {
		return transcriptIndex{}, false, err
	}
	return activeTranscriptIndex(idx), false, nil
}

func activeTranscriptIndex(idx transcriptIndex) transcriptIndex {
	compactIndex := idx.latestCompact()
	if compactIndex < 0 {
		return idx
	}
	compact := idx.entries[compactIndex]
	originalLength := len(idx.entries)
	retained, ok := retainedTranscriptEntries(idx.entries[:compactIndex], compact)
	if !ok {
		return idx
	}
	entries := make([]transcriptIndexEntry, 0, len(retained)+len(idx.entries)-compactIndex)
	entries = append(entries, retained...)
	entries = append(entries, idx.entries[compactIndex:]...)
	idx.entries = entries
	idx.complete = idx.complete && len(entries) == originalLength
	idx.latestCompactAt = len(retained)
	idx.hasLatestCompact = true
	return idx
}
