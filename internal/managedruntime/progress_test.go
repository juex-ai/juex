package managedruntime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestProgressCollectorBoundsUnicodeAndKeepsProviderBlockIdentities(t *testing.T) {
	p := &progressCollector{}
	p.add(llm.StreamDelta{Kind: "reasoning", Index: 0, Text: "先分析"})
	p.add(llm.StreamDelta{Kind: "text", Index: 0, Text: "回答"})
	p.add(llm.StreamDelta{Kind: "reasoning", Index: 0, Text: "，再检查"})
	value, seen := p.read()
	if !seen || len(value.Blocks) != 2 || value.Blocks[0].Text != "先分析，再检查" || value.Blocks[1].Text != "回答" {
		t.Fatal(value)
	}
	p.add(llm.StreamDelta{Kind: "text", Index: 0, Text: strings.Repeat("文", MaxProgressBytes)})
	value, _ = p.read()
	if !value.Truncated || value.Validate() != nil {
		t.Fatal("unbounded preview", value.Revision)
	}
	for _, block := range value.Blocks {
		if !utf8.ValidString(block.Text) {
			t.Fatal("preview split a rune")
		}
	}
	revision := value.Revision
	for range 10000 {
		p.add(llm.StreamDelta{Kind: "text", Text: "ignored"})
	}
	value, _ = p.read()
	if value.Revision != revision {
		t.Fatal("truncated preview kept scheduling writes")
	}
}

type slowProgressStore struct {
	entered   chan struct{}
	release   chan struct{}
	mu        sync.Mutex
	snapshots []ProgressSnapshot
}

func (s *slowProgressStore) WriteProgress(ctx context.Context, _ Lease, _ string, value ProgressSnapshot) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = append(s.snapshots, value)
	return nil
}

func TestProgressFlushBatchesDeltasAndDoesNotBlockOnDatabase(t *testing.T) {
	store := &slowProgressStore{entered: make(chan struct{}, 1), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	onDelta, finish := streamProgress(ctx, store, Lease{}, "attempt")
	onDelta(llm.StreamDelta{Kind: "text", Text: "first"})
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no periodic preview")
	}
	added := make(chan struct{})
	go func() {
		for range 10000 {
			onDelta(llm.StreamDelta{Kind: "text", Text: "a"})
		}
		close(added)
	}()
	select {
	case <-added:
	case <-time.After(time.Second):
		t.Fatal("database blocked provider callback")
	}
	close(store.release)
	if !finish() {
		t.Fatal("streamed output forgotten")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.snapshots) > 3 || store.snapshots[len(store.snapshots)-1].Blocks[0].Text != "first"+strings.Repeat("a", 10000) {
		t.Fatal("unbatched writes or lost final preview", len(store.snapshots))
	}
}
