package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

// makeSession creates a session dir under root with the given id and
// pre-populates conversation.jsonl with one message per element of msgs.
// mtime sets the file's modification time so list ordering tests are stable.
func makeSession(t *testing.T, root, id string, msgs []llm.Message, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionTime := mtime
	if sessionTime.IsZero() {
		sessionTime = time.Now().UTC()
	}
	sessionTimeMS := sessionTime.UnixMilli()
	if err := saveMetadata(dir, metadata{
		Kind:           KindPrimary,
		StartedAtMS:    sessionTimeMS,
		LastActiveAtMS: sessionTimeMS,
	}); err != nil {
		t.Fatal(err)
	}
	convPath := filepath.Join(dir, "conversation.jsonl")
	f, err := os.Create(convPath)
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range msgs {
		if m.ID == "" {
			m.ID = fmt.Sprintf("m%d", i+1)
		}
		buf, err := marshalTranscriptJournalLine(id, uint64(i+1), m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(convPath, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeEvents(t *testing.T, dir string, evs []events.Event) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range evs {
		buf, err := marshalEventJournalLine(filepath.Base(dir), uint64(i+1), e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func withTranscriptFingerprint(t *testing.T, info Info, dir string) Info {
	t.Helper()
	fingerprint, err := fingerprintFromPath(filepath.Join(dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}
	info.transcript = fingerprint
	return info
}

func writeTranscriptCheckpoint(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, conversationFile)
	idx, err := scanTranscriptIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.Transcript = buildTranscriptCheckpoint(idx, idx.fingerprint)
	if meta.Transcript == nil {
		t.Fatal("build transcript checkpoint")
	}
	if err := saveMetadata(dir, meta); err != nil {
		t.Fatal(err)
	}
}

func TestInfoDirPrefersID(t *testing.T) {
	got := InfoDir("/sessions", Info{ID: "abc", Dir: "/recorded"})
	if got != filepath.Join("/sessions", "abc") {
		t.Fatalf("InfoDir = %q, want canonical ID path", got)
	}
}

func TestInfoDirFallsBackToDir(t *testing.T) {
	got := InfoDir("/sessions", Info{Dir: "/recorded"})
	if got != "/recorded" {
		t.Fatalf("InfoDir = %q, want recorded dir", got)
	}
}

func TestListWithHistoryBoundsUsageEventTailScan(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(t.TempDir(), "history.json")
	mtime := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	id := "20260727T120000-b0a0ded1"
	dir := makeSession(t, root, id,
		[]llm.Message{llm.TextMessage(llm.RoleUser, "bounded")},
		mtime)
	writeTranscriptCheckpoint(t, dir)
	if err := RecordSession(historyPath, withTranscriptFingerprint(t, Info{
		ID:           id,
		Dir:          dir,
		Kind:         KindPrimary,
		StartedAt:    mtime,
		LastActiveAt: mtime,
		Turns:        1,
		Preview:      "bounded",
	}, dir)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, eventsFile)
	outsideTail := `{"type":"llm.responded","payload":{"context_usage":{"model":"outside-tail","total_tokens":7}}}` + "\n"
	paddingLine := `{"type":"tool.output"}` + "\n"
	padding := strings.Repeat(paddingLine, int(maxSessionUsageScanBytes/int64(len(paddingLine)))+1)
	latest := `{"type":"llm.responded","payload":{"token_usage":{"input_tokens":10,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(path, []byte(outsideTail+padding+latest), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ListWithHistory(root, historyPath)
	if err != nil {
		t.Fatalf("ListWithHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("sessions = %+v", got)
	}
	if got[0].TokenUsage != (llm.Usage{InputTokens: 10, OutputTokens: 2}) {
		t.Fatalf("token usage = %+v", got[0].TokenUsage)
	}
	if got[0].ContextUsage != nil {
		t.Fatalf("context usage = %+v, want nil for event outside bounded tail", got[0].ContextUsage)
	}
	_, strictContextUsage, err := loadLatestSessionUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strictContextUsage == nil || strictContextUsage.Model != "outside-tail" {
		t.Fatalf("strict context usage = %+v, want value outside bounded tail", strictContextUsage)
	}
}

func TestHasConversation(t *testing.T) {
	if HasConversation("") {
		t.Fatal("HasConversation(\"\") = true, want false")
	}
	dir := t.TempDir()
	if HasConversation(dir) {
		t.Fatal("HasConversation(empty dir) = true, want false")
	}
	if err := os.WriteFile(filepath.Join(dir, conversationFile), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasConversation(dir) {
		t.Fatal("HasConversation(dir) = false, want true")
	}
}

func TestLoadInfo_DefaultsKindToPrimary(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-primary1",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "primary")},
		time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))

	info, _, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != KindPrimary {
		t.Fatalf("kind = %q, want primary", info.Kind)
	}
}

func TestSetKindAndLoadInfo(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-side0001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "side")},
		time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))

	if err := SetKind(dir, KindSide); err != nil {
		t.Fatal(err)
	}
	info, _, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != KindSide {
		t.Fatalf("kind = %q, want side", info.Kind)
	}
}

func TestList_SortsByLastActiveDesc(t *testing.T) {
	root := t.TempDir()
	older := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	makeSession(t, root, "20260501T100000-aaaa1111",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "older")}, older)
	makeSession(t, root, "20260502T100000-bbbb2222",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "newer")}, newer)

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "20260502T100000-bbbb2222" {
		t.Errorf("got[0].ID = %s, want newer first", got[0].ID)
	}
	if got[1].ID != "20260501T100000-aaaa1111" {
		t.Errorf("got[1].ID = %s, want older second", got[1].ID)
	}
}

func TestListWithHistoryDoesNotRescanMatchingTranscriptFingerprint(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(t.TempDir(), "history.json")
	mtime := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	id := "20260727T120000-cached01"
	dir := makeSession(t, root, id,
		[]llm.Message{llm.TextMessage(llm.RoleUser, "cached transcript summary")},
		mtime)
	writeTranscriptCheckpoint(t, dir)
	if err := RecordSession(historyPath, withTranscriptFingerprint(t, Info{
		ID:           id,
		Dir:          "/stale/recorded/path",
		Kind:         KindPrimary,
		Active:       true,
		StartedAt:    mtime,
		LastActiveAt: mtime,
		Turns:        1,
		Preview:      "cached transcript summary",
		TokenUsage:   llm.Usage{InputTokens: 1},
	}, dir)); err != nil {
		t.Fatal(err)
	}
	if err := SetKind(dir, KindSide); err != nil {
		t.Fatal(err)
	}
	if err := SetAlias(dir, "current alias"); err != nil {
		t.Fatal(err)
	}
	writeEvents(t, dir, []events.Event{
		{
			Type: "llm.responded",
			Payload: map[string]any{
				"token_usage":   llm.Usage{InputTokens: 10, OutputTokens: 2},
				"context_usage": llm.ContextUsage{Model: "mock", TotalTokens: 12},
			},
		},
		{
			Type: "llm.responded",
			Payload: map[string]any{
				"token_usage": llm.Usage{InputTokens: 20, OutputTokens: 4},
			},
		},
	})
	eventsPath := filepath.Join(dir, eventsFile)
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("malformed tail"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	scans := 0
	got, err := listWithHistoryLoader(root, historyPath, func(dir string) (Info, transcriptIndex, error) {
		scans++
		return loadInfoSummary(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	if scans != 0 {
		t.Fatalf("journal scans = %d, want 0 for matching transcript fingerprint", scans)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1; got %+v", len(got), got)
	}
	info := got[0]
	if info.ID != id || info.Dir != dir {
		t.Fatalf("identity = %q %q, want %q %q", info.ID, info.Dir, id, dir)
	}
	if info.Alias != "current alias" || info.Kind != KindSide || info.Active {
		t.Fatalf("metadata = alias %q kind %q active %t", info.Alias, info.Kind, info.Active)
	}
	if info.Turns != 1 || info.Preview != "cached transcript summary" {
		t.Fatalf("transcript summary = turns %d preview %q", info.Turns, info.Preview)
	}
	if info.TokenUsage != (llm.Usage{InputTokens: 20, OutputTokens: 4}) {
		t.Fatalf("token usage = %+v", info.TokenUsage)
	}
	if info.ContextUsage == nil || info.ContextUsage.Model != "mock" || info.ContextUsage.TotalTokens != 12 {
		t.Fatalf("context usage = %+v", info.ContextUsage)
	}
}

func TestCachedInfoRejectsWeakTranscriptFingerprint(t *testing.T) {
	root := t.TempDir()
	id := "20260727T120000-weak0001"
	dir := makeSession(t, root, id,
		[]llm.Message{llm.TextMessage(llm.RoleUser, "current")}, time.Now())
	current, _, err := loadInfoSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	cached := current
	cached.Turns = 99
	cached.transcript.ChangeID = ""
	scans := 0
	got, scanned, err := cachedOrScannedInfo(dir, id, map[string]Info{id: cached}, func(dir string) (Info, transcriptIndex, error) {
		scans++
		return loadInfoSummary(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !scanned || scans != 1 {
		t.Fatalf("weak cache scan = %t/%d, want true/1", scanned, scans)
	}
	if got.Turns != 1 || got.Preview != "current" {
		t.Fatalf("summary = turns %d preview %q, want canonical 1/current", got.Turns, got.Preview)
	}
}

func TestListWithHistoryInvalidatesChangedTranscript(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(t.TempDir(), "history.json")
	mtime := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	id := "20260727T120000-stale001"
	dir := makeSession(t, root, id,
		[]llm.Message{llm.TextMessage(llm.RoleUser, "before")},
		mtime)
	if err := RecordSession(historyPath, withTranscriptFingerprint(t, Info{
		ID:           id,
		Dir:          dir,
		Kind:         KindPrimary,
		StartedAt:    mtime,
		LastActiveAt: mtime,
		Turns:        1,
		Preview:      "before",
	}, dir)); err != nil {
		t.Fatal(err)
	}
	convPath := filepath.Join(dir, conversationFile)
	if err := os.WriteFile(convPath, []byte("not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(convPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	if _, err := ListWithHistory(root, historyPath); err == nil {
		t.Fatal("ListWithHistory accepted a changed malformed transcript")
	}
}

func TestLateStaleHistoryRecordCannotCreateFalseCacheHit(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(t.TempDir(), "history.json")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.TextMessage(llm.RoleUser, "first")); err != nil {
		t.Fatal(err)
	}
	stale := s.Info()
	if err := s.Append(llm.TextMessage(llm.RoleUser, "second")); err != nil {
		t.Fatal(err)
	}
	latest := s.Info()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if err := SetActive(historyPath, latest); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(historyPath, stale); err != nil {
		t.Fatal(err)
	}
	scans := 0
	infos, err := listWithHistoryLoader(root, historyPath, func(dir string) (Info, transcriptIndex, error) {
		scans++
		return loadInfoSummary(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Fatalf("journal scans = %d, want 1 after stale fingerprint arrives late", scans)
	}
	if len(infos) != 1 || infos[0].Turns != 2 || infos[0].Preview != "first" {
		t.Fatalf("sessions = %+v, want freshly scanned two-turn summary", infos)
	}
	infos, err = listWithHistoryLoader(root, historyPath, func(dir string) (Info, transcriptIndex, error) {
		scans++
		return loadInfoSummary(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Fatalf("journal scans after cache repair = %d, want 1", scans)
	}
	if len(infos) != 1 || infos[0].Turns != 2 || infos[0].Preview != "first" {
		t.Fatalf("cached sessions = %+v, want repaired two-turn summary", infos)
	}
	history, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Sessions) != 1 || history.Sessions[0].transcript != latest.transcript {
		t.Fatalf("history sessions = %+v, want repaired current fingerprint %+v", history.Sessions, latest.transcript)
	}
	if history.Active == nil || history.Active.ID != latest.ID {
		t.Fatalf("history active = %+v, want %q preserved", history.Active, latest.ID)
	}
}

func TestListWithHistoryDoesNotRepairSummaryWhenTranscriptChangesDuringScan(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(t.TempDir(), "history.json")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.TextMessage(llm.RoleUser, "first")); err != nil {
		t.Fatal(err)
	}
	stale := s.Info()
	if err := s.Append(llm.TextMessage(llm.RoleUser, "second")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(historyPath, stale); err != nil {
		t.Fatal(err)
	}

	scans := 0
	infos, err := listWithHistoryLoader(root, historyPath, func(dir string) (Info, transcriptIndex, error) {
		scans++
		info, idx, err := loadInfoSummary(dir)
		if err != nil {
			return Info{}, transcriptIndex{}, err
		}
		if scans == 1 {
			file, err := os.OpenFile(filepath.Join(dir, conversationFile), os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return Info{}, transcriptIndex{}, err
			}
			message := llm.TextMessage(llm.RoleUser, "third")
			message.ID = "m3"
			line := mustTranscriptLine(t, filepath.Base(dir), 3, message)
			_, err = file.Write(line)
			closeErr := file.Close()
			if err != nil {
				return Info{}, transcriptIndex{}, err
			}
			if closeErr != nil {
				return Info{}, transcriptIndex{}, closeErr
			}
		}
		return info, idx, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Turns != 2 {
		t.Fatalf("first sessions = %+v, want scan snapshot before concurrent append", infos)
	}

	if _, err := listWithHistoryLoader(root, historyPath, func(dir string) (Info, transcriptIndex, error) {
		scans++
		return loadInfoSummary(dir)
	}); err != nil {
		t.Fatal(err)
	}
	if scans != 2 {
		t.Fatalf("journal scans = %d, want second scan after skipped stale repair", scans)
	}
	history, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Sessions) != 1 || history.Sessions[0].transcript == infos[0].transcript {
		t.Fatalf("history sessions = %+v, stale scan fingerprint was recorded", history.Sessions)
	}
}

func TestLoadInfoUsesStoredUTCSessionTimesForCosmeticID(t *testing.T) {
	root := t.TempDir()
	stored := time.Date(2026, 7, 29, 18, 45, 12, 345000000, time.FixedZone("CST", 8*60*60))
	dir := makeSession(t, root, "20261318T065604-8f0582f4",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "hello")},
		stored)

	info, _, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := stored.UTC().Truncate(time.Millisecond)
	if !info.StartedAt.Equal(want) || info.StartedAt.Location() != time.UTC {
		t.Fatalf("started_at = %v (%v), want %v UTC", info.StartedAt, info.StartedAt.Location(), want)
	}
	if !info.LastActiveAt.Equal(want) || info.LastActiveAt.Location() != time.UTC {
		t.Fatalf("last_active_at = %v (%v), want %v UTC", info.LastActiveAt, info.LastActiveAt.Location(), want)
	}
}

func TestSessionMetadataWithoutOwnedTimeIsUnlistedButDirectLoadFails(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260729T120000-00000001")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, conversationFile), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, metadataFile), []byte(
		`{"format_version":1,"session_id":"20260729T120000-00000001","kind":"primary"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := LoadInfo(dir); !errors.Is(err, ErrSessionTimeUnavailable) {
		t.Fatalf("LoadInfo error = %v, want ErrSessionTimeUnavailable", err)
	}
	infos, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("List = %+v, want session without owned time omitted", infos)
	}
}

func TestListWithHistoryScansDiskOnlyAndOmitsStaleHistory(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(t.TempDir(), "history.json")
	mtime := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	id := "20260727T120000-diskonly"
	makeSession(t, root, id,
		[]llm.Message{llm.TextMessage(llm.RoleUser, "disk only")},
		mtime)
	if err := RecordSession(historyPath, Info{
		ID:           "20260727T110000-stale001",
		Dir:          "/outside/old/session",
		Kind:         KindPrimary,
		StartedAt:    mtime.Add(-time.Hour),
		LastActiveAt: mtime.Add(-time.Hour),
		Turns:        10,
		Preview:      "stale",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := ListWithHistory(root, historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != id || got[0].Preview != "disk only" {
		t.Fatalf("sessions = %+v, want only disk session %s", got, id)
	}
	history, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Sessions) != 2 {
		t.Fatalf("history sessions = %+v, want repaired disk session plus unrelated cache entry", history.Sessions)
	}
}

func TestListWithHistoryMatchesStrictListWithStaleRecordedUsage(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(t.TempDir(), "history.json")
	mtime := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	id := "20260727T120000-exact001"
	dir := makeSession(t, root, id,
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "hello"),
			llm.TextMessage(llm.RoleAssistant, "world"),
		},
		mtime)
	writeEvents(t, dir, []events.Event{
		{
			Type: "llm.responded",
			Payload: map[string]any{
				"token_usage":   llm.Usage{InputTokens: 10, OutputTokens: 2},
				"context_usage": llm.ContextUsage{Model: "mock", TotalTokens: 12},
			},
		},
		{
			Type: "llm.responded",
			Payload: map[string]any{
				"token_usage": llm.Usage{InputTokens: 20, OutputTokens: 4},
			},
		},
	})
	strict, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(strict) != 1 {
		t.Fatalf("strict sessions = %+v", strict)
	}
	stale := strict[0]
	stale.TokenUsage = llm.Usage{InputTokens: 1}
	stale.ContextUsage = nil
	if err := RecordSession(historyPath, stale); err != nil {
		t.Fatal(err)
	}

	cached, err := ListWithHistory(root, historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cached, strict) {
		t.Fatalf("cached = %+v, want strict %+v", cached, strict)
	}
}

func TestListWithHistoryReturnsMetadataErrorOnCacheHit(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(t.TempDir(), "history.json")
	mtime := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	id := "20260727T120000-badmeta1"
	dir := makeSession(t, root, id,
		[]llm.Message{llm.TextMessage(llm.RoleUser, "hello")},
		mtime)
	if err := RecordSession(historyPath, withTranscriptFingerprint(t, Info{
		ID:           id,
		Dir:          dir,
		LastActiveAt: mtime,
		Turns:        1,
		Preview:      "hello",
	}, dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, metadataFile), []byte("not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ListWithHistory(root, historyPath); err == nil {
		t.Fatal("ListWithHistory accepted malformed session metadata")
	}
}

func TestList_ExtractsTurnsAndPreview(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-abcd1234",
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "summarise README.md"),
			llm.TextMessage(llm.RoleAssistant, "the readme says hello world"),
			compactTestMessage("old context summary"),
			llm.TextMessage(llm.RoleUser, "follow up"),
			llm.TextMessage(llm.RoleAssistant, "done"),
		}, time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))
	writeEvents(t, dir, []events.Event{{
		Type: "llm.responded",
		Payload: map[string]any{
			"token_usage":   llm.Usage{InputTokens: 17, OutputTokens: 7},
			"context_usage": llm.ContextUsage{Model: "mock", InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
		},
	}})

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Turns != 2 {
		t.Errorf("turns = %d, want 2 (user messages)", got[0].Turns)
	}
	if got[0].Preview != "summarise README.md" {
		t.Errorf("preview = %q", got[0].Preview)
	}
	if got[0].Dir != dir {
		t.Errorf("dir = %s, want %s", got[0].Dir, dir)
	}
	if got[0].TokenUsage != (llm.Usage{InputTokens: 17, OutputTokens: 7}) {
		t.Errorf("token_usage = %+v", got[0].TokenUsage)
	}
	if got[0].ContextUsage == nil || got[0].ContextUsage.TotalTokens != 10 {
		t.Fatalf("context_usage = %+v, want latest total 10", got[0].ContextUsage)
	}
	want := time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC)
	if !got[0].StartedAt.Equal(want) {
		t.Errorf("started_at = %v, want %v", got[0].StartedAt, want)
	}
}

func compactTestMessage(text string) llm.Message {
	msg := llm.TextMessage(llm.RoleUser, text)
	msg.Kind = llm.MessageKindCompact
	return msg
}

func TestList_TruncatesPreviewToRunes(t *testing.T) {
	root := t.TempDir()
	long := ""
	for i := 0; i < 100; i++ {
		long += "中"
	}
	makeSession(t, root, "20260506T103500-aa000001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, long)},
		time.Now())

	got, _ := List(root)
	if r := []rune(got[0].Preview); len(r) != 80 {
		t.Fatalf("preview rune count = %d, want 80; got %q", len(r), got[0].Preview)
	}
}

func TestList_SkipsDirsWithoutConversationJSONL(t *testing.T) {
	root := t.TempDir()
	makeSession(t, root, "20260506T103500-good00001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "ok")}, time.Now())
	if err := os.MkdirAll(filepath.Join(root, "20260506T100000-empty0001"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1; got %+v", len(got), got)
	}
}

func TestListRejectsMessageWithoutID(t *testing.T) {
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
	path := filepath.Join(dir, conversationFile)
	line := mustTranscriptLine(t, filepath.Base(dir), 1, llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{}})
	if err := os.WriteFile(path, line, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := List(root)
	if err == nil {
		t.Fatal("List accepted a message without an id")
	}
	for _, want := range []string{path, ":1", "manually add a unique non-empty \"id\"", "internal/session/transcript_repair.go"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("List error = %q, want %q", err, want)
		}
	}
}

func TestList_ReturnsEmptyWhenRootMissing(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestLoadInfo_ReturnsFullMessages(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-load0001",
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "u1"),
			llm.TextMessage(llm.RoleAssistant, "a1"),
		}, time.Now())

	info, msgs, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "20260506T103500-load0001" {
		t.Errorf("id = %s", info.ID)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d", len(msgs))
	}
	if msgs[0].FirstText() != "u1" || msgs[1].FirstText() != "a1" {
		t.Errorf("messages mismatch: %+v", msgs)
	}
}

func TestLoadInfo_NormalizesNullBlocks(t *testing.T) {
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
	line := mustTranscriptLine(t, filepath.Base(dir), 1, llm.Message{ID: "m1", Role: llm.RoleAssistant})
	if err := os.WriteFile(filepath.Join(dir, conversationFile), line, 0o644); err != nil {
		t.Fatal(err)
	}

	_, msgs, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	if msgs[0].Blocks == nil {
		t.Fatal("blocks is nil, want empty slice")
	}
}

func TestLoadInfo_PreservesStoredIDsAndCompactionMetadata(t *testing.T) {
	root := t.TempDir()
	user := llm.TextMessage(llm.RoleUser, "recorded")
	user.ID = "m1"
	compact := compactTestMessage("old context summary")
	compact.ID = "m2"
	compact.Compaction = &llm.CompactionMetadata{
		Auto:               true,
		Reason:             "auto",
		TailStartMessageID: "m2",
		TokensBefore:       100,
		TokensAfter:        40,
		SummaryChars:       12,
		SummaryModel:       "mock",
		RetainedInputReferences: []llm.Message{{
			ID:   "input-ref",
			Role: llm.RoleUser,
			Kind: llm.MessageKindDirect,
			Blocks: []llm.Block{{Type: llm.BlockText, Text: "bounded preview", Artifact: &llm.ContextArtifactProjection{
				SourceKind: "user_input",
				StoredPath: "sessions/session/user-inputs/input-ref-0.txt",
				SHA256:     "input-sha",
			}}},
		}},
	}
	dir := makeSession(t, root, "20260515T010203-meta0001",
		[]llm.Message{user, compact},
		time.Now())

	_, msgs, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].ID != "m1" || msgs[1].ID != "m2" {
		t.Fatalf("message IDs = %q, %q", msgs[0].ID, msgs[1].ID)
	}
	if msgs[1].Compaction == nil || msgs[1].Compaction.TokensBefore != 100 || msgs[1].Compaction.TailStartMessageID != "m2" {
		t.Fatalf("compaction metadata = %+v", msgs[1].Compaction)
	}
	refs := msgs[1].Compaction.RetainedInputReferences
	if len(refs) != 1 || refs[0].ID != "input-ref" || refs[0].Blocks[0].Artifact == nil || refs[0].Blocks[0].Artifact.StoredPath != "sessions/session/user-inputs/input-ref-0.txt" {
		t.Fatalf("retained input references = %+v", refs)
	}
}

func TestLoadInfo_NotFound(t *testing.T) {
	_, _, err := LoadInfo(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("expected error")
	}
}
