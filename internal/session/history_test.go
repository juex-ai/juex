package session

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

func TestSetAliasAndLoadInfo(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-alias001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "hi")},
		time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))

	if err := SetAlias(dir, "daily"); err != nil {
		t.Fatal(err)
	}
	info, _, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Alias != "daily" {
		t.Fatalf("alias = %q, want daily", info.Alias)
	}
}

func TestRecordHistoryConcurrentKeepsAllSessions(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(root, "history.json")
	const total = 12

	var wg sync.WaitGroup
	errCh := make(chan error, total)
	for i := 0; i < total; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := time.Date(2026, 5, 6, 10, 0, i, 0, time.UTC).Format(idTimeLayout) + "-parallel"
			errCh <- RecordHistory(historyPath, Info{
				ID:           id,
				Alias:        "parallel",
				Dir:          filepath.Join(root, id),
				StartedAt:    time.Date(2026, 5, 6, 10, 0, i, 0, time.UTC),
				LastActiveAt: time.Date(2026, 5, 6, 10, 0, i, 0, time.UTC),
			})
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	h, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Sessions) != total {
		t.Fatalf("sessions len = %d, want %d: %+v", len(h.Sessions), total, h.Sessions)
	}
}

func TestWithHistoryLockRemovesStaleLock(t *testing.T) {
	oldTimeout := historyLockTimeout
	oldStaleAfter := historyLockStaleAfter
	oldPoll := historyLockPoll
	historyLockTimeout = 500 * time.Millisecond
	historyLockStaleAfter = 20 * time.Millisecond
	historyLockPoll = 5 * time.Millisecond
	t.Cleanup(func() {
		historyLockTimeout = oldTimeout
		historyLockStaleAfter = oldStaleAfter
		historyLockPoll = oldPoll
	})

	root := t.TempDir()
	historyPath := filepath.Join(root, "history.json")
	lockPath := historyPath + ".lock"
	if err := os.WriteFile(lockPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	staleTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockPath, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	var called bool
	if err := withHistoryLock(historyPath, func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("lock callback was not called")
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file stat after callback = %v, want not exist", err)
	}
}

func TestAtomicWriteFileOverwritesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := atomicWriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("file content = %q, want new", got)
	}
}

func TestRecordHistoryUpsertsAndSetsActive(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(root, "history.json")
	firstDir := makeSession(t, root, "20260506T103500-first001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "first")},
		time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))
	secondDir := makeSession(t, root, "20260506T113500-second01",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "second")},
		time.Date(2026, 5, 6, 11, 35, 0, 0, time.UTC))
	if err := SetAlias(firstDir, "daily"); err != nil {
		t.Fatal(err)
	}
	if err := SetAlias(secondDir, "daily"); err != nil {
		t.Fatal(err)
	}

	first, _, err := LoadInfo(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := LoadInfo(secondDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordHistory(historyPath, first); err != nil {
		t.Fatal(err)
	}
	if err := RecordHistory(historyPath, second); err != nil {
		t.Fatal(err)
	}
	if err := RecordHistory(historyPath, first); err != nil {
		t.Fatal(err)
	}

	h, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Sessions) != 2 {
		t.Fatalf("sessions len = %d, want 2: %+v", len(h.Sessions), h.Sessions)
	}
	if h.Active == nil || h.Active.ID != first.ID || h.Active.Alias != "daily" {
		t.Fatalf("active = %+v, want first with alias", h.Active)
	}
}

func TestLoadHistoryMigratesLegacyLastToActive(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(root, "history.json")
	body := `{
  "sessions": [
    {
      "id": "20260506T103500-legacy01",
      "dir": "/tmp/20260506T103500-legacy01",
      "started_at": "2026-05-06T10:35:00Z",
      "last_active_at": "2026-05-06T10:35:00Z",
      "turns": 1,
      "preview": "old"
    }
  ],
  "last": {
    "id": "20260506T103500-legacy01",
    "dir": "/tmp/20260506T103500-legacy01",
    "started_at": "2026-05-06T10:35:00Z",
    "last_active_at": "2026-05-06T10:35:00Z",
    "turns": 1,
    "preview": "old"
  }
}` + "\n"
	if err := os.WriteFile(historyPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	h, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != "20260506T103500-legacy01" {
		t.Fatalf("active = %+v, want migrated legacy last", h.Active)
	}
	if h.Active.Kind != KindPrimary {
		t.Fatalf("active kind = %q, want primary", h.Active.Kind)
	}
	if len(h.Sessions) != 1 || h.Sessions[0].Kind != KindPrimary {
		t.Fatalf("sessions = %+v, want default primary kind", h.Sessions)
	}
}

func TestRecordSessionSideDoesNotUpdateActive(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(root, "history.json")
	primary := Info{
		ID:           "20260506T100000-primary1",
		Dir:          filepath.Join(root, "primary"),
		StartedAt:    time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC),
		LastActiveAt: time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC),
		Kind:         KindPrimary,
	}
	side := Info{
		ID:           "20260506T110000-side0001",
		Dir:          filepath.Join(root, "side"),
		StartedAt:    time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC),
		LastActiveAt: time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC),
		Kind:         KindSide,
	}

	if err := SetActive(historyPath, primary); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(historyPath, side); err != nil {
		t.Fatal(err)
	}

	h, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != primary.ID {
		t.Fatalf("active = %+v, want primary", h.Active)
	}
	if len(h.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want primary and side", h.Sessions)
	}
	for _, info := range h.Sessions {
		if info.ID == side.ID && info.Kind != KindSide {
			t.Fatalf("side info = %+v, want side kind", info)
		}
	}
}

func TestActivatePrimarySetsHistoryAndReturnsActive(t *testing.T) {
	root := t.TempDir()
	sessionsRoot := filepath.Join(root, "sessions")
	historyPath := filepath.Join(root, "history.json")
	dir := makeSession(t, sessionsRoot, "20260506T103500-primary1",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "primary")},
		time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))
	if err := SetAlias(dir, "daily"); err != nil {
		t.Fatal(err)
	}

	got, err := Activate(sessionsRoot, historyPath, filepath.Base(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != filepath.Base(dir) || got.Alias != "daily" || got.Kind != KindPrimary || !got.Active {
		t.Fatalf("activated = %+v", got)
	}
	h, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != got.ID || !h.Active.Active {
		t.Fatalf("history active = %+v, want %s active", h.Active, got.ID)
	}
}

func TestActivateRejectsSideSession(t *testing.T) {
	root := t.TempDir()
	sessionsRoot := filepath.Join(root, "sessions")
	historyPath := filepath.Join(root, "history.json")
	dir := makeSession(t, sessionsRoot, "20260506T103500-side0001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "side")},
		time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))
	if err := SetKind(dir, KindSide); err != nil {
		t.Fatal(err)
	}

	_, err := Activate(sessionsRoot, historyPath, filepath.Base(dir))
	if !errors.Is(err, ErrCannotActivateSide) {
		t.Fatalf("err = %v, want ErrCannotActivateSide", err)
	}
	h, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if h.Active != nil {
		t.Fatalf("active = %+v, want nil", h.Active)
	}
}

func TestActivateMissingSession(t *testing.T) {
	root := t.TempDir()
	_, err := Activate(filepath.Join(root, "sessions"), filepath.Join(root, "history.json"), "missing")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestMarkActiveCopiesInfosAndDefaultsKind(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(root, "history.json")
	primary := Info{ID: "primary", Dir: filepath.Join(root, "primary")}
	side := Info{ID: "side", Dir: filepath.Join(root, "side"), Kind: KindSide}
	if err := SetActive(historyPath, primary); err != nil {
		t.Fatal(err)
	}

	input := []Info{primary, side}
	got, err := MarkActive(historyPath, input)
	if err != nil {
		t.Fatal(err)
	}
	if input[0].Kind != "" || input[0].Active {
		t.Fatalf("input mutated = %+v", input[0])
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Kind != KindPrimary || !got[0].Active {
		t.Fatalf("primary = %+v, want default primary active", got[0])
	}
	if got[1].Kind != KindSide || got[1].Active {
		t.Fatalf("side = %+v, want side inactive", got[1])
	}
}

func TestDeleteRemovesDirectoryAndHistoryEntry(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(root, "history.json")
	sessionsRoot := filepath.Join(root, "sessions")
	olderTime := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	newerTime := time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC)
	olderDir := makeSession(t, sessionsRoot, "20260506T100000-old00001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "old")}, olderTime)
	newerDir := makeSession(t, sessionsRoot, "20260506T110000-new00001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "new")}, newerTime)

	older, _, err := LoadInfo(olderDir)
	if err != nil {
		t.Fatal(err)
	}
	newer, _, err := LoadInfo(newerDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordHistory(historyPath, older); err != nil {
		t.Fatal(err)
	}
	if err := RecordHistory(historyPath, newer); err != nil {
		t.Fatal(err)
	}

	if err := Delete(sessionsRoot, historyPath, newer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newerDir); !os.IsNotExist(err) {
		t.Fatalf("deleted dir stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(olderDir); err != nil {
		t.Fatalf("older dir should remain: %v", err)
	}

	h, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Sessions) != 1 || h.Sessions[0].ID != older.ID {
		t.Fatalf("history sessions = %+v, want only older", h.Sessions)
	}
	if h.Active == nil || h.Active.ID != older.ID {
		t.Fatalf("active = %+v, want older", h.Active)
	}
}

func TestDeleteActiveFallsBackToNewestPrimary(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(root, "history.json")
	sessionsRoot := filepath.Join(root, "sessions")
	oldPrimaryDir := makeSession(t, sessionsRoot, "20260506T100000-oldpri01",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "old primary")},
		time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC))
	newPrimaryDir := makeSession(t, sessionsRoot, "20260506T110000-newpri01",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "new primary")},
		time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC))
	sideDir := makeSession(t, sessionsRoot, "20260506T120000-side0001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "side")},
		time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC))
	if err := SetKind(sideDir, KindSide); err != nil {
		t.Fatal(err)
	}
	oldPrimary, _, err := LoadInfo(oldPrimaryDir)
	if err != nil {
		t.Fatal(err)
	}
	newPrimary, _, err := LoadInfo(newPrimaryDir)
	if err != nil {
		t.Fatal(err)
	}
	side, _, err := LoadInfo(sideDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(historyPath, oldPrimary); err != nil {
		t.Fatal(err)
	}
	if err := SetActive(historyPath, newPrimary); err != nil {
		t.Fatal(err)
	}
	if err := RecordSession(historyPath, side); err != nil {
		t.Fatal(err)
	}

	if err := Delete(sessionsRoot, historyPath, newPrimary.ID); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != oldPrimary.ID {
		t.Fatalf("active = %+v, want old primary fallback", h.Active)
	}
}

func TestDeleteLastHistoryEntryClearsActive(t *testing.T) {
	root := t.TempDir()
	historyPath := filepath.Join(root, "history.json")
	sessionsRoot := filepath.Join(root, "sessions")
	dir := makeSession(t, sessionsRoot, "20260506T100000-only0001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "only")}, time.Now())
	info, _, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordHistory(historyPath, info); err != nil {
		t.Fatal(err)
	}
	if err := Delete(sessionsRoot, historyPath, info.ID); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHistory(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Sessions) != 0 || h.Active != nil {
		t.Fatalf("history = %+v, want empty sessions and nil active", h)
	}
}

func TestDeleteRemovesScratchpadContents(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID
	dir := s.Dir
	if err := os.WriteFile(filepath.Join(s.ScratchpadDir(), "draft.md"), []byte("temporary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Delete(root, "", id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("session dir stat err = %v, want not exist", err)
	}
}
