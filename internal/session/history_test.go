package session

import (
	"path/filepath"
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
