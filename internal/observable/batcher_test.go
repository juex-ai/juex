package observable_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/eventmedia"
	"github.com/juex-ai/juex/internal/observable"
)

func TestBatcher_FlushesAfterInterval(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	b := newBatcher(t, validSpec("logs"), store, observable.BatcherOptions{})
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
	b := newBatcher(t, validSpec("logs"), store, observable.BatcherOptions{})
	got, err := b.Flush("shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Flush() = %+v, want empty", got)
	}
}

func TestNewBatcherRejectsScheduleSpec(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	spec := scheduleOnceSpec("schedule-batcher", fixedTime.Add(time.Hour))
	batcher, err := observable.NewBatcher(spec, store, observable.BatcherOptions{})
	if err == nil || !strings.Contains(err.Error(), "command source") {
		t.Fatalf("NewBatcher() = %#v, %v; want command source error", batcher, err)
	}
}

func TestBatcher_FlushDueFlushesQuietBatch(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	b := newBatcher(t, validSpec("logs"), store, observable.BatcherOptions{})
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
	b := newBatcher(t, validSpec("logs"), store, observable.BatcherOptions{})
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

func TestBatcher_PersistsAttachmentErrors(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	b := newBatcher(t, validSpec("logs"), store, observable.BatcherOptions{})
	unit := parsedUnit("stdout", "keep me", fixedTime)
	unit.AttachmentErrors = []string{"attachments must be an array"}
	if _, err := b.Add(unit); err != nil {
		t.Fatal(err)
	}
	got, err := b.Flush("shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AttachmentState != observable.ObservationAttachmentStateError || len(got[0].AttachmentErrors) != 1 {
		t.Fatalf("records = %+v, want persisted attachment error", got)
	}
}

func TestBatcher_SnapshotsAttachmentsBeforeFlush(t *testing.T) {
	workDir := t.TempDir()
	sourcePath := filepath.Join(workDir, ".juex", "inbox", "pixel.png")
	writeBatcherPNG(t, sourcePath)
	store := observable.NewStore(filepath.Join(workDir, ".juex", "observables"), observable.StoreOptions{Now: fixedNow})
	b := newBatcher(t, validSpec("logs"), store, observable.BatcherOptions{WorkDir: workDir})
	unit := parsedUnit("stdout", "image event", fixedTime)
	unit.Attachments = []eventmedia.AttachmentRef{{Path: ".juex/inbox/pixel.png", MediaType: "image/png"}}
	if _, err := b.Add(unit); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}

	records, err := b.Flush("interval")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Attachments) != 1 {
		t.Fatalf("records = %+v, want one stored attachment", records)
	}
	ref := records[0].Attachments[0]
	if !strings.HasPrefix(ref.Path, ".juex/artifacts/event-media/") {
		t.Fatalf("attachment path = %q, want durable event artifact", ref.Path)
	}
	if report := eventmedia.ValidateAttachments(records[0].Attachments, eventmedia.ValidationOptions{WorkDir: workDir}); len(report.Errors) != 0 || len(report.Valid) != 1 {
		t.Fatalf("stored attachment validation = %+v", report)
	}
}

func TestBatcher_EnforcesAttachmentLimitAcrossBatch(t *testing.T) {
	workDir := t.TempDir()
	firstPath := filepath.Join(workDir, "first.txt")
	secondPath := filepath.Join(workDir, "second.txt")
	if err := os.WriteFile(firstPath, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := observable.NewStore(filepath.Join(workDir, ".juex", "observables"), observable.StoreOptions{Now: fixedNow})
	b := newBatcher(t, validSpec("logs"), store, observable.BatcherOptions{
		WorkDir:       workDir,
		MaxEventBytes: 1,
	})
	first := parsedUnit("stdout", "first", fixedTime)
	first.Attachments = []eventmedia.AttachmentRef{{Path: "first.txt", MediaType: "text/plain"}}
	if _, err := b.Add(first); err != nil {
		t.Fatal(err)
	}
	second := parsedUnit("stdout", "second", fixedTime.Add(time.Second))
	second.Attachments = []eventmedia.AttachmentRef{{Path: "second.txt", MediaType: "text/plain"}}
	if _, err := b.Add(second); err != nil {
		t.Fatal(err)
	}

	records, err := b.Flush("interval")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %+v, want one record", records)
	}
	record := records[0]
	if len(record.Attachments) != 1 || !strings.Contains(record.Attachments[0].Path, "event-media") {
		t.Fatalf("attachments = %+v, want first durable attachment only", record.Attachments)
	}
	if record.AttachmentState != observable.ObservationAttachmentStateError || len(record.AttachmentErrors) != 1 {
		t.Fatalf("attachment state/errors = %q, %+v", record.AttachmentState, record.AttachmentErrors)
	}
	if !strings.Contains(record.AttachmentErrors[0], "event attachments exceed 1 bytes") {
		t.Fatalf("attachment errors = %+v", record.AttachmentErrors)
	}
	if !strings.Contains(record.AttachmentErrors[0], "batch remaining: 0 bytes") {
		t.Fatalf("attachment errors = %+v, want remaining budget", record.AttachmentErrors)
	}
}

func TestBatcher_ResetsAttachmentLimitAfterIntervalFlush(t *testing.T) {
	workDir := t.TempDir()
	for _, name := range []string{"first.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store := observable.NewStore(filepath.Join(workDir, ".juex", "observables"), observable.StoreOptions{Now: fixedNow})
	b := newBatcher(t, validSpec("logs"), store, observable.BatcherOptions{
		WorkDir:       workDir,
		MaxEventBytes: 1,
	})
	first := parsedUnit("stdout", "first", fixedTime)
	first.Attachments = []eventmedia.AttachmentRef{{Path: "first.txt", MediaType: "text/plain"}}
	if _, err := b.Add(first); err != nil {
		t.Fatal(err)
	}
	second := parsedUnit("stdout", "second", fixedTime.Add(10*time.Second))
	second.Attachments = []eventmedia.AttachmentRef{{Path: "second.txt", MediaType: "text/plain"}}
	flushed, err := b.Add(second)
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := b.Flush("shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(flushed) != 1 || len(flushed[0].Attachments) != 1 || len(flushed[0].AttachmentErrors) != 0 {
		t.Fatalf("flushed = %+v, want one valid attachment", flushed)
	}
	if len(remaining) != 1 || len(remaining[0].Attachments) != 1 || len(remaining[0].AttachmentErrors) != 0 {
		t.Fatalf("remaining = %+v, want reset attachment budget", remaining)
	}
}

func TestBatcher_SnapshotsNewAttachmentBeforeIntervalFlush(t *testing.T) {
	workDir := t.TempDir()
	spec := validSpec("logs")
	spec = mutateCommandSpec(spec, func(config *observable.CommandSourceSpec) { config.Batch.MaxChars = 1 })
	store := observable.NewStore(filepath.Join(workDir, ".juex", "observables"), observable.StoreOptions{Now: fixedNow})
	b := newBatcher(t, spec, store, observable.BatcherOptions{WorkDir: workDir})
	first := parsedUnit("stdout", "old-batch", fixedTime)
	if _, err := b.Add(first); err != nil {
		t.Fatal(err)
	}
	oldID := observable.BuildObservationID(observable.ObservationRecord{
		ObservableID: "logs",
		Kind:         "log_batch",
		Severity:     "info",
		WindowStart:  fixedTime,
		WindowEnd:    fixedTime,
		Content:      "old-batch",
	})
	temporaryPath := store.ArtifactPath("logs", oldID)
	if err := os.MkdirAll(filepath.Dir(temporaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporaryPath, []byte("temporary attachment"), 0o644); err != nil {
		t.Fatal(err)
	}
	relTemporaryPath, err := filepath.Rel(workDir, temporaryPath)
	if err != nil {
		t.Fatal(err)
	}
	second := parsedUnit("stdout", "new-batch", fixedTime.Add(10*time.Second))
	second.Attachments = []eventmedia.AttachmentRef{{Path: filepath.ToSlash(relTemporaryPath), MediaType: "text/plain"}}
	if _, err := b.Add(second); err != nil {
		t.Fatal(err)
	}
	records, err := b.Flush("shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Attachments) != 1 {
		t.Fatalf("records = %+v, want new batch attachment", records)
	}
	storedPath := filepath.Join(workDir, filepath.FromSlash(records[0].Attachments[0].Path))
	stored, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "temporary attachment" {
		t.Fatalf("stored attachment = %q, want pre-flush source bytes", stored)
	}
}

func TestBatcher_WritesArtifactWhenContentExceedsMaxChars(t *testing.T) {
	spec := validSpec("large")
	spec = mutateCommandSpec(spec, func(config *observable.CommandSourceSpec) { config.Batch.MaxChars = 80 })
	config, _ := spec.CommandConfig()
	root := t.TempDir()
	store := observable.NewStore(root, observable.StoreOptions{Now: fixedNow})
	b := newBatcher(t, spec, store, observable.BatcherOptions{})
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
	if !rec.Truncated || rec.OriginalChars != 120 || rec.ArtifactPath == "" || len([]rune(rec.Content)) > config.Batch.MaxChars {
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

func TestBatcher_KeepsBatchWhenPersistenceFails(t *testing.T) {
	root := t.TempDir()
	stateDir := root + "/state"
	if err := os.WriteFile(stateDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := observable.NewStore(stateDir, observable.StoreOptions{Now: fixedNow})
	b := newBatcher(t, validSpec("logs"), store, observable.BatcherOptions{})
	if _, err := b.Add(parsedUnit("stdout", "retry me", fixedTime)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Flush("first"); err == nil {
		t.Fatal("Flush() err = nil, want persistence failure")
	}
	if err := os.Remove(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := b.Flush("retry")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "retry me" {
		t.Fatalf("retry Flush() = %+v, want retained batch", got)
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

func newBatcher(t *testing.T, spec observable.Spec, store *observable.Store, opts observable.BatcherOptions) *observable.Batcher {
	t.Helper()
	batcher, err := observable.NewBatcher(spec, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	return batcher
}

func writeBatcherPNG(t *testing.T, path string) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
