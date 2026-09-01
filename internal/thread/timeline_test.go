package thread

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestTimelineLatestPageDoesNotScanHistoricalPrefix(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := main.Append(llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.ThreadsDir(), MainID, journalFile)
	file, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{'!'}, 0); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	page, err := LoadTimelinePage(filepath.Join(store.ThreadsDir(), MainID), "", 1)
	if err != nil {
		t.Fatalf("latest page scanned corrupt historical prefix: %v", err)
	}
	assertPageText(t, page, "message-099")
	if _, err := Load(filepath.Join(store.ThreadsDir(), MainID)); err == nil {
		t.Fatal("full replay accepted corrupt historical prefix")
	}
}

func TestTimelinePagesFromEOFPreservingChronologicalOrder(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	store.now = fixedNow()
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	for i := 1; i <= 5; i++ {
		if err := main.Append(llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	first, err := LoadTimelinePage(main.Dir, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPageText(t, first, "message-4", "message-5")
	if !first.HasMoreBefore || first.PreviousCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := LoadTimelinePage(main.Dir, first.PreviousCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPageText(t, second, "message-2", "message-3")
	third, err := LoadTimelinePage(main.Dir, second.PreviousCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPageText(t, third, "message-1")
	if third.HasMoreBefore {
		t.Fatalf("third page unexpectedly has more: %#v", third)
	}
}

func assertPageText(t *testing.T, page TimelinePage, want ...string) {
	t.Helper()
	if len(page.Items) != len(want) {
		t.Fatalf("items = %#v, want %v", page.Items, want)
	}
	for i, text := range want {
		if page.Items[i].Message == nil || page.Items[i].Message.FirstText() != text {
			t.Fatalf("item %d = %#v, want %q", i, page.Items[i], text)
		}
	}
}
