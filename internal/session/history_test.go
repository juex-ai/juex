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
