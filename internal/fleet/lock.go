package fleet

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/homestore"
)

func acquireLifecycleLock(store homestore.Store, agentID string) (maintenanceGuard, error) {
	if agentID == "" || filepath.Base(agentID) != agentID ||
		strings.ContainsAny(agentID, `/\`) || strings.HasPrefix(agentID, ".") {
		return nil, &ConflictError{AgentID: agentID, Reason: "invalid registry identity cannot be locked"}
	}
	guard, err := store.Lock(homestore.FleetLocks, agentID, homestore.LockTry)
	if errors.Is(err, homestore.ErrLockBusy) {
		return nil, &ConflictError{AgentID: agentID, Reason: "another lifecycle operation is in progress"}
	}
	if err != nil {
		return nil, fmt.Errorf("fleet: lock agent %q lifecycle: %w", agentID, err)
	}
	return guard, nil
}

func acquireSupervisorLock(homeDir string) (maintenanceGuard, error) {
	guard, err := homestore.AcquireLock(filepath.Join(homeDir, "fleet.lock"), homestore.LockTry)
	if errors.Is(err, homestore.ErrLockBusy) {
		return nil, &ConflictError{Reason: "another fleet supervisor is already running for this home"}
	}
	if err != nil {
		return nil, fmt.Errorf("fleet: lock supervisor: %w", err)
	}
	return guard, nil
}
