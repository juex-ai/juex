package servicelog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gatedSink struct {
	bytes.Buffer
	gate, entered chan struct{}
	once          sync.Once
	block         string
}

func (s *gatedSink) wait(operation string) {
	if s.block == operation {
		s.once.Do(func() { close(s.entered) })
		<-s.gate
	}
}
func (s *gatedSink) Write(p []byte) (int, error) { s.wait("write"); return s.Buffer.Write(p) }
func (s *gatedSink) Close() error                { s.wait("close"); return nil }
func (s *gatedSink) Prune() error                { s.wait("prune"); return nil }

func TestBufferedBoundsInflightAndReportsLossInOrder(t *testing.T) {
	sink := &gatedSink{gate: make(chan struct{}), entered: make(chan struct{}), block: "write"}
	b := NewBuffered(sink)
	t.Cleanup(func() {
		select {
		case <-sink.gate:
		default:
			close(sink.gate)
		}
		_ = b.Close(context.Background())
	})
	first := bytes.Repeat([]byte("a"), chunkBytes)
	_, _ = b.Write(first)
	<-sink.entered
	first[0] = 'z'
	_, _ = b.Write(bytes.Repeat([]byte("b"), bufferBytes))
	_, _ = b.Write(bytes.Repeat([]byte("c"), 4*bufferBytes))
	b.mu.Lock()
	pending := b.bytes
	b.mu.Unlock()
	if pending != bufferBytes {
		t.Fatal("inflight capacity not bounded", pending)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("closed")); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	close(sink.gate)
	if err := b.Close(context.Background()); err == nil || !strings.Contains(err.Error(), "2129920 bytes dropped, 0 bytes still pending") {
		t.Fatal(err)
	}
	want := strings.Repeat("a", chunkBytes) + strings.Repeat("b", bufferBytes-chunkBytes) + "\n[juex-service-log: dropped 2129920 ordinary log bytes]\n"
	if sink.String() != want {
		t.Fatal("retained order, buffer ownership or loss notice changed", len(sink.String()))
	}
}

type partialSink struct {
	bytes.Buffer
	failed  bool
	failure error
}

func (s *partialSink) Write(p []byte) (int, error) {
	if !s.failed {
		s.failed = true
		_, _ = s.Buffer.Write(p[:2])
		return 2, s.failure
	}
	return s.Buffer.Write(p)
}
func (s *partialSink) Prune() error { return nil }
func (s *partialSink) Close() error { return nil }

func TestBufferedPartialFailureDoesNotReplayOrCloseServicePipe(t *testing.T) {
	failure := errors.New("storage failure")
	sink := &partialSink{failure: failure}
	b := NewBuffered(sink)
	for _, data := range []string{"abcdef", "next"} {
		if n, err := b.Write([]byte(data)); n != len(data) || err != nil {
			t.Fatal(n, err)
		}
	}
	if err := b.Close(context.Background()); !errors.Is(err, failure) || !strings.Contains(err.Error(), "4 bytes dropped") {
		t.Fatal(err)
	}
	if got := sink.String(); got != "ab\n[juex-service-log: dropped 4 ordinary log bytes]\nnext" {
		t.Fatal(got)
	}
}

func TestBufferedPruneAndCloseCannotBlockWriterOrExit(t *testing.T) {
	for _, operation := range []string{"prune", "close"} {
		t.Run(operation, func(t *testing.T) {
			sink := &gatedSink{gate: make(chan struct{}), entered: make(chan struct{}), block: operation}
			b := newBuffered(sink, time.Millisecond)
			t.Cleanup(func() {
				select {
				case <-sink.gate:
				default:
					close(sink.gate)
				}
				_ = b.Close(context.Background())
			})
			if operation == "prune" {
				select {
				case <-sink.entered:
				case <-time.After(time.Second):
					t.Fatal("no prune")
				}
				if n, err := b.Write([]byte("still accepting")); n != 15 || err != nil {
					t.Fatal(n, err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if err := b.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			close(sink.gate)
			if err := b.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBufferedConcurrentCloseAndWrites(t *testing.T) {
	sink := &gatedSink{}
	b := NewBuffered(sink)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 100 {
				_, err := b.Write([]byte("x"))
				if err != nil && !errors.Is(err, os.ErrClosed) {
					t.Error(err)
				}
			}
			_ = b.Close(context.Background())
		})
	}
	workers.Wait()
	if _, err := io.WriteString(b, ""); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
}

type recoveredSink struct {
	gatedSink
	notice chan struct{}
}

func (s *recoveredSink) Write(p []byte) (int, error) {
	n, err := s.gatedSink.Write(p)
	if bytes.Contains(p, []byte("ordinary log bytes")) {
		select {
		case s.notice <- struct{}{}:
		default:
		}
	}
	return n, err
}
func TestBufferedReportsLossWhenRecoveredServiceBecomesQuiet(t *testing.T) {
	sink := &recoveredSink{gatedSink: gatedSink{gate: make(chan struct{}), entered: make(chan struct{}), block: "write"}, notice: make(chan struct{}, 1)}
	b := NewBuffered(sink)
	t.Cleanup(func() {
		select {
		case <-sink.gate:
		default:
			close(sink.gate)
		}
		_ = b.Close(context.Background())
	})
	_, _ = b.Write(bytes.Repeat([]byte("a"), chunkBytes))
	<-sink.entered
	_, _ = b.Write(bytes.Repeat([]byte("b"), bufferBytes))
	close(sink.gate)
	select {
	case <-sink.notice:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("recovered idle sink hides dropped log bytes until shutdown")
	}
}

type recoveringErrorSink struct {
	failed atomic.Bool
	errors chan struct{}
	notice chan string
}

func (s *recoveringErrorSink) Write(p []byte) (int, error) {
	if s.failed.Load() {
		select {
		case s.errors <- struct{}{}:
		default:
		}
		return 0, errors.New("storage unavailable")
	}
	select {
	case s.notice <- string(p):
	default:
	}
	return len(p), nil
}
func (s *recoveringErrorSink) Prune() error { return nil }
func (s *recoveringErrorSink) Close() error { return nil }

func TestBufferedRetriesFailedLossNoticeDuringIdleMaintenance(t *testing.T) {
	sink := &recoveringErrorSink{errors: make(chan struct{}, 8), notice: make(chan string, 8)}
	sink.failed.Store(true)
	b := newBuffered(sink, 5*time.Millisecond)
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	_, _ = b.Write([]byte("abcdef"))
	for range 2 {
		select {
		case <-sink.errors:
		case <-time.After(time.Second):
			t.Fatal("did not exercise failed data and loss notice")
		}
	}
	sink.failed.Store(false)
	select {
	case notice := <-sink.notice:
		if notice != "\n[juex-service-log: dropped 6 ordinary log bytes]\n" {
			t.Fatal(notice)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("maintenance never reports recovered storage loss")
	}
}
