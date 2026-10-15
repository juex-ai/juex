package agent

import (
	"sync"

	"github.com/juex-ai/juex/internal/framework/runtime"
)

// ReserveIdleMaintenance reserves this Thread and its managed Worker tree.
// Failed reservations roll back without interrupting work or result delivery.
func (a *Agent) ReserveIdleMaintenance() (func(), error) {
	if a == nil || a.Engine == nil {
		return nil, ErrThreadUnavailable
	}
	if a.pendingRecoveryDone != nil {
		select {
		case <-a.pendingRecoveryDone:
		default:
			return nil, errTurnAdmissionBusy
		}
	}
	if !a.turnAdmission.transitionMu.TryLock() {
		return nil, errTurnAdmissionBusy
	}
	defer a.turnAdmission.transitionMu.Unlock()
	a.turnAdmission.mu.Lock()
	if a.turnAdmission.phase != turnAdmissionIdle {
		a.turnAdmission.mu.Unlock()
		return nil, errTurnAdmissionBusy
	}
	releaseEngine, err := a.Engine.ReserveIdleMaintenance()
	if err != nil {
		a.turnAdmission.mu.Unlock()
		return nil, err
	}
	a.turnAdmission.phase = turnAdmissionMaintenance
	a.turnAdmission.mu.Unlock()
	releaseWorkers, err := a.workers.reserveIdleMaintenance()
	if err != nil {
		releaseEngine()
		a.turnAdmission.mu.Lock()
		a.turnAdmission.phase = turnAdmissionIdle
		a.turnAdmission.mu.Unlock()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseWorkers()
			a.turnAdmission.transitionMu.Lock()
			releaseEngine()
			a.turnAdmission.mu.Lock()
			a.turnAdmission.phase = turnAdmissionIdle
			a.turnAdmission.mu.Unlock()
			a.turnAdmission.transitionMu.Unlock()
		})
	}, nil
}

func (m *WorkerManager) reserveIdleMaintenance() (func(), error) {
	if m == nil {
		return func() {}, nil
	}
	if !m.lifecycleMu.TryLock() {
		return nil, errTurnAdmissionBusy
	}
	m.mu.Lock()
	busy := m.closed || m.transitioning || len(m.reservations) != 0 || len(m.resultHandoffs) != 0
	var children []*Agent
	for _, managed := range m.threads {
		if managed.status.State == WorkerThreadStateRunning || managed.status.State == WorkerThreadStateStopping || managed.resultHandoffs != 0 {
			busy = true
		}
		children = append(children, managed.app)
	}
	if busy {
		m.mu.Unlock()
		m.lifecycleMu.Unlock()
		return nil, errTurnAdmissionBusy
	}
	m.transitioning = true
	m.mu.Unlock()
	m.lifecycleMu.Unlock()
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		m.lifecycleMu.Lock()
		m.mu.Lock()
		if !m.closed {
			m.transitioning = false
		}
		m.mu.Unlock()
		m.lifecycleMu.Unlock()
	}
	for _, child := range children {
		if child == nil {
			release()
			return nil, runtime.ErrActiveTurnExists
		}
		undo, err := child.ReserveIdleMaintenance()
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, undo)
	}
	return release, nil
}
