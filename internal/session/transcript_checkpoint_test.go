package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

func TestLoadUsesCheckpointForCompactedActiveHistory(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleUser, "old user"), "m1"),
		messageWithID(llm.TextMessage(llm.RoleAssistant, "old assistant"), "m2"),
		messageWithID(llm.TextMessage(llm.RoleUser, "retained user"), "m3"),
	} {
		if err := s.Append(msg); err != nil {
			t.Fatal(err)
		}
	}
	compact := messageWithID(compactTestMessage("summary"), "m4")
	compact.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{"m3"}}
	if err := s.Append(compact); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleAssistant, "recent"), "m5")); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transcript == nil || meta.Transcript.LatestCompact == nil {
		t.Fatalf("transcript checkpoint = %+v, want latest compact", meta.Transcript)
	}
	idx, checkpointed, err := loadActiveTranscriptIndex(filepath.Join(dir, conversationFile), meta.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	if !checkpointed {
		t.Fatal("active transcript used full scan, want checkpoint")
	}
	if got := strings.Join(transcriptEntryIDs(idx.entries), ","); got != "m3,m4,m5" {
		t.Fatalf("active index ids = %s, want m3,m4,m5", got)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if got := strings.Join(messageIDsForTest(loaded.History), ","); got != "m3,m4,m5" {
		t.Fatalf("active history ids = %s, want m3,m4,m5", got)
	}
}

func TestLoadInfoPageUsesCheckpointAndKeepsToolPair(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleUser, "old"), "m1"),
		messageWithID(compactTestMessage("summary"), "m2"),
		toolUseMessage("m3", "call-1", "read"),
		toolResultMessage("m4", "call-1", "done"),
		messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m5"),
	} {
		if err := s.Append(msg); err != nil {
			t.Fatal(err)
		}
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, checkpointed, err := transcriptMessagePageFromCheckpoint(
		filepath.Join(dir, conversationFile), meta.Transcript, "", 2,
	); err != nil || !checkpointed {
		t.Fatalf("checkpoint page = used %v error %v, want fast checkpoint path", checkpointed, err)
	}

	info, page, err := LoadInfoPage(dir, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(messageIDsForTest(page.Messages), ","); got != "m3,m4,m5" {
		t.Fatalf("page ids = %s, want m3,m4,m5", got)
	}
	if !page.HasMoreBefore || page.OldestMessageID != "m3" {
		t.Fatalf("page = %+v, want more before m3", page)
	}
	if info.Turns != 2 || info.Preview != "old" {
		t.Fatalf("summary = turns %d preview %q, want 2/old", info.Turns, info.Preview)
	}
	_, older, err := LoadInfoPage(dir, "m3", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(messageIDsForTest(older.Messages), ","); got != "m1,m2" {
		t.Fatalf("older page ids = %s, want m1,m2", got)
	}
	if older.HasMoreBefore || older.OldestMessageID != "m1" {
		t.Fatalf("older page = %+v, want complete prefix from m1", older)
	}
}

func TestCheckpointPageKeepsFastPathForOversizedTranscriptRow(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(messageWithID(compactTestMessage("summary"), "m1")); err != nil {
		t.Fatal(err)
	}
	oversized := llm.TextMessage(llm.RoleAssistant, strings.Repeat("x", maxEventLineBytes+1))
	if err := s.Append(messageWithID(oversized, "m2")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleUser, "latest"), "m3")); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}

	page, checkpointed, err := transcriptMessagePageFromCheckpoint(
		filepath.Join(dir, conversationFile), meta.Transcript, "", 2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !checkpointed {
		t.Fatal("oversized transcript row forced checkpoint page fallback")
	}
	if got := strings.Join(messageIDsForTest(page.Messages), ","); got != "m2,m3" {
		t.Fatalf("page ids = %s, want m2,m3", got)
	}
}

func TestStaleTranscriptCheckpointFallsBackToStrictScan(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleUser, "valid"), "m1")); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, conversationFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not-json\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, _, err := LoadInfoPage(dir, "", 1); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("LoadInfoPage error = %v, want strict parse failure", err)
	}
}

func TestSameSizeTimestampPreservingRewriteInvalidatesCheckpoint(t *testing.T) {
	unsafeMessage := toolUseMessage("m1", "call-hidden", "read")
	unsafeLine, err := marshalJSONLine(unsafeMessage)
	if err != nil {
		t.Fatal(err)
	}
	baseSafeMessage := messageWithID(llm.TextMessage(llm.RoleAssistant, "x"), "m1")
	baseSafeLine, err := marshalJSONLine(baseSafeMessage)
	if err != nil {
		t.Fatal(err)
	}
	padding := len(unsafeLine) - len(baseSafeLine)
	if padding < 0 {
		t.Fatalf("unsafe line is %d bytes, shorter than safe line %d", len(unsafeLine), len(baseSafeLine))
	}
	safeMessage := messageWithID(llm.TextMessage(llm.RoleAssistant, "x"+strings.Repeat("x", padding)), "m1")
	safeLine, err := marshalJSONLine(safeMessage)
	if err != nil {
		t.Fatal(err)
	}
	if len(safeLine) != len(unsafeLine) {
		t.Fatalf("safe line is %d bytes, want %d", len(safeLine), len(unsafeLine))
	}

	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []llm.Message{
		safeMessage,
		messageWithID(compactTestMessage("summary"), "m2"),
		messageWithID(llm.TextMessage(llm.RoleUser, "latest"), "m3"),
	} {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, conversationFile)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < len(safeLine) || string(data[:len(safeLine)]) != string(safeLine) {
		t.Fatal("canonical transcript does not begin with the expected safe row")
	}
	copy(data[:len(unsafeLine)], unsafeLine)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || after.ModTime().UnixNano() != before.ModTime().UnixNano() {
		t.Fatalf("rewrite changed size or mtime: before=%d/%d after=%d/%d",
			before.Size(), before.ModTime().UnixNano(), after.Size(), after.ModTime().UnixNano())
	}
	afterFingerprint, err := fingerprintFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transcript == nil || meta.Transcript.Fingerprint.ChangeID == afterFingerprint.ChangeID {
		t.Fatalf("change identity did not invalidate checkpoint: before=%+v after=%+v",
			meta.Transcript, afterFingerprint)
	}

	loaded, err := LoadWithOptions(dir, Options{RepairTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Close(); err != nil {
		t.Fatal(err)
	}
	_, full, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 4 || full[0].ID != "m1" || full[1].Kind != llm.MessageKindToolResult ||
		full[1].Blocks[0].ToolUseID != "call-hidden" || full[2].ID != "m2" {
		t.Fatalf("repaired transcript = %+v, want hidden tool result before compact marker", full)
	}
}

func TestTranscriptSnapshotDetectsSameMetadataReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows prevents replacing a path while the snapshot handle is open")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "conversation.jsonl")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := openTranscriptSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()

	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || after.ModTime().UnixNano() != before.ModTime().UnixNano() {
		t.Fatalf("replacement changed size or mtime: before=%d/%d after=%d/%d",
			before.Size(), before.ModTime().UnixNano(), after.Size(), after.ModTime().UnixNano())
	}
	if err := snapshot.verify(); !errors.Is(err, ErrTranscriptChanged) {
		t.Fatalf("snapshot verify error = %v, want ErrTranscriptChanged", err)
	}
}

func TestWeakFingerprintCannotBuildOrValidateCheckpoint(t *testing.T) {
	fingerprint := transcriptFingerprint{Size: 1, MtimeNS: 2}
	idx := transcriptIndex{repairSafe: true, repairPrefixSafe: true, complete: true}
	if checkpoint := buildTranscriptCheckpoint(idx, fingerprint); checkpoint != nil {
		t.Fatalf("checkpoint = %+v, want nil for weak fingerprint", checkpoint)
	}
	checkpoint := &transcriptCheckpoint{
		Version:     transcriptCheckpointVersion,
		Fingerprint: fingerprint,
	}
	if transcriptCheckpointValid(checkpoint, fingerprint) {
		t.Fatal("weak fingerprint validated a checkpoint")
	}
}

func TestLegacyTranscriptGainsCheckpointOnNextAppend(t *testing.T) {
	root := t.TempDir()
	id := "20260812T120000-legacy01"
	dir := makeSession(t, root, id, []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleUser, "legacy"), "m1"),
	}, time.Now())
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transcript != nil {
		t.Fatalf("legacy checkpoint = %+v, want nil", meta.Transcript)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleAssistant, "adopted"), "m2")); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	meta, err = loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transcript == nil || meta.Transcript.Fingerprint.Size <= 0 {
		t.Fatalf("adopted checkpoint = %+v, want current fingerprint", meta.Transcript)
	}
}

func TestLegacyCompactedTranscriptWithRepairLoadsOnlyActiveWindow(t *testing.T) {
	root := t.TempDir()
	compact := messageWithID(compactTestMessage("summary"), "m3")
	compact.Compaction = &llm.CompactionMetadata{TailStartMessageID: "m2"}
	dir := makeSession(t, root, "20260812T120000-legacy02", []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleUser, "old"), "m1"),
		messageWithID(llm.TextMessage(llm.RoleAssistant, "retained"), "m2"),
		compact,
		messageWithID(llm.TextMessage(llm.RoleUser, "latest"), "m4"),
	}, time.Now())

	s, err := LoadWithOptions(dir, Options{RepairTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := strings.Join(messageIDsForTest(s.History), ","); got != "m2,m3,m4" {
		t.Fatalf("active history ids = %s, want m2,m3,m4", got)
	}
}

func TestCheckpointedCompactedTranscriptRepairsUnretainedToolUse(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []llm.Message{
		toolUseMessage("m1", "call-hidden", "read"),
		messageWithID(compactTestMessage("summary"), "m2"),
		messageWithID(llm.TextMessage(llm.RoleUser, "latest"), "m3"),
	} {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	repaired, err := LoadWithOptions(dir, Options{RepairTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	defer repaired.Close()
	_, full, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 4 || full[0].ID != "m1" || full[1].Kind != llm.MessageKindToolResult ||
		full[1].Blocks[0].ToolUseID != "call-hidden" || full[2].ID != "m2" {
		t.Fatalf("repaired transcript = %+v, want hidden tool result before compact marker", full)
	}
}

func TestCorruptCheckpointEntryFallsBackToCanonicalTranscript(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleUser, "old"), "m1"),
		messageWithID(llm.TextMessage(llm.RoleAssistant, "retained"), "m2"),
	} {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	compact := messageWithID(compactTestMessage("summary"), "m3")
	compact.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{"m2"}}
	if err := s.Append(compact); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleUser, "latest"), "m4")); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.Transcript.Retained[0].ID = "wrong-id"
	if err := saveMetadata(dir, meta); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if got := strings.Join(messageIDsForTest(loaded.History), ","); got != "m2,m3,m4" {
		t.Fatalf("active history ids = %s, want m2,m3,m4", got)
	}
}

func TestCheckpointRetainedEntriesMustMatchCompactMarker(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleUser, "old"), "m1"),
		messageWithID(llm.TextMessage(llm.RoleAssistant, "retained"), "m2"),
	} {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	compact := messageWithID(compactTestMessage("summary"), "m3")
	compact.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{"m2"}}
	if err := s.Append(compact); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleUser, "latest"), "m4")); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, conversationFile)
	canonical, err := scanTranscriptIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.Transcript.Retained[0] = checkpointEntry(canonical.entries[0])
	if err := saveMetadata(dir, meta); err != nil {
		t.Fatal(err)
	}

	idx, checkpointed, err := loadActiveTranscriptIndex(path, meta.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	if checkpointed {
		t.Fatal("active transcript accepted retained entries that disagree with compact marker")
	}
	if got := strings.Join(transcriptEntryIDs(idx.entries), ","); got != "m2,m3,m4" {
		t.Fatalf("active index ids = %s, want m2,m3,m4", got)
	}
}

func TestCheckpointLatestCompactMustMatchCanonicalLatestMarker(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstCompact := messageWithID(compactTestMessage("first summary"), "m3")
	firstCompact.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{"m2"}}
	latestCompact := messageWithID(compactTestMessage("latest summary"), "m5")
	latestCompact.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{"m4"}}
	for _, message := range []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleUser, "old"), "m1"),
		messageWithID(llm.TextMessage(llm.RoleAssistant, "first retained"), "m2"),
		firstCompact,
		messageWithID(llm.TextMessage(llm.RoleUser, "latest retained"), "m4"),
		latestCompact,
		messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m6"),
	} {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, conversationFile)
	canonical, err := scanTranscriptIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.Transcript.LatestCompact = pointerToCheckpointEntry(checkpointEntry(canonical.entries[2]))
	meta.Transcript.Retained = []transcriptCheckpointEntry{checkpointEntry(canonical.entries[1])}
	if err := saveMetadata(dir, meta); err != nil {
		t.Fatal(err)
	}

	idx, checkpointed, err := loadActiveTranscriptIndex(path, meta.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	if checkpointed {
		t.Fatal("active transcript accepted a checkpoint that names an older compact marker")
	}
	if got := strings.Join(transcriptEntryIDs(idx.entries), ","); got != "m4,m5,m6" {
		t.Fatalf("active index ids = %s, want m4,m5,m6", got)
	}
	if _, checkpointed, err := transcriptMessagePageFromCheckpoint(path, meta.Transcript, "", 60); err != nil {
		t.Fatal(err)
	} else if checkpointed {
		t.Fatal("transcript page accepted a checkpoint that names an older compact marker")
	}
}

func TestCheckpointRepairSafetyRecoversAfterToolResult(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(toolUseMessage("m1", "call-1", "read")); err != nil {
		t.Fatal(err)
	}
	meta, err := loadMetadata(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transcript.RepairSafe {
		t.Fatal("checkpoint repair_safe = true with an unresolved tool call")
	}
	if err := s.Append(toolResultMessage("m2", "call-1", "done")); err != nil {
		t.Fatal(err)
	}
	compact := messageWithID(compactTestMessage("summary"), "m3")
	if err := s.Append(compact); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	meta, err = loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Transcript.RepairSafe || !meta.Transcript.RepairPrefixSafe {
		t.Fatalf("checkpoint repair state = %+v, want safe transcript and prefix", meta.Transcript)
	}
	if _, checkpointed, err := loadActiveTranscriptIndex(filepath.Join(dir, conversationFile), meta.Transcript); err != nil || !checkpointed {
		t.Fatalf("checkpoint load = used %v error %v, want fast path", checkpointed, err)
	}
}

func TestLiveTranscriptIndexIsBoundedAfterCompaction(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, message := range []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleUser, "old"), "m1"),
		messageWithID(llm.TextMessage(llm.RoleAssistant, "retained"), "m2"),
	} {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	compact := messageWithID(compactTestMessage("summary"), "m3")
	compact.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{"m2"}}
	if err := s.Append(compact); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleUser, "latest"), "m4")); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(messageIDsForTest(s.History), ","); got != "m1,m2,m3,m4" {
		t.Fatalf("live history ids = %s, want m1,m2,m3,m4", got)
	}
	if got := strings.Join(transcriptEntryIDs(s.transcript.entries), ","); got != "m2,m3,m4" {
		t.Fatalf("active index ids = %s, want m2,m3,m4", got)
	}
	if s.transcript.complete {
		t.Fatal("compacted live transcript index is still marked complete")
	}
}

func transcriptEntryIDs(entries []transcriptIndexEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}
