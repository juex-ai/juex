package thread

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
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
	historicalPath := main.CurrentGenerationJournalPath()
	if _, err := main.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if err := main.Append(llm.TextMessage(llm.RoleUser, "current-generation")); err != nil {
		t.Fatal(err)
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(historicalPath, os.O_WRONLY, 0o600)
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

	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatalf("open scanned corrupt historical Generation: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	page, err := reopened.Timeline("", 1)
	if err != nil {
		t.Fatalf("latest page scanned corrupt historical prefix: %v", err)
	}
	assertPageText(t, page, "current-generation")
	if _, err := reopened.Timeline(page.PreviousCursor, 500); err == nil {
		t.Fatal("historical timeline accepted corrupt Generation")
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
	defer func() { _ = main.Close() }()
	for i := 1; i <= 5; i++ {
		if err := main.Append(llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	first, err := main.Timeline("", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPageText(t, first, "message-4", "message-5")
	if !first.HasMoreBefore || first.PreviousCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := main.Timeline(first.PreviousCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPageText(t, second, "message-2", "message-3")
	third, err := main.Timeline(second.PreviousCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPageText(t, third, "message-1")
	if third.HasMoreBefore {
		t.Fatalf("third page unexpectedly has more: %#v", third)
	}
}

func TestTimelinePagesCrossGenerationBoundaryChronologically(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	for _, text := range []string{"message-1", "message-2"} {
		if err := main.Append(llm.TextMessage(llm.RoleUser, text)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := main.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"message-3", "message-4"} {
		if err := main.Append(llm.TextMessage(llm.RoleUser, text)); err != nil {
			t.Fatal(err)
		}
	}

	first, err := main.Timeline("", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPageText(t, first, "message-3", "message-4")
	second, err := main.Timeline(first.PreviousCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := timelineLabels(second); !reflect.DeepEqual(got, []string{"message:message-2", "activity:context.renewed"}) {
		t.Fatalf("boundary page = %v", got)
	}
	third, err := main.Timeline(second.PreviousCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPageText(t, third, "message-1")
	if third.HasMoreBefore || third.PreviousCursor != "" {
		t.Fatalf("final page = %#v", third)
	}
}

func TestTimelineBoundsNonDisplayScanAndResumesFromCursor(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	if err := main.Append(llm.TextMessage(llm.RoleUser, "visible")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"event-1", "event-2", "event-3"} {
		if err := main.AppendEvent(events.Event{ID: id, Type: "test.event"}); err != nil {
			t.Fatal(err)
		}
	}

	main.mu.Lock()
	generations, err := main.eventStore.captureGenerations(main.state.Projection.Generations)
	latest := main.state.Projection.EventCursor
	main.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	first, err := loadTimelinePageWithScanLimit(main.ID, generations, latest, "", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 0 || !first.HasMoreBefore || first.PreviousCursor == "" {
		t.Fatalf("bounded empty page = %#v", first)
	}
	second, err := loadTimelinePageWithScanLimit(main.ID, generations, latest, first.PreviousCursor, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPageText(t, second, "visible")
	third, err := loadTimelinePageWithScanLimit(main.ID, generations, latest, second.PreviousCursor, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Items) != 0 || third.HasMoreBefore || third.PreviousCursor != "" {
		t.Fatalf("terminal non-display page = %#v", third)
	}
}

func TestTimelineRejectsMismatchedCursorIdentityAndSequence(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	if err := main.Append(llm.TextMessage(llm.RoleUser, "one")); err != nil {
		t.Fatal(err)
	}
	if err := main.Append(llm.TextMessage(llm.RoleUser, "two")); err != nil {
		t.Fatal(err)
	}
	page, err := main.Timeline("", 1)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := decodePageCursor(page.PreviousCursor)
	if err != nil {
		t.Fatal(err)
	}

	wrongSequence := cursor
	wrongSequence.BeforeSeq++
	encoded, err := encodePageCursor(wrongSequence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := main.Timeline(encoded, 1); !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("sequence mismatch error = %v, want corrupt Journal", err)
	}

	worker, err := store.CreateWorker(MainID, "cursor-owner")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Close() }()
	if _, err := worker.Timeline(page.PreviousCursor, 1); err == nil || !strings.Contains(err.Error(), "another Thread") {
		t.Fatalf("foreign cursor error = %v", err)
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

func timelineLabels(page TimelinePage) []string {
	labels := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		switch {
		case item.Message != nil:
			labels = append(labels, "message:"+item.Message.FirstText())
		case item.Activity != nil:
			labels = append(labels, "activity:"+item.Activity.Type)
		}
	}
	return labels
}
