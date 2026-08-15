package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

func TestLoadInfoPageKeepsSummaryAndPageOnSameTranscriptRevision(t *testing.T) {
	for _, test := range []struct {
		name   string
		append func(*testing.T, *Session, string, llm.Message)
	}{
		{
			name: "new checkpoint",
			append: func(t *testing.T, s *Session, _ string, message llm.Message) {
				t.Helper()
				if err := s.Append(message); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "checkpoint fallback",
			append: func(t *testing.T, _ *Session, path string, message llm.Message) {
				t.Helper()
				line := mustTranscriptLine(t, journalSessionID(path), 2, message)
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write(line); err != nil {
					file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.Append(messageWithID(llm.TextMessage(llm.RoleAssistant, "initial"), "m1")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Dir, conversationFile)
			newMessage := messageWithID(llm.TextMessage(llm.RoleUser, "new user"), "m2")
			appended := false
			info, page, err := loadInfoPageWithSummaryLoader(s.Dir, "", 10, func(dir string) (Info, transcriptIndex, error) {
				info, idx, err := loadInfoSummary(dir)
				if err == nil && !appended {
					appended = true
					test.append(t, s, path, newMessage)
				}
				return info, idx, err
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(messageIDsForTest(page.Messages), ","); got != "m1,m2" {
				t.Fatalf("page ids = %s, want m1,m2", got)
			}
			if info.Turns != 1 || info.Preview != "new user" {
				t.Fatalf("summary = turns %d preview %q, want 1/new user", info.Turns, info.Preview)
			}
		})
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

func TestCheckpointRecentPageDoesNotScanCompleteSuffix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, conversationFile)
	sessionID := filepath.Base(dir)
	compactLine := mustTranscriptLine(t, sessionID, 1, messageWithID(compactTestMessage("summary"), "m1"))
	middleLine := mustTranscriptLine(t, sessionID, 2, messageWithID(llm.TextMessage(llm.RoleAssistant, "middle"), "m2"))
	latestLine := mustTranscriptLine(t, sessionID, 3, messageWithID(llm.TextMessage(llm.RoleUser, "latest"), "m3"))
	data := append([]byte{}, compactLine...)
	data = append(data, []byte("not-json\n")...)
	data = append(data, middleLine...)
	data = append(data, latestLine...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := fingerprintFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &transcriptCheckpoint{
		Version:       transcriptCheckpointVersion,
		Fingerprint:   fingerprint,
		RepairSafe:    true,
		LatestCompact: &transcriptCheckpointEntry{ID: "m1", Offset: 0, Length: len(compactLine), Sequence: 1},
	}
	digest := sha256.Sum256(data)
	checkpoint.ContentSHA256 = hex.EncodeToString(digest[:])
	checkpoint.ChecksumSHA256 = transcriptCheckpointChecksum(checkpoint)

	page, checkpointed, err := transcriptMessagePageFromCheckpoint(path, checkpoint, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !checkpointed {
		t.Fatal("recent page scanned the malformed interior suffix instead of using the bounded tail path")
	}
	if got := strings.Join(messageIDsForTest(page.Messages), ","); got != "m3" {
		t.Fatalf("recent page ids = %s, want m3", got)
	}
	if !page.HasMoreBefore {
		t.Fatal("recent page has_more_before = false, want true")
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
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unsafeMessage := toolUseMessage("m1", "call-hidden", "read")
	unsafeLine := mustTranscriptLine(t, s.ID, 1, unsafeMessage)
	baseSafeMessage := messageWithID(llm.TextMessage(llm.RoleAssistant, "x"), "m1")
	baseSafeLine := mustTranscriptLine(t, s.ID, 1, baseSafeMessage)
	padding := len(unsafeLine) - len(baseSafeLine)
	if padding < 0 {
		t.Fatalf("unsafe line is %d bytes, shorter than safe line %d", len(unsafeLine), len(baseSafeLine))
	}
	safeMessage := messageWithID(llm.TextMessage(llm.RoleAssistant, "x"+strings.Repeat("x", padding)), "m1")
	safeLine := mustTranscriptLine(t, s.ID, 1, safeMessage)
	if len(safeLine) != len(unsafeLine) {
		t.Fatalf("safe line is %d bytes, want %d", len(safeLine), len(unsafeLine))
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

	loaded, err := LoadWithOptions(dir, Options{
		RepairTranscript: true,
		EventCatalog:     sessionTestEventCatalog{},
	})
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

func TestCheckpointContentDigestRejectsFingerprintCollision(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleAssistant, "first"), "m1"),
		messageWithID(compactTestMessage("summary"), "m2"),
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
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := bytes.Replace(data, []byte("first"), []byte("other"), 1)
	if bytes.Equal(rewritten, data) || len(rewritten) != len(data) {
		t.Fatal("test did not produce an equal-size content rewrite")
	}
	if err := os.WriteFile(path, rewritten, 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := openTranscriptSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()

	// Simulate a Windows metadata collision by making the stale checkpoint's
	// fingerprint agree with the rewritten file while retaining its old digest.
	meta.Transcript.Fingerprint = snapshot.fingerprint
	meta.Transcript.ChecksumSHA256 = transcriptCheckpointChecksum(meta.Transcript)
	if !transcriptCheckpointValid(meta.Transcript, snapshot.fingerprint) {
		t.Fatal("simulated colliding checkpoint is structurally invalid")
	}
	if transcriptCheckpointMatchesSnapshotWithContentCheck(meta.Transcript, snapshot, true) {
		t.Fatal("content digest accepted a stale checkpoint after an equal-size rewrite")
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

func TestTranscriptSnapshotIdentityRejectsWeakMetadataReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows prevents replacing a path while the original handle is open")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "conversation.jsonl")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	initialInfo, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}

	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, initialInfo.ModTime(), initialInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	canonicalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	openInfo, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	initialWeak := fingerprintFromFileInfo(initialInfo)
	canonicalWeak := fingerprintFromFileInfo(canonicalInfo)
	if initialWeak != canonicalWeak {
		t.Fatalf("weak fingerprints differ: initial=%+v canonical=%+v", initialWeak, canonicalWeak)
	}
	if sameTranscriptFile(initialInfo, openInfo, canonicalInfo) {
		t.Fatal("replacement accepted solely because weak size/mtime metadata matched")
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

func TestTranscriptWithoutCheckpointGainsOneOnNextAppend(t *testing.T) {
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

func TestTailStartCompactedTranscriptWithRepairLoadsOnlyActiveWindow(t *testing.T) {
	root := t.TempDir()
	compact := messageWithID(compactTestMessage("summary"), "m3")
	compact.Compaction = &llm.CompactionMetadata{TailStartMessageID: "m2"}
	dir := makeSession(t, root, "20260812T120000-legacy02", []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleUser, "old"), "m1"),
		messageWithID(llm.TextMessage(llm.RoleAssistant, "retained"), "m2"),
		compact,
		messageWithID(llm.TextMessage(llm.RoleUser, "latest"), "m4"),
	}, time.Now())

	s, err := LoadWithOptions(dir, Options{
		RepairTranscript: true,
		EventCatalog:     sessionTestEventCatalog{},
	})
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

	repaired, err := LoadWithOptions(dir, Options{
		RepairTranscript: true,
		EventCatalog:     sessionTestEventCatalog{},
	})
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

func TestCheckpointRepairFlagsMustMatchCanonicalCompactMarker(t *testing.T) {
	s, err := New(t.TempDir())
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
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transcript == nil || meta.Transcript.RepairPrefixSafe || meta.Transcript.RepairSafe {
		t.Fatalf("checkpoint repair state = %+v, want unsafe prefix", meta.Transcript)
	}
	meta.Transcript.RepairPrefixSafe = true
	meta.Transcript.RepairSafe = true
	if err := saveMetadata(dir, meta); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := fingerprintFromPath(filepath.Join(dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if transcriptCheckpointValid(meta.Transcript, fingerprint) {
		t.Fatal("checkpoint accepted repair flags changed without a matching checksum")
	}

	loaded, err := LoadWithOptions(dir, Options{
		RepairTranscript: true,
		EventCatalog:     sessionTestEventCatalog{},
	})
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

func TestCheckpointTailStartMustRetainEveryCanonicalRow(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	compact := messageWithID(compactTestMessage("summary"), "m4")
	compact.Compaction = &llm.CompactionMetadata{TailStartMessageID: "m2"}
	for _, message := range []llm.Message{
		messageWithID(llm.TextMessage(llm.RoleUser, "old"), "m1"),
		messageWithID(llm.TextMessage(llm.RoleAssistant, "tail start"), "m2"),
		messageWithID(llm.TextMessage(llm.RoleUser, "tail middle"), "m3"),
		compact,
		messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m5"),
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
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Transcript.Retained) != 2 {
		t.Fatalf("checkpoint retained = %+v, want m2 and m3", meta.Transcript.Retained)
	}
	meta.Transcript.Retained = meta.Transcript.Retained[:1]
	meta.Transcript.ChecksumSHA256 = transcriptCheckpointChecksum(meta.Transcript)
	if err := saveMetadata(dir, meta); err != nil {
		t.Fatal(err)
	}

	idx, checkpointed, err := loadActiveTranscriptIndex(path, meta.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	if checkpointed {
		t.Fatal("active transcript accepted a retained tail with a canonical hole")
	}
	if got := strings.Join(transcriptEntryIDs(idx.entries), ","); got != "m2,m3,m4,m5" {
		t.Fatalf("active index ids = %s, want m2,m3,m4,m5", got)
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
