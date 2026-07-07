package observable

import (
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
