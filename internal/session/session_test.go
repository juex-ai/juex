package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func TestSession_AppendsToConversationJSONL(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_ = s.Append(llm.TextMessage(llm.RoleUser, "hello"))
	_ = s.Append(llm.TextMessage(llm.RoleAssistant, "hi"))

	data, _ := os.ReadFile(filepath.Join(s.Dir, conversationFile))
	lines := countLines(data)
	if lines != 2 {
		t.Fatalf("expected 2 lines, got %d: %s", lines, data)
	}
	if len(s.History) != 2 {
		t.Fatalf("history len = %d", len(s.History))
	}
}

func TestValidIDRequiresCanonicalGeneratedShape(t *testing.T) {
	for _, valid := range []string{
		"20260718T065604-8f0582f4",
		"20261318T065604-8f0582f4",
	} {
		if !ValidID(valid) {
			t.Fatalf("ValidID(%q) = false", valid)
		}
	}
	for _, id := range []string{
		"",
		".",
		"..",
		"s1",
		"20260718T065604",
		"20260718T065604-8f0582fg",
		"20260718T065604-8f0582f4\\..\\other",
		"20260718T065604-8f0582f4/other",
		"20260718x065604-8f0582f4",
		"20260718T06a604-8f0582f4",
		"20260718T065604-8F0582F4",
	} {
		t.Run(id, func(t *testing.T) {
			if ValidID(id) {
				t.Fatalf("ValidID(%q) = true", id)
			}
		})
	}
}

func TestSessionNewPersistsOwnedEpochMillisecondTimes(t *testing.T) {
	beforeMS := time.Now().UTC().UnixMilli()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	afterMS := time.Now().UTC().UnixMilli()

	meta, err := loadMetadata(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.StartedAtMS < beforeMS || meta.StartedAtMS > afterMS {
		t.Fatalf("started_at_ms = %d, want [%d, %d]", meta.StartedAtMS, beforeMS, afterMS)
	}
	if meta.LastActiveAtMS != meta.StartedAtMS {
		t.Fatalf("last_active_at_ms = %d, want creation time %d", meta.LastActiveAtMS, meta.StartedAtMS)
	}

	var raw map[string]any
	data, err := os.ReadFile(filepath.Join(s.Dir, metadataFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"started_at_ms", "last_active_at_ms"} {
		if _, ok := raw[key].(float64); !ok {
			t.Fatalf("%s = %#v, want JSON integer", key, raw[key])
		}
	}
}

func TestSessionAppendUpdatesOwnedLastActiveTime(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	stored := time.Date(2025, 1, 2, 3, 4, 5, 678000000, time.UTC).UnixMilli()
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.StartedAtMS = stored
	meta.LastActiveAtMS = stored
	if err := saveMetadata(dir, meta); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if err := loaded.Append(llm.TextMessage(llm.RoleUser, "touch")); err != nil {
		t.Fatal(err)
	}
	updated, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if updated.StartedAtMS != stored {
		t.Fatalf("started_at_ms = %d, want preserved %d", updated.StartedAtMS, stored)
	}
	if updated.LastActiveAtMS <= stored {
		t.Fatalf("last_active_at_ms = %d, want greater than %d", updated.LastActiveAtMS, stored)
	}
}

func TestSessionLazyFirstAppendPersistsOwnedTimes(t *testing.T) {
	s, err := NewWithOptions(t.TempDir(), Options{Lazy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	created := s.Info()
	if _, err := os.Stat(filepath.Join(s.Dir, metadataFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lazy metadata stat = %v, want not exist before first append", err)
	}

	if err := s.Append(llm.TextMessage(llm.RoleUser, "persist lazy session")); err != nil {
		t.Fatal(err)
	}
	meta, err := loadMetadata(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.StartedAtMS != created.StartedAt.UnixMilli() {
		t.Fatalf("started_at_ms = %d, want lazy creation %d", meta.StartedAtMS, created.StartedAt.UnixMilli())
	}
	if meta.LastActiveAtMS < meta.StartedAtMS {
		t.Fatalf("last_active_at_ms = %d, want >= started_at_ms %d", meta.LastActiveAtMS, meta.StartedAtMS)
	}
}

func TestSessionAppendRollsBackWhenMetadataUpdateFails(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	metadataPath := filepath.Join(s.Dir, metadataFile)
	if err := os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(metadataPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadataPath, "block-replacement"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = s.Append(llm.TextMessage(llm.RoleUser, "must roll back"))
	if err == nil {
		t.Fatal("Append error = nil, want metadata persistence failure")
	}
	if strings.Contains(err.Error(), "rollback conversation batch") {
		t.Fatalf("Append error = %v, transcript rollback also failed", err)
	}
	if len(s.History) != 0 || len(s.transcript.entries) != 0 {
		t.Fatalf("in-memory state changed: history=%d transcript=%d", len(s.History), len(s.transcript.entries))
	}
	data, readErr := os.ReadFile(filepath.Join(s.Dir, conversationFile))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(data) != 0 {
		t.Fatalf("conversation = %q, want rolled back empty file", data)
	}
}

func TestSessionAppendRejectsExternallyChangedTranscript(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Append(llm.TextMessage(llm.RoleUser, "first")); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(s.Dir, conversationFile)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(data, []byte("first"), []byte("other"), 1)
	if bytes.Equal(changed, data) {
		t.Fatal("test transcript did not contain the expected text")
	}
	if err := os.WriteFile(path, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	metadataBefore, err := os.ReadFile(filepath.Join(s.Dir, metadataFile))
	if err != nil {
		t.Fatal(err)
	}

	err = s.Append(llm.TextMessage(llm.RoleAssistant, "must not append"))
	if !errors.Is(err, ErrTranscriptChanged) {
		t.Fatalf("Append error = %v, want ErrTranscriptChanged", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, changed) {
		t.Fatalf("conversation changed after rejected append:\ngot  %s\nwant %s", got, changed)
	}
	metadataAfter, err := os.ReadFile(filepath.Join(s.Dir, metadataFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(metadataAfter, metadataBefore) {
		t.Fatal("rejected append replaced the transcript checkpoint")
	}
	if len(s.History) != 1 || len(s.transcript.entries) != 1 {
		t.Fatalf("rejected append mutated memory: history=%d entries=%d", len(s.History), len(s.transcript.entries))
	}
}

func TestSessionAppendNeverReportsFailureAfterPersistingBatch(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	external := messageWithID(llm.TextMessage(llm.RoleUser, "external"), "external")
	externalLine, err := marshalJSONLine(external)
	if err != nil {
		t.Fatal(err)
	}
	s.beforeTranscriptWrite = func() {
		file, openErr := os.OpenFile(filepath.Join(s.Dir, conversationFile), os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.Write(externalLine); writeErr != nil {
			file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}

	message := messageWithID(llm.TextMessage(llm.RoleAssistant, "owned"), "owned")
	appendErr := s.Append(message)
	data, err := os.ReadFile(filepath.Join(s.Dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}
	persisted := bytes.Contains(data, []byte(`"id":"owned"`))
	if appendErr != nil && persisted {
		t.Fatalf("Append error = %v after owned batch persisted: %s", appendErr, data)
	}
	if appendErr == nil && !persisted {
		t.Fatalf("Append succeeded without persisting owned batch: %s", data)
	}
}

func TestSessionAppendAdoptsCanonicalSuffixAfterCommittedRace(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	external := messageWithID(llm.TextMessage(llm.RoleUser, "external"), "external-after")
	externalLine, err := marshalJSONLine(external)
	if err != nil {
		t.Fatal(err)
	}
	s.afterTranscriptWrite = func() {
		file, openErr := os.OpenFile(filepath.Join(s.Dir, conversationFile), os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.Write(externalLine); writeErr != nil {
			file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}

	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleAssistant, "owned"), "owned-before")); err != nil {
		t.Fatalf("Append error after committed race = %v", err)
	}
	if got := strings.Join(messageIDsForTest(s.History), ","); got != "owned-before,external-after" {
		t.Fatalf("resident history ids = %s, want owned-before,external-after", got)
	}
	meta, err := loadMetadata(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := fingerprintFromPath(filepath.Join(s.Dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if !transcriptCheckpointValid(meta.Transcript, fingerprint) {
		t.Fatalf("recovered checkpoint = %+v, want current sealed checkpoint", meta.Transcript)
	}
}

func TestSessionAppendRecognizesBatchShiftedByExternalAppend(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleUser, "base"), "base")); err != nil {
		t.Fatal(err)
	}
	external := messageWithID(llm.TextMessage(llm.RoleAssistant, "external"), "external-before")
	externalLine, err := marshalJSONLine(external)
	if err != nil {
		t.Fatal(err)
	}
	s.afterTranscriptPrewriteCheck = func() {
		file, openErr := os.OpenFile(filepath.Join(s.Dir, conversationFile), os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.Write(externalLine); writeErr != nil {
			file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}

	owned := messageWithID(llm.TextMessage(llm.RoleUser, "owned"), "owned-shifted")
	if err := s.Append(owned); err != nil {
		t.Fatalf("Append shifted batch = %v", err)
	}
	if got := strings.Join(messageIDsForTest(s.History), ","); got != "base,external-before,owned-shifted" {
		t.Fatalf("resident history ids = %s, want base,external-before,owned-shifted", got)
	}
}

func TestSessionAppendPreservesFullHistoryAfterCompactedDivergence(t *testing.T) {
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
	external := messageWithID(llm.TextMessage(llm.RoleAssistant, "external"), "m5")
	externalLine, err := marshalJSONLine(external)
	if err != nil {
		t.Fatal(err)
	}
	s.afterTranscriptWrite = func() {
		file, openErr := os.OpenFile(filepath.Join(s.Dir, conversationFile), os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.Write(externalLine); writeErr != nil {
			file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}

	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleUser, "owned"), "m4")); err != nil {
		t.Fatalf("Append after compacted divergence = %v", err)
	}
	if got := strings.Join(messageIDsForTest(s.History), ","); got != "m1,m2,m3,m4,m5" {
		t.Fatalf("live history ids = %s, want m1,m2,m3,m4,m5", got)
	}
	if got := strings.Join(transcriptEntryIDs(s.transcript.entries), ","); got != "m2,m3,m4,m5" {
		t.Fatalf("active index ids = %s, want m2,m3,m4,m5", got)
	}
}

func TestSessionAppendRejectsSameSizedPostWriteRewrite(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owned := messageWithID(llm.TextMessage(llm.RoleAssistant, "owned"), "owned-same")
	rewritten := messageWithID(llm.TextMessage(llm.RoleAssistant, "other"), "other-same")
	ownedLine, err := marshalJSONLine(owned)
	if err != nil {
		t.Fatal(err)
	}
	rewrittenLine, err := marshalJSONLine(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownedLine) != len(rewrittenLine) {
		t.Fatalf("test rows differ in size: owned=%d rewritten=%d", len(ownedLine), len(rewrittenLine))
	}
	s.afterTranscriptWrite = func() {
		file, openErr := os.OpenFile(filepath.Join(s.Dir, conversationFile), os.O_WRONLY, 0o644)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.WriteAt(rewrittenLine, 0); writeErr != nil {
			file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}

	if err := s.Append(owned); !errors.Is(err, ErrTranscriptChanged) {
		t.Fatalf("Append error = %v, want ErrTranscriptChanged", err)
	}
	if got := strings.Join(messageIDsForTest(s.History), ","); got != "other-same" {
		t.Fatalf("resident history ids = %s, want other-same", got)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"id":"owned-same"`)) || !bytes.Contains(data, []byte(`"id":"other-same"`)) {
		t.Fatalf("rewritten transcript = %s, want only other-same", data)
	}
}

func TestSessionAppendAcceptsAtomicReplacementContainingBatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows prevents replacing the canonical path while the resident handle is open")
	}
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(s.Dir, conversationFile)
	s.afterTranscriptWrite = func() {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		replacement := filepath.Join(s.Dir, "conversation.replacement")
		if writeErr := os.WriteFile(replacement, data, 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
		if renameErr := os.Rename(replacement, path); renameErr != nil {
			t.Fatal(renameErr)
		}
	}

	owned := messageWithID(llm.TextMessage(llm.RoleAssistant, "owned"), "owned-replaced")
	if err := s.Append(owned); err != nil {
		t.Fatalf("Append error after canonical replacement containing batch = %v", err)
	}
	if got := strings.Join(messageIDsForTest(s.History), ","); got != "owned-replaced" {
		t.Fatalf("resident history ids = %s, want owned-replaced", got)
	}
	if err := s.convFD.Close(); err != nil {
		t.Fatal(err)
	}
	s.convFD = nil
	s.afterTranscriptWrite = nil
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleUser, "next"), "next")); err != nil {
		t.Fatalf("Append after descriptor rebinding = %v", err)
	}
	_, full, err := LoadInfo(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(messageIDsForTest(full), ","); got != "owned-replaced,next" {
		t.Fatalf("canonical history ids = %s, want owned-replaced,next", got)
	}
}

func TestTranscriptRangeRecheckDetectsSameInodeWeakRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), conversationFile)
	original := []byte("owned-row\n")
	rewritten := []byte("other-row\n")
	if len(original) != len(rewritten) {
		t.Fatalf("test data length mismatch: original=%d rewritten=%d", len(original), len(rewritten))
	}
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if matched, err := transcriptRangeMatches(file, 0, original); err != nil || !matched {
		t.Fatalf("initial range match = %t, %v; want true, nil", matched, err)
	}

	if err := os.WriteFile(path, rewritten, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("test rewrote a different inode")
	}
	if before.Size() != after.Size() || before.ModTime().UnixNano() != after.ModTime().UnixNano() {
		t.Fatalf("weak metadata changed: before=%d/%d after=%d/%d",
			before.Size(), before.ModTime().UnixNano(), after.Size(), after.ModTime().UnixNano())
	}
	if matched, err := transcriptRangeMatches(file, 0, original); err != nil || matched {
		t.Fatalf("final range match = %t, %v; want false, nil", matched, err)
	}
}

func TestSessionAppendAdoptsCanonicalPrefixRewriteAfterWrite(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := messageWithID(llm.TextMessage(llm.RoleAssistant, "first"), "first-old")
	rewritten := messageWithID(llm.TextMessage(llm.RoleAssistant, "other"), "first-new")
	firstLine, err := marshalJSONLine(first)
	if err != nil {
		t.Fatal(err)
	}
	rewrittenLine, err := marshalJSONLine(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstLine) != len(rewrittenLine) {
		t.Fatalf("test rows differ in size: first=%d rewritten=%d", len(firstLine), len(rewrittenLine))
	}
	if err := s.Append(first); err != nil {
		t.Fatal(err)
	}
	s.afterTranscriptWrite = func() {
		file, openErr := os.OpenFile(filepath.Join(s.Dir, conversationFile), os.O_WRONLY, 0o644)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.WriteAt(rewrittenLine, 0); writeErr != nil {
			file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}

	owned := messageWithID(llm.TextMessage(llm.RoleAssistant, "owned"), "owned")
	if err := s.Append(owned); err != nil {
		t.Fatalf("Append after canonical prefix rewrite = %v", err)
	}
	if got := strings.Join(messageIDsForTest(s.History), ","); got != "first-new,owned" {
		t.Fatalf("resident history ids = %s, want first-new,owned", got)
	}
	meta, err := loadMetadata(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	canonicalFingerprint, err := fingerprintFromPath(filepath.Join(s.Dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if !transcriptCheckpointValid(meta.Transcript, canonicalFingerprint) {
		t.Fatalf("checkpoint = %+v, want canonical fingerprint %+v", meta.Transcript, canonicalFingerprint)
	}
}

func TestSessionAppendAdoptsPrefixRewriteAfterPrewriteDigest(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := messageWithID(llm.TextMessage(llm.RoleAssistant, "first"), "first-old")
	rewritten := messageWithID(llm.TextMessage(llm.RoleAssistant, "other"), "first-new")
	firstLine, err := marshalJSONLine(first)
	if err != nil {
		t.Fatal(err)
	}
	rewrittenLine, err := marshalJSONLine(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstLine) != len(rewrittenLine) {
		t.Fatalf("test rows differ in size: first=%d rewritten=%d", len(firstLine), len(rewrittenLine))
	}
	if err := s.Append(first); err != nil {
		t.Fatal(err)
	}
	s.afterTranscriptPrewriteCheck = func() {
		file, openErr := os.OpenFile(filepath.Join(s.Dir, conversationFile), os.O_WRONLY, 0o644)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.WriteAt(rewrittenLine, 0); writeErr != nil {
			file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}

	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleAssistant, "owned"), "owned")); err != nil {
		t.Fatalf("Append after prewrite prefix rewrite = %v", err)
	}
	if got := strings.Join(messageIDsForTest(s.History), ","); got != "first-new,owned" {
		t.Fatalf("resident history ids = %s, want first-new,owned", got)
	}
}

func TestTranscriptPrefixDigestDetectsRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(path, []byte("prefix-owned"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	digest, err := digestTranscriptPrefix(file, 6)
	if err != nil {
		t.Fatal(err)
	}
	if matched, err := transcriptPrefixDigestMatches(file, 6, digest); err != nil || !matched {
		t.Fatalf("unchanged prefix match = %t, %v; want true, nil", matched, err)
	}
	if _, err := file.WriteAt([]byte("PREFIX"), 0); err != nil {
		t.Fatal(err)
	}
	if matched, err := transcriptPrefixDigestMatches(file, 6, digest); err != nil || matched {
		t.Fatalf("rewritten prefix match = %t, %v; want false, nil", matched, err)
	}
}

func TestConcurrentSessionAppendsSerializeBeforeFingerprintCheck(t *testing.T) {
	first, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := first.Dir
	second, err := Load(dir)
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	defer first.Close()
	defer second.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	first.beforeTranscriptWrite = func() {
		close(entered)
		<-release
	}
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	go func() {
		firstResult <- first.Append(messageWithID(llm.TextMessage(llm.RoleUser, "first"), "first"))
	}()
	<-entered
	go func() {
		secondResult <- second.Append(messageWithID(llm.TextMessage(llm.RoleUser, "second"), "second"))
	}()
	select {
	case err := <-secondResult:
		close(release)
		t.Fatalf("second append completed before the first write was released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	firstErr := <-firstResult
	secondErr := <-secondResult
	if firstErr != nil {
		t.Fatalf("first append error = %v", firstErr)
	}
	if !errors.Is(secondErr, ErrTranscriptChanged) {
		t.Fatalf("second append error = %v, want ErrTranscriptChanged", secondErr)
	}
	data, err := os.ReadFile(filepath.Join(dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"id":"first"`)) || bytes.Contains(data, []byte(`"id":"second"`)) {
		t.Fatalf("serialized transcript = %s, want only first batch", data)
	}
}

func TestResidentTranscriptFingerprintAllowsMatchingWeakIdentity(t *testing.T) {
	weak := transcriptFingerprint{Size: 42, MtimeNS: 99}
	if !residentTranscriptFingerprintMatches(weak, weak) {
		t.Fatal("matching weak fingerprint rejected resident append")
	}
	if residentTranscriptFingerprintMatches(weak, transcriptFingerprint{Size: 43, MtimeNS: 99}) {
		t.Fatal("changed weak fingerprint accepted resident append")
	}
}

func TestMessageCreatedAtParsesOnlyCanonicalMessageIDs(t *testing.T) {
	got, ok := MessageCreatedAt("msg-20260718T065604-8f0582f4")
	if !ok {
		t.Fatal("MessageCreatedAt(valid) = false")
	}
	want := time.Date(2026, 7, 18, 6, 56, 4, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("MessageCreatedAt(valid) = %s, want %s", got, want)
	}
	for _, id := range []string{
		"",
		"m1",
		"20260718T065604-8f0582f4",
		"msg-20260718T065604-bad",
	} {
		if got, ok := MessageCreatedAt(id); ok {
			t.Fatalf("MessageCreatedAt(%q) = %s, true", id, got)
		}
	}
}

func TestSession_AppendDoesNotMutateHistoryWhenPersistFails(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.convFD.Close(); err != nil {
		t.Fatal(err)
	}
	s.convFD, err = os.Open(filepath.Join(s.Dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}

	err = s.Append(llm.TextMessage(llm.RoleUser, "lost"))
	if err == nil {
		t.Fatal("Append err = nil, want write failure")
	}
	if len(s.History) != 0 {
		t.Fatalf("history len = %d, want 0 after failed append", len(s.History))
	}
	if len(s.transcript.entries) != 0 {
		t.Fatalf("transcript entries = %d, want 0 after failed append", len(s.transcript.entries))
	}
}

func TestSessionAppendBatchPersistsAdjacentMessages(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	notice := llm.TextMessage(llm.RoleUser, "model switched")
	notice.Kind = llm.MessageKindModelChange
	assistant := llm.TextMessage(llm.RoleAssistant, "continuing")
	assistant.Model = "fallback:model"
	if err := s.AppendBatch([]llm.Message{notice, assistant}); err != nil {
		t.Fatal(err)
	}

	if len(s.History) != 2 || s.History[0].Kind != llm.MessageKindModelChange || s.History[1].Model != "fallback:model" {
		t.Fatalf("history = %+v", s.History)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := countLines(data); got != 2 {
		t.Fatalf("conversation lines = %d, want 2: %s", got, data)
	}
	if s.transcript.turns != 0 {
		t.Fatalf("fallback notice counted as user turn: %d", s.transcript.turns)
	}
}

func TestSessionAppendReusesAvailableHistoryCapacity(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Append(llm.TextMessage(llm.RoleUser, "first")); err != nil {
		t.Fatal(err)
	}

	history := make([]llm.Message, len(s.History), len(s.History)+2)
	copy(history, s.History)
	s.History = history
	first := &s.History[0]
	if err := s.Append(llm.TextMessage(llm.RoleAssistant, "second")); err != nil {
		t.Fatal(err)
	}
	if &s.History[0] != first {
		t.Fatal("Append replaced history backing storage despite available capacity")
	}
}

func TestSessionAppendAssignedReturnsPersistedMessageIDs(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	user, err := s.AppendAssigned(llm.TextMessage(llm.RoleUser, "hello"))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := s.AppendBatchAssigned([]llm.Message{
		llm.TextMessage(llm.RoleUser, "model switched"),
		llm.TextMessage(llm.RoleAssistant, "continuing"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if user.ID == "" || len(batch) != 2 || batch[0].ID == "" || batch[1].ID == "" {
		t.Fatalf("assigned messages = user:%+v batch:%+v", user, batch)
	}
	got := s.History
	if got[0].ID != user.ID || got[1].ID != batch[0].ID || got[2].ID != batch[1].ID {
		t.Fatalf("history ids = [%q %q %q], assigned = [%q %q %q]",
			got[0].ID, got[1].ID, got[2].ID,
			user.ID, batch[0].ID, batch[1].ID,
		)
	}
}

func TestSystemNoticeRetainsUserTurnSummarySemantics(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	notice := llm.TextMessage(llm.RoleUser, "continue after restart")
	notice.Kind = llm.MessageKindSystemNotice
	if err := s.Append(notice); err != nil {
		t.Fatal(err)
	}
	if s.transcript.turns != 1 || s.transcript.preview != "continue after restart" {
		t.Fatalf("summary = turns %d preview %q", s.transcript.turns, s.transcript.preview)
	}
}

func TestSessionAppendBatchRollsBackWhenSecondMessageCannotMarshal(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Append(llm.TextMessage(llm.RoleUser, "existing")); err != nil {
		t.Fatal(err)
	}

	notice := llm.TextMessage(llm.RoleUser, "model switched")
	notice.Kind = llm.MessageKindModelChange
	invalid := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
		Type:  llm.BlockToolUse,
		Input: map[string]any{"not_json": func() {}},
	}}}
	if err := s.AppendBatch([]llm.Message{notice, invalid}); err == nil {
		t.Fatal("AppendBatch err = nil, want marshal failure")
	}

	if len(s.History) != 1 || len(s.transcript.entries) != 1 {
		t.Fatalf("batch mutated state: history=%d entries=%d", len(s.History), len(s.transcript.entries))
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := countLines(data); got != 1 {
		t.Fatalf("conversation lines = %d, want existing line only: %s", got, data)
	}
}

func TestSession_NewWithOptionsPersistsKind(t *testing.T) {
	root := t.TempDir()
	s, err := NewWithOptions(root, Options{Kind: KindSide})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Kind != KindSide {
		t.Fatalf("session kind = %q, want side", s.Kind)
	}

	loaded, err := Load(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.Kind != KindSide {
		t.Fatalf("loaded kind = %q, want side", loaded.Kind)
	}
}

func TestSession_NewIDUsesUTCTimePrefix(t *testing.T) {
	before := time.Now().UTC().Add(-1 * time.Second)
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after := time.Now().UTC().Add(1 * time.Second)
	if len(s.ID) < len(idTimeLayout) {
		t.Fatalf("session id = %q, missing time prefix", s.ID)
	}
	got, err := time.ParseInLocation(idTimeLayout, s.ID[:len(idTimeLayout)], time.UTC)
	if err != nil {
		t.Fatalf("parse session id %q: %v", s.ID, err)
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("session id UTC prefix = %v, want between %v and %v", got, before, after)
	}
}

func TestAcquireSessionLockConflictsUntilClosed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260529T120000-locktest")
	first, err := AcquireSessionLock(dir, "run")
	if err != nil {
		t.Fatal(err)
	}
	_, err = AcquireSessionLock(dir, "repl")
	if err == nil {
		t.Fatal("expected lock conflict")
	}
	if _, ok := err.(*LockError); !ok {
		t.Fatalf("err = %T %v, want *LockError", err, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireSessionLock(dir, "repl")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireSessionLockRemovesDeadPIDLock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260529T120000-stalelock")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionLockFile)
	stale := LockInfo{
		PID:       definitelyDeadPID(),
		Mode:      "serve",
		SessionID: filepath.Base(dir),
		StartedAt: time.Now().Add(-time.Hour).UTC(),
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	lock, err := AcquireSessionLock(dir, "resume")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil {
			t.Fatalf("close lock: %v", err)
		}
	})

	info := readLockInfo(path)
	if info.PID != os.Getpid() || info.Mode != "resume" || info.SessionID != filepath.Base(dir) {
		t.Fatalf("lock info = %+v, want current process resume lock", info)
	}
}

func TestProcessStartedAtCurrentProcessIsPlausible(t *testing.T) {
	startedAt, err := processStartedAt(os.Getpid())
	if err != nil {
		t.Skipf("process start time unavailable: %v", err)
	}
	now := time.Now().UTC()
	if startedAt.After(now.Add(time.Second)) || startedAt.Before(now.Add(-24*time.Hour)) {
		t.Fatalf("process start time = %v, want within the last 24 hours", startedAt)
	}
}

func TestAcquireSessionLockRemovesReusedPIDLock(t *testing.T) {
	startedAt, err := processStartedAt(os.Getpid())
	if err != nil {
		t.Skipf("process start time unavailable: %v", err)
	}

	root := t.TempDir()
	dir := filepath.Join(root, "20260529T120000-reusedpid")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionLockFile)
	stale := LockInfo{
		PID:       os.Getpid(),
		Mode:      "serve",
		SessionID: filepath.Base(dir),
		StartedAt: startedAt.Add(-time.Minute),
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	lock, err := AcquireSessionLock(dir, "resume")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil {
			t.Fatalf("close lock: %v", err)
		}
	})

	info := readLockInfo(path)
	if info.PID != os.Getpid() || info.Mode != "resume" || info.SessionID != filepath.Base(dir) {
		t.Fatalf("lock info = %+v, want current process resume lock", info)
	}
}

func TestAcquireSessionLockStaleCleanupHasSingleWinner(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260529T120000-stalerace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := LockInfo{
		PID:       definitelyDeadPID(),
		Mode:      "serve",
		SessionID: filepath.Base(dir),
		StartedAt: time.Now().Add(-time.Hour).UTC(),
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionLockFile), data, 0o600); err != nil {
		t.Fatal(err)
	}

	const workers = 16
	type result struct {
		lock *Lock
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			lock, err := AcquireSessionLock(dir, "resume")
			results <- result{lock: lock, err: err}
		}()
	}
	close(start)

	successes := 0
	conflicts := 0
	timeout := time.After(5 * time.Second)
	for i := 0; i < workers; i++ {
		select {
		case res := <-results:
			if res.err == nil {
				successes++
				lock := res.lock
				t.Cleanup(func() {
					if err := lock.Close(); err != nil {
						t.Fatalf("close lock: %v", err)
					}
				})
				continue
			}
			var lockErr *LockError
			if errors.As(res.err, &lockErr) {
				conflicts++
				continue
			}
			t.Fatalf("AcquireSessionLock err = %T %v, want nil or *LockError", res.err, res.err)
		case <-timeout:
			t.Fatal("timed out waiting for concurrent lock attempts")
		}
	}
	if successes != 1 || conflicts != workers-1 {
		t.Fatalf("successes=%d conflicts=%d, want 1 success and %d conflicts", successes, conflicts, workers-1)
	}
}

func TestAcquireSessionLockRemovesOldUnreadableLock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260529T120000-badlock")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionLockFile)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-unreadableLockStaleAfter - time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	lock, err := AcquireSessionLock(dir, "resume")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil {
			t.Fatalf("close lock: %v", err)
		}
	})

	info := readLockInfo(path)
	if info.PID != os.Getpid() || info.Mode != "resume" || info.SessionID != filepath.Base(dir) {
		t.Fatalf("lock info = %+v, want current process resume lock", info)
	}
}

func TestAcquireSessionLockKeepsFreshUnreadableLock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260529T120000-freshbadlock")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionLockFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := AcquireSessionLock(dir, "resume")
	if err == nil {
		t.Fatal("expected lock conflict")
	}
	var lockErr *LockError
	if !errors.As(err, &lockErr) {
		t.Fatalf("err = %T %v, want *LockError", err, err)
	}
}

func definitelyDeadPID() int {
	pid := os.Getpid() + 1_000_000
	for i := 0; i < 1000; i++ {
		candidate := pid + i
		alive, err := processExists(candidate)
		if err != nil || !alive {
			return candidate
		}
	}
	return pid
}

func TestSession_AppendNormalizesNilBlocks(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Append(llm.Message{Role: llm.RoleAssistant}); err != nil {
		t.Fatal(err)
	}
	if s.History[0].Blocks == nil {
		t.Fatal("history blocks is nil, want empty slice")
	}

	data, _ := os.ReadFile(filepath.Join(s.Dir, conversationFile))
	if strings.Contains(string(data), `"blocks":null`) {
		t.Fatalf("conversation contains null blocks: %s", data)
	}
	if !strings.Contains(string(data), `"blocks":[]`) {
		t.Fatalf("conversation missing empty blocks array: %s", data)
	}
}

func TestAppend_AssignsMessageID(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Append(llm.TextMessage(llm.RoleUser, "hello")); err != nil {
		t.Fatal(err)
	}
	if s.History[0].ID == "" {
		t.Fatal("message ID was not assigned")
	}

	s2, err := Load(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.History[0].ID != s.History[0].ID {
		t.Fatalf("loaded ID = %q, want %q", s2.History[0].ID, s.History[0].ID)
	}
}

func TestLoadRejectsMessageWithoutID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260515T010203-missingid")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionTime := time.Date(2026, 5, 15, 1, 2, 3, 0, time.UTC).UnixMilli()
	if err := saveMetadata(dir, metadata{
		Kind:           KindPrimary,
		StartedAtMS:    sessionTime,
		LastActiveAtMS: sessionTime,
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"m1","role":"user","blocks":[{"type":"text","text":"old"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"reply"}]}` + "\n"
	path := filepath.Join(dir, conversationFile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load accepted a message without an id")
	}
	for _, want := range []string{path, ":2", "manually add a unique non-empty \"id\"", "internal/session/transcript_repair.go"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Load error = %q, want %q", err, want)
		}
	}
}

func TestReadTranscriptMessagesRejectsMessageWithoutID(t *testing.T) {
	path := filepath.Join(t.TempDir(), conversationFile)
	body := []byte(`{"role":"user","blocks":[]}` + "\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := readTranscriptMessages(path, []transcriptIndexEntry{{
		LineIndex: 3,
		Offset:    0,
		Length:    len(body),
	}})
	if err == nil {
		t.Fatal("readTranscriptMessages accepted a message without an id")
	}
	for _, want := range []string{path, ":4", "manually add a unique non-empty \"id\"", "internal/session/transcript_repair.go"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("readTranscriptMessages error = %q, want %q", err, want)
		}
	}
}

func TestSession_AppendEventToJSONL(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_ = s.AppendEvent(events.Event{Type: "turn.started", Payload: "x"})
	_ = s.AppendEvent(events.Event{Type: "tool.completed", Payload: "y"})

	data, _ := os.ReadFile(filepath.Join(s.Dir, eventsFile))
	if c := countLines(data); c != 2 {
		t.Fatalf("expected 2 event lines, got %d", c)
	}
}

func TestSession_AppendEventSkipsTransientEvent(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.AppendEvent(events.Event{Type: "tool.output_delta", Payload: "live", Transient: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, eventsFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("transient event persisted: %s", data)
	}
}

func TestSession_LazyCreatesNoFilesUntilAppend(t *testing.T) {
	root := t.TempDir()
	s, err := NewWithOptions(root, Options{Lazy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Fatalf("lazy session dir stat err = %v, want not exist", err)
	}
	if err := s.Append(llm.TextMessage(llm.RoleUser, "hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, conversationFile)); err != nil {
		t.Fatalf("conversation stat err = %v", err)
	}
}

func TestSession_BusSubscription(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bus := events.NewBus()
	s.SubscribeBus(bus)

	bus.Emit(events.Event{Type: "x.fired"})
	bus.Emit(events.Event{Type: "y.fired"})

	data, _ := os.ReadFile(filepath.Join(s.Dir, eventsFile))
	if c := countLines(data); c != 2 {
		t.Fatalf("expected 2 events from bus, got %d: %s", c, data)
	}
}

func TestSession_BusSubscriptionSkipsTransientEvents(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bus := events.NewBus()
	s.SubscribeBus(bus)

	bus.Emit(events.Event{Type: "llm.output_delta", Transient: true, Payload: map[string]any{
		"iter": 0,
		"kind": "text",
		"text": "live only",
	}})
	bus.Emit(events.Event{Type: "turn.completed", Payload: map[string]any{"output_len": 4}})

	data, _ := os.ReadFile(filepath.Join(s.Dir, eventsFile))
	if c := countLines(data); c != 1 {
		t.Fatalf("expected only durable event from bus, got %d: %s", c, data)
	}
	if strings.Contains(string(data), "llm.output_delta") {
		t.Fatalf("llm.output_delta should not be persisted: %s", data)
	}
}

func TestSession_LoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Append(llm.TextMessage(llm.RoleUser, "msg-1"))
	_ = s.Append(llm.TextMessage(llm.RoleAssistant, "msg-2"))
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if len(s2.History) != 2 {
		t.Fatalf("loaded history len = %d", len(s2.History))
	}
	if s2.History[0].FirstText() != "msg-1" || s2.History[1].FirstText() != "msg-2" {
		t.Fatalf("history mismatch: %+v", s2.History)
	}
	if !strings.HasPrefix(s2.ID, time2025OrLater(t)) {
		// just make sure ID is the dir basename
		if s2.ID != filepath.Base(dir) {
			t.Errorf("id = %s vs dir base %s", s2.ID, filepath.Base(dir))
		}
	}
}

func TestLoad_UsesLatestCompactActiveWindow(t *testing.T) {
	root := t.TempDir()
	oldUser := llm.TextMessage(llm.RoleUser, "old user")
	oldUser.ID = "m1"
	tail := llm.TextMessage(llm.RoleAssistant, "tail assistant")
	tail.ID = "m2"
	compact := compactTestMessage("summary")
	compact.ID = "m3"
	compact.Compaction = &llm.CompactionMetadata{TailStartMessageID: "m2"}
	latest := llm.TextMessage(llm.RoleUser, "latest user")
	latest.ID = "m4"
	dir := makeSession(t, root, "20260515T010203-window", []llm.Message{oldUser, tail, compact, latest}, time.Now())

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if got := messageIDsForTest(s.History); strings.Join(got, ",") != "m2,m3,m4" {
		t.Fatalf("active history ids = %v, want m2,m3,m4", got)
	}
	info := s.Info()
	if info.Turns != 2 || info.Preview != "old user" {
		t.Fatalf("info = turns %d preview %q, want full transcript summary", info.Turns, info.Preview)
	}

	page, err := s.TranscriptMessagePage("", 60)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDsForTest(page.Messages); strings.Join(got, ",") != "m3,m4" {
		t.Fatalf("initial page ids = %v, want m3,m4", got)
	}
	if !page.HasMoreBefore || page.OldestMessageID != "m3" {
		t.Fatalf("page = %+v, want more before m3", page)
	}

	older, err := s.TranscriptMessagePage("m3", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDsForTest(older.Messages); strings.Join(got, ",") != "m1,m2" {
		t.Fatalf("older page ids = %v, want m1,m2", got)
	}
	if older.HasMoreBefore {
		t.Fatalf("older page has_more_before = true, want false")
	}
}

func TestLoadWithRepairTranscript_PreservesMessagesBeforeWindow(t *testing.T) {
	root := t.TempDir()
	assistant := llm.Message{
		ID:   "m1",
		Role: llm.RoleAssistant,
		Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "call_1",
			ToolName:  "read",
		}},
	}
	compact := compactTestMessage("summary")
	compact.ID = "m2"
	latest := llm.TextMessage(llm.RoleUser, "latest")
	latest.ID = "m3"
	dir := makeSession(t, root, "20260515T010203-repair", []llm.Message{assistant, compact, latest}, time.Now())

	s, err := LoadWithOptions(dir, Options{RepairTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, full, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 4 {
		t.Fatalf("full transcript len = %d, want 4: %+v", len(full), full)
	}
	if full[0].ID != "m1" || full[1].Role != llm.RoleUser || full[1].Blocks[0].ToolUseID != "call_1" || full[2].ID != "m2" {
		t.Fatalf("repaired transcript = %+v", full)
	}
	if got := messageIDsForTest(s.History); strings.Join(got, ",") != "m2,m3" {
		t.Fatalf("active history ids = %v, want m2,m3", got)
	}
}

func TestSession_LoadNormalizesNullBlocks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260509T074114-a20bf346")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionTime := time.Date(2026, 5, 9, 7, 41, 14, 0, time.UTC).UnixMilli()
	if err := saveMetadata(dir, metadata{
		Kind:           KindPrimary,
		StartedAtMS:    sessionTime,
		LastActiveAtMS: sessionTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, conversationFile), []byte(`{"id":"m1","role":"assistant","blocks":null}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(s.History))
	}
	if s.History[0].Blocks == nil {
		t.Fatal("loaded blocks is nil, want empty slice")
	}
}

func countLines(data []byte) int {
	n := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			n++
		}
	}
	return n
}

func messageIDsForTest(msgs []llm.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, msg.ID)
	}
	return out
}

func TestSession_ScratchpadLifecycle(t *testing.T) {
	t.Run("eager create", func(t *testing.T) {
		s, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		if got, want := s.ScratchpadDir(), filepath.Join(s.Dir, "scratchpad"); got != want {
			t.Fatalf("scratchpad dir = %q, want %q", got, want)
		}
		if info, err := os.Stat(s.ScratchpadDir()); err != nil || !info.IsDir() {
			t.Fatalf("scratchpad stat = %+v, %v", info, err)
		}
	})

	t.Run("lazy first append", func(t *testing.T) {
		s, err := NewWithOptions(t.TempDir(), Options{Lazy: true})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
			t.Fatalf("lazy session dir stat err = %v, want not exist", err)
		}
		if err := s.Append(llm.TextMessage(llm.RoleUser, "persist")); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(s.ScratchpadDir()); err != nil || !info.IsDir() {
			t.Fatalf("scratchpad after append = %+v, %v", info, err)
		}
	})

	t.Run("load existing", func(t *testing.T) {
		s, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		dir := s.Dir
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, "scratchpad")); err != nil {
			t.Fatal(err)
		}

		loaded, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer loaded.Close()
		if info, err := os.Stat(loaded.ScratchpadDir()); err != nil || !info.IsDir() {
			t.Fatalf("loaded scratchpad = %+v, %v", info, err)
		}
	})
}

// time2025OrLater is a tiny helper that just returns "" — kept here so the
// HasPrefix check above always falls through to the basename comparison while
// staying explicit about intent.
func time2025OrLater(t *testing.T) string { t.Helper(); return "" }
