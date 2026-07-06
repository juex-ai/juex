package observable_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/observable"
)

var fixedTime = time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)

func fixedNow() time.Time {
	return fixedTime
}

func TestStore_AppendAndLoadLatestRuns(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	if err := store.AppendRun(observable.RunRecord{
		ObservableID: "lark-events",
		RunID:        "run-1",
		State:        observable.RunStateStarting,
		StartedAt:    fixedTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRun(observable.RunRecord{
		ObservableID: "lark-events",
		RunID:        "run-1",
		State:        observable.RunStateRunning,
		PID:          42,
		StartedAt:    fixedTime,
	}); err != nil {
		t.Fatal(err)
	}
	runs, err := store.LatestRuns()
	if err != nil {
		t.Fatal(err)
	}
	got := runs["lark-events"]
	if got.State != observable.RunStateRunning || got.PID != 42 {
		t.Fatalf("latest run = %+v", got)
	}
}

func TestStore_RecordObservationDeduplicatesStableID(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	rec := observable.ObservationRecord{
		ObservableID: "lark-events",
		RunID:        "run-1",
		Kind:         "lark_notification",
		Severity:     "info",
		WindowStart:  fixedTime,
		WindowEnd:    fixedTime.Add(10 * time.Second),
		Content:      "hello",
	}
	first, err := store.RecordObservation(rec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RecordObservation(rec)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("ids differ: %q %q", first.ID, second.ID)
	}
	observations, err := store.ListObservations(observable.ObservationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations len = %d, want 1", len(observations))
	}
}

func TestStore_UpdateAndListObservations(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	first, err := store.RecordObservation(observation("lark-events", "first", fixedTime))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RecordObservation(observation("test-watch", "second", fixedTime.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateObservation(first.ID, func(rec observable.ObservationRecord) observable.ObservationRecord {
		rec.State = observable.ObservationStateQueued
		rec.PendingInputID = "observation-" + rec.ID
		return rec
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListObservations(observable.ObservationFilter{ObservableID: "lark-events", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != first.ID || got[0].State != observable.ObservationStateQueued {
		t.Fatalf("filtered observations = %+v", got)
	}
	latest, err := store.ListObservations(observable.ObservationFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].ID != second.ID {
		t.Fatalf("latest observations = %+v, want second first", latest)
	}
}

func TestStore_UpdateUnknownObservation(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	err := store.UpdateObservation("missing", func(rec observable.ObservationRecord) observable.ObservationRecord {
		return rec
	})
	if !errors.Is(err, observable.ErrObservationNotFound) {
		t.Fatalf("err = %v, want ErrObservationNotFound", err)
	}
}

func TestStore_ArtifactPath(t *testing.T) {
	root := t.TempDir()
	store := observable.NewStore(root, observable.StoreOptions{Now: fixedNow})
	got := store.ArtifactPath("lark-events", "obs-123")
	want := filepath.Join(root, "artifacts", "lark-events", "obs-123.txt")
	if got != want {
		t.Fatalf("ArtifactPath() = %q, want %q", got, want)
	}
}

func observation(id, content string, ts time.Time) observable.ObservationRecord {
	return observable.ObservationRecord{
		ObservableID: id,
		RunID:        "run-1",
		Kind:         "log_batch",
		Severity:     "info",
		WindowStart:  ts,
		WindowEnd:    ts.Add(10 * time.Second),
		Content:      content,
	}
}
