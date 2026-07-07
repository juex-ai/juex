package observable

import (
	"context"
	"testing"
	"time"
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
	defer close(releaseDelivery)
	mgr := &Manager{
		store: NewStore(t.TempDir(), StoreOptions{Now: func() time.Time { return now }}),
		opts: ManagerOptions{
			Deliver: func(ctx context.Context, record ObservationRecord) error {
				close(deliveryStarted)
				<-releaseDelivery
				return nil
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
	done := make(chan error, 1)
	go func() {
		_, _, err := mgr.emitScheduledOccurrence(context.Background(), run, occurrence, now)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("emitScheduledOccurrence blocked on delivery")
	}
	select {
	case <-deliveryStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled observation was not delivered")
	}
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
			Deliver: func(ctx context.Context, record ObservationRecord) error {
				delivered <- record
				return store.UpdateObservation(record.ID, func(record ObservationRecord) ObservationRecord {
					record.State = ObservationStateQueued
					return record
				})
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
