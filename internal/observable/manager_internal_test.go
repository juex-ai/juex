package observable

import (
	"context"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

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
	spec := Spec{
		ID:          "interval-delivery",
		Observation: ObservationSpec{Kind: "heartbeat", Severity: "info", Content: "check"},
	}
	run := &observableRun{id: spec.ID, runID: "run-1", spec: spec}
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
		record, _, err := mgr.emitScheduledOccurrence(context.Background(), run, occurrence, now)
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
	spec := Spec{
		ID: "interval-recovery",
		Source: SourceSpec{
			Type:     SourceTypeSchedule,
			Interval: &IntervalSchedule{EverySeconds: 60},
		},
		Observation: ObservationSpec{Kind: "heartbeat", Severity: "info", Content: "check"},
	}
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
	if err := mgr.evaluateScheduleStartup(context.Background(), run); err != nil {
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
	records, err := store.ListObservations(ObservationFilter{ObservableID: spec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].State != ObservationStateQueued {
		t.Fatalf("recovered observations = %+v, want one queued record", records)
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
