package managedruntime

import (
	"context"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

const MaxProgressBytes = 64 << 10
const MaxProgressBlocks = 32

type ProgressBlock struct {
	Kind  string `json:"kind"`
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type ProgressSnapshot struct {
	Revision  int64           `json:"revision"`
	Blocks    []ProgressBlock `json:"blocks"`
	Truncated bool            `json:"truncated"`
}

// ModelProgress is a provisional display, never provider replay or tool input.
// Attempts and their final messages share the same ID for atomic replacement.
type ModelProgress struct {
	AttemptID  string           `json:"attempt_id"`
	TurnID     string           `json:"turn_id"`
	Generation int64            `json:"generation"`
	Sequence   int64            `json:"sequence"`
	Model      string           `json:"model"`
	State      string           `json:"state"`
	StartedAt  time.Time        `json:"started_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
	Snapshot   ProgressSnapshot `json:"snapshot"`
}

type ProgressStore interface {
	WriteProgress(context.Context, Lease, string, ProgressSnapshot) error
}

func (s ProgressSnapshot) Validate() error {
	if s.Revision < 1 || len(s.Blocks) > MaxProgressBlocks {
		return ErrInvalid
	}
	total := 0
	for _, block := range s.Blocks {
		if block.Kind != "text" && block.Kind != "reasoning" || block.Index < 0 || !utf8.ValidString(block.Text) {
			return ErrInvalid
		}
		total += len(block.Text)
	}
	if total > MaxProgressBytes {
		return ErrInvalid
	}
	return nil
}

type progressCollector struct {
	mu       sync.Mutex
	snapshot ProgressSnapshot
	bytes    int
	seen     bool
}

func (p *progressCollector) add(delta llm.StreamDelta) {
	if delta.Kind != "text" && delta.Kind != "reasoning" || delta.Text == "" || delta.Index < 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = true
	if p.snapshot.Truncated {
		return
	}
	index := slices.IndexFunc(p.snapshot.Blocks, func(b ProgressBlock) bool { return b.Kind == delta.Kind && b.Index == delta.Index })
	if index < 0 && len(p.snapshot.Blocks) == MaxProgressBlocks {
		p.snapshot.Truncated = true
		p.snapshot.Revision++
		return
	}
	text := delta.Text
	if len(text) > MaxProgressBytes-p.bytes {
		text = text[:MaxProgressBytes-p.bytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		p.snapshot.Truncated = true
	}
	if index < 0 {
		p.snapshot.Blocks = append(p.snapshot.Blocks, ProgressBlock{Kind: delta.Kind, Index: delta.Index, Text: text})
	} else {
		p.snapshot.Blocks[index].Text += text
	}
	p.bytes += len(text)
	p.snapshot.Revision++
}

func (p *progressCollector) read() (ProgressSnapshot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value := p.snapshot
	value.Blocks = slices.Clone(value.Blocks)
	return value, p.seen
}

// One bounded accumulator and one writer replace an unbounded per-token queue.
// A slow database can delay preview refresh, but cannot block provider deltas.
func streamProgress(ctx context.Context, store ProgressStore, lease Lease, attempt string) (func(llm.StreamDelta), func() bool) {
	collector := &progressCollector{}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		var written int64
		flush := func() {
			value, _ := collector.read()
			if store == nil || value.Revision <= written || ctx.Err() != nil {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if store.WriteProgress(writeCtx, lease, attempt, value) == nil {
				written = value.Revision
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				flush()
				return
			case <-ticker.C:
				flush()
			}
		}
	}()
	return collector.add, func() bool { close(stop); <-done; _, seen := collector.read(); return seen }
}
