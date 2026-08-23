package fleet

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
)

func (m *Manager) GCCandidates(ctx context.Context) ([]GCCandidate, error) {
	entries, err := m.deps.listRegistry(m.homeDir)
	if err != nil {
		return nil, err
	}
	candidates := make([]GCCandidate, 0)
	for _, entry := range entries {
		binding := m.deps.inspectBinding(entry)
		if binding.Kind != agentstate.WorkspaceOrphaned {
			continue
		}
		size, lastActivity, err := treeActivity(entry.Address.StateDir())
		if err != nil {
			return nil, fmt.Errorf("fleet: inspect orphan %q: %w", entry.ID, err)
		}
		status := m.inspectStatus(ctx, entry)
		candidates = append(candidates, GCCandidate{
			AgentID:      entry.ID,
			Workspace:    entry.Agent.Workspace,
			SizeBytes:    size,
			LastActivity: lastActivity,
			Running:      status.RuntimeHealth == RuntimeHealthy || status.RuntimeHealth == RuntimeAmbiguous,
			Reason:       binding.Reason,
		})
	}
	return candidates, nil
}

func (m *Manager) DeleteOrphans(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := m.deleteOrphan(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) deleteOrphan(ctx context.Context, id string) error {
	entry, err := m.resolve(id)
	if err != nil {
		return err
	}
	if entry.ID != id {
		return &ConflictError{AgentID: entry.ID, Reason: "garbage collection requires an exact agent id"}
	}
	lifecycle, err := acquireLifecycleLock(m.store(), entry.ID)
	if err != nil {
		return err
	}
	defer func() { _ = lifecycle.Close() }()
	entry, err = m.reload(entry.ID)
	if err != nil {
		return err
	}
	binding := m.deps.inspectBinding(entry)
	if binding.Kind != agentstate.WorkspaceOrphaned {
		return &ConflictError{
			AgentID: entry.ID,
			Reason:  fmt.Sprintf("workspace binding is %s: %s", binding.Kind, binding.Reason),
		}
	}
	maintenance, err := m.deps.acquireMaintenance(entry.Address)
	if err != nil {
		return &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("endpoint is running or changing: %v", err)}
	}
	defer func() { _ = maintenance.Close() }()

	runtimeState, err := m.deps.readRuntime(entry.Address)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return &ConflictError{AgentID: entry.ID, Reason: fmt.Sprintf("runtime metadata is malformed: %v", err)}
	default:
		if err := m.cleanStaleRuntimeUnderMaintenance(ctx, entry, runtimeState); err != nil {
			return err
		}
	}
	if err := agentstate.DeleteOrphan(m.homeDir, entry.ID); err != nil {
		return err
	}
	return nil
}

func treeActivity(root string) (int64, time.Time, error) {
	var size int64
	var latest time.Time
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		if !entry.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, latest, err
}
