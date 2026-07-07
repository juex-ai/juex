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
