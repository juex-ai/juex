//go:build windows

package e2e

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/homestore"
	"github.com/juex-ai/juex/internal/session"
	"golang.org/x/sys/windows"
)

func TestSessionHistoryBlockedReplacementPreservesActiveSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	old := session.Info{ID: "old", Kind: session.KindPrimary}
	next := session.Info{ID: "next", Kind: session.KindPrimary}
	if err := session.SetActive(path, old); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	rolledBack := false
	replaced, err := session.CompareAndSetActive(path, old.ID, next, func() { rolledBack = true })
	if err == nil || replaced || rolledBack || homestore.ReplacementOccurred(err) {
		t.Fatalf("blocked selection = replaced %t, rollback %t, err %v; want a pre-publication failure", replaced, rolledBack, err)
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("blocked selection error = %v, want a Windows sharing conflict", err)
	}
	history, err := session.LoadHistory(path)
	if err != nil || history.Active == nil || history.Active.ID != old.ID || len(history.Sessions) != 1 {
		t.Fatalf("history after failed selection = %+v, %v; want only the original active Session", history, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	replaced, err = session.CompareAndSetActive(path, old.ID, next, nil)
	if err != nil || !replaced {
		t.Fatalf("selection after reader closed = replaced %t, err %v", replaced, err)
	}
	history, err = session.LoadHistory(path)
	if err != nil || history.Active == nil || history.Active.ID != next.ID {
		t.Fatalf("history after successful selection = %+v, %v; want the new active Session", history, err)
	}
}
