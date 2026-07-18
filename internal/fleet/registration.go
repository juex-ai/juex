package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/agentstate"
)

func (m *Manager) Add(ctx context.Context, opts AddOptions) (AddResult, error) {
	if strings.TrimSpace(opts.Workspace) == "" {
		return AddResult{}, &ValidationError{Reason: "workspace is required"}
	}
	if !filepath.IsAbs(opts.Workspace) {
		return AddResult{}, &ValidationError{Reason: "workspace must be an absolute path"}
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(opts.Workspace)
	if err != nil {
		return AddResult{}, &ValidationError{
			Reason: fmt.Sprintf("workspace path is invalid: %v", err),
		}
	}
	info, err := os.Stat(canonicalWorkspace)
	if err != nil {
		return AddResult{}, &ValidationError{Reason: fmt.Sprintf("workspace must be an existing directory: %v", err)}
	}
	if !info.IsDir() {
		return AddResult{}, &ValidationError{Reason: "workspace must be an existing directory"}
	}
	opts.Workspace = canonicalWorkspace

	var name *string
	if opts.Name != nil {
		trimmed := strings.TrimSpace(*opts.Name)
		if trimmed == "" {
			return AddResult{}, &ValidationError{Reason: "name must not be empty"}
		}
		name = &trimmed
	}
	resolved, err := m.deps.resolveAgent(agentstate.Options{
		HomeDir: m.homeDir,
		WorkDir: opts.Workspace,
	})
	if err != nil {
		return AddResult{}, registrationError(err)
	}

	guard, err := acquireLifecycleLock(m.homeDir, resolved.Agent.ID)
	if err != nil {
		return AddResult{}, err
	}
	defer func() { _ = guard.Close() }()

	if name != nil || opts.Autostart != nil {
		if _, err := m.deps.updateAgent(m.homeDir, resolved.Agent.ID, agentstate.AgentUpdate{
			Name:      name,
			Autostart: opts.Autostart,
		}); err != nil {
			return AddResult{}, err
		}
	}
	entry, err := m.reload(resolved.Agent.ID)
	if err != nil {
		return AddResult{}, err
	}
	var status AgentStatus
	if opts.Start {
		status, err = m.startEntry(ctx, entry)
	} else {
		status = m.inspectStatus(ctx, entry)
	}
	if err != nil {
		return AddResult{}, err
	}
	return AddResult{Agent: status, Created: resolved.Created}, nil
}

func (m *Manager) SetEnabled(
	ctx context.Context,
	selector string,
	enabled bool,
) (AgentStatus, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return AgentStatus{}, err
	}
	guard, err := acquireLifecycleLock(m.homeDir, entry.ID)
	if err != nil {
		return AgentStatus{}, err
	}
	defer func() { _ = guard.Close() }()

	entry, err = m.reload(entry.ID)
	if err != nil {
		return AgentStatus{}, err
	}
	if !enabled {
		if _, err := m.stopEntry(ctx, entry); err != nil {
			return AgentStatus{}, err
		}
	}
	if _, err := m.deps.updateAgent(
		m.homeDir,
		entry.ID,
		agentstate.AgentUpdate{Enabled: &enabled},
	); err != nil {
		return AgentStatus{}, err
	}
	entry, err = m.reload(entry.ID)
	if err != nil {
		return AgentStatus{}, err
	}
	return m.inspectStatus(ctx, entry), nil
}

func (m *Manager) Remove(
	ctx context.Context,
	selector string,
	opts RemoveOptions,
) (RemovedAgent, error) {
	entry, err := m.resolve(selector)
	if err != nil {
		return RemovedAgent{}, err
	}
	guard, err := acquireLifecycleLock(m.homeDir, entry.ID)
	if err != nil {
		return RemovedAgent{}, err
	}
	defer func() { _ = guard.Close() }()

	entry, err = m.reload(entry.ID)
	if err != nil {
		return RemovedAgent{}, err
	}
	confirmationTarget := entry.Agent.Name
	if strings.TrimSpace(confirmationTarget) == "" {
		confirmationTarget = entry.ID
	}
	if !opts.SkipConfirmation && opts.ConfirmName != confirmationTarget {
		return RemovedAgent{}, &ValidationError{
			Reason: fmt.Sprintf("confirmation must exactly match %q", confirmationTarget),
		}
	}
	if _, err := m.stopEntry(ctx, entry); err != nil {
		return RemovedAgent{}, err
	}
	maintenance, err := m.deps.acquireMaintenance(entry.Dir)
	if err != nil {
		return RemovedAgent{}, &ConflictError{
			AgentID: entry.ID,
			Reason:  fmt.Sprintf("endpoint is running or changing: %v", err),
		}
	}
	defer func() { _ = maintenance.Close() }()

	if err := m.deps.deleteRegistered(m.homeDir, entry.ID); err != nil {
		return RemovedAgent{}, err
	}
	return RemovedAgent{
		ID:        entry.ID,
		Name:      entry.Agent.Name,
		Workspace: entry.Agent.Workspace,
	}, nil
}

func registrationError(err error) error {
	var (
		unknown *agentstate.UnknownAgentError
		copied  *agentstate.WorkspaceCopyError
	)
	if errors.As(err, &unknown) || errors.As(err, &copied) {
		return &ConflictError{Reason: err.Error()}
	}
	return err
}
