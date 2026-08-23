package fleet

import (
	"fmt"

	"github.com/juex-ai/juex/internal/endpoint"
)

type processIdentityState uint8

const (
	processIdentityUnknown processIdentityState = iota
	processIdentityMatched
	processIdentityReplaced
)

type recordedProcessInspection struct {
	Alive          bool
	ExistenceKnown bool
	Identity       processIdentityState
	Err            error
}

func (m *Manager) inspectRecordedProcess(runtimeState endpoint.Runtime) recordedProcessInspection {
	alive, err := m.deps.processAlive(runtimeState.PID)
	if err != nil {
		return recordedProcessInspection{Err: fmt.Errorf("check process %d: %w", runtimeState.PID, err)}
	}
	result := recordedProcessInspection{Alive: alive, ExistenceKnown: true}
	if !alive || runtimeState.ProcessIdentity == "" {
		return result
	}
	identity, err := m.deps.processIdentity(runtimeState.PID)
	if err != nil {
		result.Err = fmt.Errorf("check process %d identity: %w", runtimeState.PID, err)
		return result
	}
	if identity == runtimeState.ProcessIdentity {
		result.Identity = processIdentityMatched
	} else {
		result.Identity = processIdentityReplaced
	}
	return result
}
