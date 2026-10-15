package runtime

import (
	"errors"
	"sync"
)

var ErrMaintenance = errors.New("runtime: admission is reserved for maintenance")

// ReserveIdleMaintenance atomically excludes new input before durable
// acceptance. A successful reservation remains held until release or process
// exit; checking a status snapshot before shutdown cannot provide this fence.
func (e *Engine) ReserveIdleMaintenance() (func(), error) {
	if e == nil || !e.pendingLifecycleMu.TryLock() {
		return nil, ErrActiveTurnExists
	}
	defer e.pendingLifecycleMu.Unlock()
	if e.maintenanceReserved {
		return nil, ErrMaintenance
	}
	if !e.mu.TryLock() {
		return nil, ErrActiveTurnExists
	}
	defer e.mu.Unlock()
	status := e.PendingInputStatus()
	if status.TurnID != "" || status.PendingCount != 0 {
		return nil, ErrActiveTurnExists
	}
	if _, _, publishing := e.pendingEventPublicationStatus(); publishing {
		return nil, ErrActiveTurnExists
	}
	if queue := e.currentPendingInputQueue(); queue != nil {
		records, err := queue.Records()
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			switch record.State {
			case PendingInputStateAccepting, PendingInputStatePending, PendingInputStateAdmitted, PendingInputStateRetryable:
				return nil, ErrActiveTurnExists
			}
		}
	}
	e.maintenanceReserved = true
	var once sync.Once
	return func() {
		once.Do(func() { e.pendingLifecycleMu.Lock(); e.maintenanceReserved = false; e.pendingLifecycleMu.Unlock() })
	}, nil
}

// HasStoredUnsettledInput checks a dormant Thread without loading, reconciling,
// or rewriting its input state. Callers must first fence live admission.
func HasStoredUnsettledInput(dir string) (bool, error) {
	queue := NewPendingInputQueue(dir, PendingInputQueueOptions{})
	records, _, err := queue.loadDocumentLocked()
	if err != nil {
		return false, err
	}
	for _, record := range records {
		switch record.State {
		case PendingInputStateAccepting, PendingInputStatePending, PendingInputStateAdmitted, PendingInputStateProcessed, PendingInputStateRetryable:
			return true, nil
		}
	}
	return false, nil
}
