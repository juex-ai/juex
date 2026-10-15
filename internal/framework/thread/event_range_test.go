package thread

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestEventStoreBoundedRangeFreezesUpperAndPreservesOversizedCursor(t *testing.T) {
	th, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = th.Close() }()
	if err := th.AppendBatch([]llm.Message{llm.TextMessage(llm.RoleUser, "first")}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := th.CaptureEventStore()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	upper := th.Projection().EventCursor.Seq
	if err := th.AppendBatch([]llm.Message{llm.TextMessage(llm.RoleUser, strings.Repeat("large", 2000))}); err != nil {
		t.Fatal(err)
	}
	var cursor EventCursor
	count := 0
	for {
		batch, err := snapshot.ReadRange(context.Background(), cursor, 1, 4096)
		if err != nil {
			t.Fatal(err)
		}
		count += len(batch.Commits)
		cursor = batch.Cursor
		if !batch.More {
			break
		}
	}
	if cursor.Seq != upper || count != int(upper) {
		t.Fatalf("frozen cursor/count: %+v %d", cursor, count)
	}
	newer, err := th.CaptureEventStore()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = newer.Close() }()
	batch, err := newer.ReadRange(context.Background(), cursor, 1, 4096)
	var oversized *OversizedCommitError
	if !errors.As(err, &oversized) || batch.Cursor != cursor || len(batch.Commits) != 0 {
		t.Fatalf("oversized advanced %+v %v", batch, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newer.ReadRange(ctx, cursor, 1, 32768); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
