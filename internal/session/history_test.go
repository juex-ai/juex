package session

import (
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

func TestRecordHistoryUpsertsAndSetsLast(t *testing.T) {
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
	if h.Last == nil || h.Last.ID != first.ID || h.Last.Alias != "daily" {
		t.Fatalf("last = %+v, want first with alias", h.Last)
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
	if h.Last == nil || h.Last.ID != older.ID {
		t.Fatalf("last = %+v, want older", h.Last)
	}
}

func TestDeleteLastHistoryEntryClearsLast(t *testing.T) {
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
	if len(h.Sessions) != 0 || h.Last != nil {
		t.Fatalf("history = %+v, want empty sessions and nil last", h)
	}
}
