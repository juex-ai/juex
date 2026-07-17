package observable

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

type blockingRunOnceSource struct {
	*fakeSourceRuntime
	entered chan struct{}
	release chan struct{}
}

func (s *blockingRunOnceSource) runOnce(ctx context.Context) (ObservationRecord, error) {
	close(s.entered)
	select {
	case <-s.release:
		return ObservationRecord{
			ID:            "manual-record",
			ObservableID:  "manual-delete-race",
			SourceEventID: "schedule:manual-delete-race:manual:fixed",
			State:         ObservationStateRecorded,
			WindowStart:   time.Now().UTC(),
			WindowEnd:     time.Now().UTC(),
			Kind:          DefaultScheduleKind,
			Severity:      DefaultSeverity,
			Content:       "manual",
			OriginalChars: len("manual"),
		}, nil
	case <-ctx.Done():
		return ObservationRecord{}, ctx.Err()
	}
}

func TestManagerRunOnceSerializesWithDelete(t *testing.T) {
	spec := mustScheduleSpec("manual-delete-race", ScheduleSourceSpec{
		Interval:    &IntervalSchedule{EverySeconds: 3600},
		Observation: ScheduleObservationSpec{Content: "manual"},
	})
	deleteEntered := make(chan struct{})
	source := &blockingRunOnceSource{
		fakeSourceRuntime: &fakeSourceRuntime{deleteFn: func(context.Context, string) error {
			close(deleteEntered)
			return nil
		}},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	mgr := newSourceTestManager(t, spec, source)

	runDone := make(chan error, 1)
	go func() {
		_, err := mgr.RunOnce(context.Background(), spec.ID)
		runDone <- err
	}()
	<-source.entered
	if mgr.mu.TryLock() {
		mgr.mu.Unlock()
		t.Fatal("RunOnce released the manager lock before the source completed")
	}

	deleteStarted := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		close(deleteStarted)
		deleteDone <- mgr.Delete(context.Background(), spec.ID)
	}()
	<-deleteStarted
	select {
	case <-deleteEntered:
		t.Fatal("Delete interleaved with RunOnce")
	case <-time.After(50 * time.Millisecond):
	}

	close(source.release)
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-deleteEntered:
	case <-time.After(time.Second):
		t.Fatal("Delete did not proceed after RunOnce released the lock")
	}
	if err := <-deleteDone; err != nil {
		t.Fatal(err)
	}
}

func TestManagerRunOnceRejectsDeletingSchedule(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".juex", "observables.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := mustScheduleSpec("manual-deleting", ScheduleSourceSpec{
		Interval:    &IntervalSchedule{EverySeconds: 3600},
		Observation: ScheduleObservationSpec{Content: "Run once."},
	})
	if err := SaveConfig(configPath, FileConfig{Observables: []Spec{spec}}); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(ManagerOptions{
		ConfigPath: configPath,
		StateDir:   filepath.Join(dir, ".juex", "observables"),
		WorkDir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	mgr.deleting[spec.ID] = true
	if _, err := mgr.RunOnce(context.Background(), spec.ID); !errors.Is(err, ErrObservableDeleting) {
		t.Fatalf("deleting error = %v, want ErrObservableDeleting", err)
	}
}

func TestScheduleManualSourceEventIDKeepsRecoveryPrefix(t *testing.T) {
	sourceEventID, err := scheduleManualSourceEventID("daily-brief")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sourceEventID, scheduleSourceEventPrefix("daily-brief")+"manual:") {
		t.Fatalf("manual source event id = %q", sourceEventID)
	}
}

func TestStopScheduleTimerDoesNotBlockWhenStopReturnsFalseWithoutValue(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("initial Stop() = false, want true")
	}
	done := make(chan struct{})
	go func() {
		stopScheduleTimer(timer)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stopScheduleTimer blocked when Stop returned false without a value to drain")
	}
}

func TestEmitScheduledOccurrenceDoesNotBlockOnDelivery(t *testing.T) {
	now := time.Now().UTC()
	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(releaseDelivery)
		}
	})
	store := NewStore(t.TempDir(), StoreOptions{Now: func() time.Time { return now }})
	mgr := &Manager{
		store: store,
		opts: ManagerOptions{
			Deliver: func(ctx context.Context, record ObservationRecord) (DeliveryOutcome, error) {
				close(deliveryStarted)
				<-releaseDelivery
				return DeliveryOutcome{State: ObservationStateDelivered}, nil
			},
		},
	}
	spec := mustScheduleSpec("interval-delivery", ScheduleSourceSpec{
		Interval:    &IntervalSchedule{EverySeconds: 60},
		Observation: ScheduleObservationSpec{Kind: "heartbeat", Severity: "info", Content: "check"},
	})
	run := &observableRun{id: spec.ID, runID: "run-1", spec: spec}
	scheduleSpec, _ := spec.scheduleRuntime()
	source := &scheduleSourceRuntime{spec: scheduleSpec, kernel: mgr, store: store}
	occurrence := ScheduledOccurrence{
		ObservableID:  spec.ID,
		ScheduledAt:   now,
		SourceEventID: scheduleSourceEventID(spec.ID, now),
	}
	type emitResult struct {
		record ObservationRecord
		err    error
	}
	done := make(chan emitResult, 1)
	go func() {
		record, _, err := source.emitOccurrence(context.Background(), run, occurrence, now)
		done <- emitResult{record: record, err: err}
	}()
	var result emitResult
	select {
	case result = <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("emitScheduledOccurrence blocked on delivery")
	}
	select {
	case <-deliveryStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled observation was not delivered")
	}
	close(releaseDelivery)
	released = true
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		updated, ok, err := store.Observation(result.record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ok && updated.State == ObservationStateDelivered {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scheduled observation delivery transition did not finish")
}

func TestEvaluateScheduleStartupRecoversRecordedScheduleObservation(t *testing.T) {
	now := time.Now().UTC()
	scheduledAt := now.Add(-time.Minute)
	store := NewStore(t.TempDir(), StoreOptions{Now: func() time.Time { return now }})
	spec := mustScheduleSpec("interval-recovery", ScheduleSourceSpec{
		Interval:    &IntervalSchedule{EverySeconds: 60},
		Observation: ScheduleObservationSpec{Kind: "heartbeat", Severity: "info", Content: "check"},
	})
	if err := store.RecordScheduleState(ScheduleStateRecord{
		ObservableID:           spec.ID,
		LastEvaluatedAt:        now,
		LastEmittedScheduledAt: scheduledAt,
		UpdatedAt:              now,
	}); err != nil {
		t.Fatal(err)
	}
	record, err := store.RecordObservation(ObservationRecord{
		ObservableID:  spec.ID,
		RunID:         "run-1",
		SourceEventID: scheduleSourceEventID(spec.ID, scheduledAt),
		Kind:          "heartbeat",
		Severity:      "info",
		WindowStart:   scheduledAt,
		WindowEnd:     scheduledAt,
		Content:       "check",
		State:         ObservationStateRecorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan ObservationRecord, 1)
	mgr := &Manager{
		store: store,
		opts: ManagerOptions{
			Now: func() time.Time { return now },
			Deliver: func(ctx context.Context, record ObservationRecord) (DeliveryOutcome, error) {
				delivered <- record
				return DeliveryOutcome{State: ObservationStateQueued, PendingInputID: "pending-" + record.ID}, nil
			},
		},
	}
	run := &observableRun{id: spec.ID, runID: "run-2", spec: spec}
	scheduleSpec, _ := spec.scheduleRuntime()
	source := &scheduleSourceRuntime{spec: scheduleSpec, kernel: mgr, store: store}
	if err := source.evaluateStartup(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-delivered:
		if got.ID != record.ID {
			t.Fatalf("delivered observation id = %q, want %q", got.ID, record.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("recorded schedule observation was not recovered")
	}
	deadline := time.Now().Add(time.Second)
	for {
		records, err := store.ListObservations(ObservationFilter{ObservableID: spec.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 1 && records[0].State == ObservationStateQueued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered observations = %+v, want one queued record", records)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDeliverObservationOwnsOutcomeTransitionAndSkipsTransitionedRecord(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore(t.TempDir(), StoreOptions{Now: func() time.Time { return now }})
	record, err := store.RecordObservation(ObservationRecord{
		ObservableID: "lifecycle",
		RunID:        "run-1",
		Kind:         "notice",
		Severity:     "info",
		WindowStart:  now,
		WindowEnd:    now,
		Content:      "hello",
		State:        ObservationStateRecorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	var seen []string
	bus.Subscribe("*", func(e events.Event) {
		seen = append(seen, e.Type)
	})
	deliveries := 0
	mgr := &Manager{
		store: store,
		opts: ManagerOptions{
			Bus: bus,
			Now: func() time.Time { return now },
			Deliver: func(ctx context.Context, record ObservationRecord) (DeliveryOutcome, error) {
				deliveries++
				return DeliveryOutcome{State: ObservationStateDelivered, TargetSession: "sess-1"}, nil
			},
		},
	}
	if err := mgr.deliverObservation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	updated, ok, err := store.Observation(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || updated.State != ObservationStateDelivered || updated.TargetSession != "sess-1" || updated.DeliveredAt.IsZero() {
		t.Fatalf("updated observation = %+v ok=%v", updated, ok)
	}
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
	if len(seen) != 2 || seen[0] != EventObservationRecorded || seen[1] != EventObservationDelivered {
		t.Fatalf("events = %+v, want recorded then delivered", seen)
	}

	if err := mgr.deliverObservation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatalf("deliveries after transitioned record = %d, want still 1", deliveries)
	}
}

func TestDeliverObservationAppliesOutcomeWithoutStore(t *testing.T) {
	now := time.Now().UTC()
	bus := events.NewBus()
	var seen []ObservationEventPayload
	bus.Subscribe("observation.*", func(e events.Event) {
		payload, ok := e.Payload.(ObservationEventPayload)
		if !ok {
			t.Fatalf("payload = %T, want ObservationEventPayload", e.Payload)
		}
		seen = append(seen, payload)
	})
	mgr := &Manager{
		opts: ManagerOptions{
			Bus: bus,
			Now: func() time.Time { return now },
			Deliver: func(ctx context.Context, record ObservationRecord) (DeliveryOutcome, error) {
				return DeliveryOutcome{State: ObservationStateDelivered, TargetSession: "sess-1"}, nil
			},
		},
	}
	record := ObservationRecord{
		ID:           "obs-1",
		ObservableID: "no-store",
		RunID:        "run-1",
		Kind:         "notice",
		Severity:     "info",
		WindowStart:  now,
		WindowEnd:    now,
		Content:      "hello",
		State:        ObservationStateRecorded,
	}
	if err := mgr.deliverObservation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("events = %+v, want recorded then delivered", seen)
	}
	delivered := seen[1].Observation
	if delivered.ID != record.ID || delivered.State != ObservationStateDelivered || delivered.TargetSession != "sess-1" || !delivered.DeliveredAt.Equal(now) {
		t.Fatalf("delivered observation = %+v", delivered)
	}
}

func mustScheduleSpec(id string, config ScheduleSourceSpec) Spec {
	spec, err := NewScheduleSpec(id, "", config)
	if err != nil {
		panic(err)
	}
	return spec
}
