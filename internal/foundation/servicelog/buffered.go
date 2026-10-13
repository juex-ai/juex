package servicelog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const bufferBytes = 512 << 10
const chunkBytes = 32 << 10

type logChunk struct {
	data []byte
	lost uint64
}

// Buffered isolates service output from ordinary log storage. Overflow is
// counted and discarded; durable business receipts must never use this writer.
type Buffered struct {
	mu      sync.Mutex
	chunks  chan logChunk
	done    chan struct{}
	closed  bool
	bytes   int // Includes the chunk currently being written to the sink.
	pending uint64
	lost    uint64
	err     error // Keep only the first error, even during a prolonged outage.
}

// NewBuffered transfers sink ownership to one worker, including retention and
// closing. Close may stop waiting, but cannot cancel a blocked file syscall.
func NewBuffered(sink interface {
	io.WriteCloser
	Prune() error
}) *Buffered {
	return newBuffered(sink, time.Hour)
}

func newBuffered(sink interface {
	io.WriteCloser
	Prune() error
}, pruneEvery time.Duration) *Buffered {
	b := &Buffered{chunks: make(chan logChunk, 128), done: make(chan struct{})}
	go b.drain(sink, pruneEvery)
	return b
}

func (b *Buffered) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, os.ErrClosed
	}
	n := len(p)
	for len(p) != 0 {
		size := min(len(p), chunkBytes)
		if b.bytes+size <= bufferBytes && len(b.chunks) < cap(b.chunks) {
			data := append([]byte(nil), p[:size]...)
			b.chunks <- logChunk{data: data, lost: b.pending}
			b.bytes += size
			b.pending = 0
		} else {
			// The worker cannot release capacity while this lock is held.
			// Count the rest without copying or walking an oversized write.
			b.pending += uint64(len(p))
			b.lost += uint64(len(p))
			break
		}
		p = p[size:]
	}
	// Returning a sink error would stop exec's pipe reader and SIGPIPE the
	// service. Report loss separately when storage recovers and on Close.
	return n, nil
}

func (b *Buffered) recordError(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		b.err = err
	}
}

func (b *Buffered) drain(sink interface {
	io.WriteCloser
	Prune() error
}, pruneEvery time.Duration) {
	defer close(b.done)
	defer func() { b.recordError(sink.Close()) }()
	ticker := time.NewTicker(pruneEvery)
	defer ticker.Stop()
	var lost uint64
	reportLoss := func() {
		if lost == 0 {
			return
		}
		notice := fmt.Sprintf("\n[juex-service-log: dropped %d ordinary log bytes]\n", lost)
		n, err := sink.Write([]byte(notice))
		if err == nil && n != len(notice) {
			err = io.ErrShortWrite
		}
		b.recordError(err)
		if err == nil {
			lost = 0
		}
	}
	for {
		select {
		case chunk, ok := <-b.chunks:
			if !ok {
				b.mu.Lock()
				lost += b.pending
				b.mu.Unlock()
				reportLoss()
				return
			}
			lost += chunk.lost
			reportLoss()
			n, err := sink.Write(chunk.data)
			n = max(0, min(n, len(chunk.data)))
			if err == nil && n != len(chunk.data) {
				err = io.ErrShortWrite
			}
			b.recordError(err)
			dropped := uint64(len(chunk.data) - n)
			lost += dropped
			b.mu.Lock()
			b.lost += dropped
			b.bytes -= len(chunk.data)
			idle := len(b.chunks) == 0
			if idle {
				// Report overflow after all earlier retained bytes, even when
				// the recovered service produces no further output.
				lost += b.pending
				b.pending = 0
			}
			b.mu.Unlock()
			if idle {
				reportLoss()
			}
		case <-ticker.C:
			err := sink.Prune()
			b.recordError(err)
			if err == nil {
				reportLoss()
			}
		}
	}
}

// Close stops accepting output and waits only as long as ctx permits. Its
// diagnostics describe logs, not the outcome of the supervised service.
func (b *Buffered) Close(ctx context.Context) error {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		close(b.chunks)
	}
	b.mu.Unlock()
	var waitErr error
	select {
	case <-b.done:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var lossErr error
	if b.lost != 0 || b.bytes != 0 {
		lossErr = fmt.Errorf("ordinary logs: %d bytes dropped, %d bytes still pending", b.lost, b.bytes)
	}
	return errors.Join(b.err, waitErr, lossErr)
}
