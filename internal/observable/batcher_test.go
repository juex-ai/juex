package observable_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/observable"
)

func TestBatcher_FlushesAfterInterval(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	b := observable.NewBatcher(validSpec("logs"), store, observable.BatcherOptions{})
	first := parsedUnit("stdout", "first", fixedTime)
	if got, err := b.Add(first); err != nil || len(got) != 0 {
		t.Fatalf("first Add() = %+v, %v; want no flush", got, err)
	}
	second := parsedUnit("stdout", "second", fixedTime.Add(10*time.Second))
	got, err := b.Add(second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Content, "first") {
		t.Fatalf("flushed = %+v, want first batch", got)
	}
	remaining, err := b.Flush("shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || !strings.Contains(remaining[0].Content, "second") {
		t.Fatalf("remaining = %+v, want second batch", remaining)
	}
}

func TestBatcher_EmptyFlushDoesNothing(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	b := observable.NewBatcher(validSpec("logs"), store, observable.BatcherOptions{})
	got, err := b.Flush("shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Flush() = %+v, want empty", got)
	}
}

func TestBatcher_FlushDueFlushesQuietBatch(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	b := observable.NewBatcher(validSpec("logs"), store, observable.BatcherOptions{})
	if got, err := b.Add(parsedUnit("stdout", "quiet", fixedTime)); err != nil || len(got) != 0 {
		t.Fatalf("Add() = %+v, %v; want no immediate flush", got, err)
	}
	early, err := b.FlushDue(fixedTime.Add(time.Second), "interval")
	if err != nil {
		t.Fatal(err)
	}
	if len(early) != 0 {
		t.Fatalf("early FlushDue() = %+v, want empty", early)
	}
	got, err := b.FlushDue(fixedTime.Add(11*time.Second), "interval")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "quiet" {
		t.Fatalf("FlushDue() = %+v, want quiet record", got)
	}
}

func TestBatcher_UsesHighestSeverityInWindow(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	b := observable.NewBatcher(validSpec("logs"), store, observable.BatcherOptions{})
	if _, err := b.Add(parsedUnit("stdout", "info", fixedTime)); err != nil {
		t.Fatal(err)
	}
	errUnit := parsedUnit("stderr", "error", fixedTime.Add(time.Second))
	errUnit.Severity = "error"
	if _, err := b.Add(errUnit); err != nil {
		t.Fatal(err)
	}
	got, err := b.Flush("shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Severity != "error" {
		t.Fatalf("Flush() = %+v, want severity error", got)
	}
}

func TestBatcher_WritesArtifactWhenContentExceedsMaxChars(t *testing.T) {
	spec := validSpec("large")
	spec.Batch.MaxChars = 80
	root := t.TempDir()
	store := observable.NewStore(root, observable.StoreOptions{Now: fixedNow})
	b := observable.NewBatcher(spec, store, observable.BatcherOptions{})
	_, err := b.Add(parsedUnit("stdout", strings.Repeat("x", 120), fixedTime))
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Flush("shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("records = %+v, want 1", got)
	}
	rec := got[0]
	if !rec.Truncated || rec.OriginalChars != 120 || rec.ArtifactPath == "" || len([]rune(rec.Content)) > spec.Batch.MaxChars {
		t.Fatalf("record = %+v, want truncated artifact preview", rec)
	}
	body, err := os.ReadFile(rec.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != strings.Repeat("x", 120) {
		t.Fatalf("artifact body len = %d, want full content", len(body))
	}
}

func parsedUnit(stream, content string, receivedAt time.Time) observable.ParsedUnit {
	return observable.ParsedUnit{
		Stream:     stream,
		Content:    content,
		Kind:       "log_batch",
		Severity:   "info",
		ReceivedAt: receivedAt,
	}
}
