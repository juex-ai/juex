package fleet

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var errLockBusy = errors.New("fleet lock is already held")

func acquireLifecycleLock(homeDir, agentID string) (maintenanceGuard, error) {
	if agentID == "" || filepath.Base(agentID) != agentID ||
		strings.ContainsAny(agentID, `/\`) || strings.HasPrefix(agentID, ".") {
		return nil, &ConflictError{AgentID: agentID, Reason: "invalid registry identity cannot be locked"}
	}
	path := filepath.Join(homeDir, ".locks", "fleet", agentID+".lock")
	guard, err := acquireLockGuard(path)
	if errors.Is(err, errLockBusy) {
		return nil, &ConflictError{AgentID: agentID, Reason: "another lifecycle operation is in progress"}
	}
	if err != nil {
		return nil, fmt.Errorf("fleet: lock agent %q lifecycle: %w", agentID, err)
	}
	return guard, nil
}

func acquireSupervisorLock(homeDir string) (maintenanceGuard, error) {
	guard, err := acquireLockGuard(filepath.Join(homeDir, "fleet.lock"))
	if errors.Is(err, errLockBusy) {
		return nil, &ConflictError{Reason: "another fleet supervisor is already running for this home"}
	}
	if err != nil {
		return nil, fmt.Errorf("fleet: lock supervisor: %w", err)
	}
	return guard, nil
}
