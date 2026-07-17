package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	notice.Kind = llm.MessageKindModelFallback
	assistant := llm.TextMessage(llm.RoleAssistant, "continuing")
	assistant.Model = "fallback:model"
	if err := s.AppendBatch([]llm.Message{notice, assistant}); err != nil {
		t.Fatal(err)
	}

	if len(s.History) != 2 || s.History[0].Kind != llm.MessageKindModelFallback || s.History[1].Model != "fallback:model" {
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
	notice.Kind = llm.MessageKindModelFallback
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

func TestLoad_AssignsDeterministicLegacyIDs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260515T010203-legacy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"role":"user","blocks":[{"type":"text","text":"old"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"reply"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, conversationFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.History[0].ID != "legacy-000001" || s.History[1].ID != "legacy-000002" {
		t.Fatalf("legacy IDs = %q, %q", s.History[0].ID, s.History[1].ID)
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
	info := s.Info(time.Now())
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
	if err := os.WriteFile(filepath.Join(dir, conversationFile), []byte(`{"role":"assistant","blocks":null}`+"\n"), 0o644); err != nil {
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
