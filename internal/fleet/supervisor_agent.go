package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/foundation/homestore"
	"github.com/juex-ai/juex/internal/framework/agentstate"
)

type supervisorBinding struct {
	State      string              `json:"state"`
	Agent      agentstate.Agent    `json:"agent"`
	Retained   []string            `json:"retained_agent_ids,omitempty"`
	Settlement *ExecutorSettlement `json:"executor_settlement,omitempty"`
}

// ExecutorSettlement reports the business service's acknowledgement separately
// from the process lifecycle result. Fleet owns neither its queue nor its data.
type ExecutorSettlement struct {
	AgentID   string `json:"agent_id"`
	Confirmed bool   `json:"confirmed"`
	Released  int    `json:"released"`
	Reason    string `json:"reason,omitempty"`
}

type SupervisorStatus struct {
	State              string              `json:"state"`
	Agent              AgentStatus         `json:"agent"`
	Retained           []string            `json:"retained_agent_ids,omitempty"`
	HistoryDisposition string              `json:"history_disposition"`
	Settlement         *ExecutorSettlement `json:"executor_settlement,omitempty"`
}

func (m *Manager) supervisorPath() string {
	return filepath.Join(m.homeDir, "fleet", "supervisor.json")
}
func (m *Manager) supervisorLock() (*homestore.Lock, error) {
	return m.store().Lock(homestore.FleetLocks, "supervisor-role", homestore.LockWait)
}
func (m *Manager) readSupervisor() (supervisorBinding, error) {
	var binding supervisorBinding
	data, err := os.ReadFile(m.supervisorPath())
	if err != nil {
		return binding, err
	}
	if err := json.Unmarshal(data, &binding); err != nil {
		return binding, err
	}
	if binding.State != "initializing" && binding.State != "ready" && binding.State != "removed" {
		return binding, errors.New("fleet: invalid Supervisor binding state")
	}
	if _, err := agentstate.NewAgentAddress(m.homeDir, binding.Agent.ID); err != nil {
		return binding, err
	}
	return binding, nil
}
func (m *Manager) writeSupervisor(binding supervisorBinding) error {
	data, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return err
	}
	return homestore.WriteFileAtomic(m.supervisorPath(), append(data, '\n'), 0600, 0700)
}

func (m *Manager) Supervisor(ctx context.Context) (SupervisorStatus, error) {
	binding, err := m.readSupervisor()
	if errors.Is(err, os.ErrNotExist) {
		return SupervisorStatus{State: "uninitialized", HistoryDisposition: "preserved"}, nil
	}
	if err != nil {
		return SupervisorStatus{}, err
	}
	return m.supervisorStatus(ctx, binding)
}

func (m *Manager) supervisorStatus(ctx context.Context, binding supervisorBinding) (SupervisorStatus, error) {
	status := SupervisorStatus{State: binding.State, Retained: binding.Retained, HistoryDisposition: "preserved", Settlement: binding.Settlement}
	if binding.State == "removed" {
		return status, nil
	}
	entry, err := m.reload(binding.Agent.ID)
	if err != nil {
		status.State = "repair_required"
		return status, &ConflictError{AgentID: binding.Agent.ID, Reason: fmt.Sprintf("bound Supervisor requires explicit repair: %v", err)}
	}
	if inspected := agentstate.InspectBinding(entry); inspected.Kind != agentstate.WorkspaceBound {
		status.State = "repair_required"
		return status, &ConflictError{AgentID: binding.Agent.ID, Reason: "bound Supervisor requires explicit repair: " + inspected.Reason}
	}
	if entry.Agent.Workspace != binding.Agent.Workspace {
		status.State = "repair_required"
		return status, &ConflictError{AgentID: binding.Agent.ID, Reason: "Supervisor workspace differs from its role binding; requires explicit repair"}
	}
	status.Agent = m.inspectStatus(ctx, entry)
	return status, nil
}

// EnsureSupervisor initializes resources without running inference or starting
// a process. Fleet's normal startup policy handles the registered Agent later.
func (m *Manager) EnsureSupervisor(ctx context.Context, enabled bool) (SupervisorStatus, error) {
	guard, err := m.supervisorLock()
	if err != nil {
		return SupervisorStatus{}, err
	}
	defer func() { _ = guard.Close() }()
	binding, err := m.readSupervisor()
	if errors.Is(err, os.ErrNotExist) {
		if !enabled {
			return SupervisorStatus{State: "disabled", HistoryDisposition: "preserved"}, nil
		}
		binding, err = m.planSupervisor(nil)
	}
	if err != nil {
		return SupervisorStatus{}, err
	}
	if binding.State == "initializing" {
		if err := m.finishSupervisorInitialization(&binding); err != nil {
			return SupervisorStatus{}, err
		}
	}
	if !enabled && binding.State == "ready" {
		if _, err := m.SetEnabled(ctx, binding.Agent.ID, false); err != nil {
			return SupervisorStatus{}, err
		}
		if err := m.settleSupervisorWork(ctx, &binding); err != nil {
			return SupervisorStatus{}, err
		}
	}
	return m.supervisorStatus(ctx, binding)
}

func (m *Manager) planSupervisor(retained []string) (supervisorBinding, error) {
	root := filepath.Join(m.homeDir, "workspaces")
	if err := os.MkdirAll(root, 0700); err != nil {
		return supervisorBinding{}, err
	}
	workspace, err := os.MkdirTemp(root, "supervisor-")
	if err != nil {
		return supervisorBinding{}, err
	}
	identity, err := agentstate.PlanIdentity(agentstate.Options{HomeDir: m.homeDir, WorkDir: workspace})
	if err != nil {
		return supervisorBinding{}, err
	}
	identity.Name, identity.Autostart = "Supervisor", true
	binding := supervisorBinding{State: "initializing", Agent: identity, Retained: retained}
	return binding, m.writeSupervisor(binding)
}

func (m *Manager) finishSupervisorInitialization(binding *supervisorBinding) error {
	resolution, err := agentstate.RestoreIdentity(m.homeDir, binding.Agent)
	if err != nil {
		return err
	}
	var config, guidance []byte
	if m.supervisorTemplate != nil {
		config, guidance = m.supervisorTemplate()
	}
	if len(config) == 0 {
		config = []byte("{}\n")
	}
	for path, body := range map[string][]byte{resolution.Address.ConfigPath(): config, filepath.Join(binding.Agent.Workspace, ".agents", "AGENTS.md"): guidance} {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if err := homestore.WriteFileAtomic(path, body, 0600, 0700); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	binding.State = "ready"
	return m.writeSupervisor(*binding)
}

func (m *Manager) RepairSupervisor(ctx context.Context) (SupervisorStatus, error) {
	guard, err := m.supervisorLock()
	if err != nil {
		return SupervisorStatus{}, err
	}
	defer func() { _ = guard.Close() }()
	binding, err := m.readSupervisor()
	if err != nil {
		return SupervisorStatus{}, err
	}
	if binding.State == "removed" {
		return SupervisorStatus{}, &ConflictError{Reason: "Supervisor was explicitly removed; use reset"}
	}
	lifecycle, err := acquireLifecycleLock(m.store(), binding.Agent.ID)
	if err != nil {
		return SupervisorStatus{}, err
	}
	defer func() { _ = lifecycle.Close() }()
	address, err := agentstate.NewAgentAddress(m.homeDir, binding.Agent.ID)
	if err != nil {
		return SupervisorStatus{}, err
	}
	maintenance, err := homestore.AcquireLock(address.EndpointLockPath(), homestore.LockTry)
	if err != nil {
		return SupervisorStatus{}, err
	}
	defer func() { _ = maintenance.Close() }()
	if err := os.MkdirAll(binding.Agent.Workspace, 0700); err != nil {
		return SupervisorStatus{}, err
	}
	// Missing metadata is repaired stopped; missing history cannot be recovered
	// from the role record and must never be reported as restored.
	binding.Agent.Autostart = false
	binding.Agent.Enabled = false
	if err := m.finishSupervisorInitialization(&binding); err != nil {
		return SupervisorStatus{}, err
	}
	return m.supervisorStatus(ctx, binding)
}

func (m *Manager) ResetSupervisor(ctx context.Context) (SupervisorStatus, error) {
	return m.retireSupervisor(ctx, true)
}
func (m *Manager) RemoveSupervisor(ctx context.Context) (SupervisorStatus, error) {
	return m.retireSupervisor(ctx, false)
}
func (m *Manager) retireSupervisor(ctx context.Context, reset bool) (SupervisorStatus, error) {
	guard, err := m.supervisorLock()
	if err != nil {
		return SupervisorStatus{}, err
	}
	defer func() { _ = guard.Close() }()
	binding, err := m.readSupervisor()
	if err != nil {
		return SupervisorStatus{}, err
	}
	if binding.State == "initializing" {
		if err := m.finishSupervisorInitialization(&binding); err != nil {
			return SupervisorStatus{}, err
		}
		if reset {
			return m.supervisorStatus(ctx, binding)
		}
	}
	if binding.State != "removed" {
		if _, err := m.SetEnabled(ctx, binding.Agent.ID, false); err != nil {
			return SupervisorStatus{}, err
		}
		if err := m.settleSupervisorWork(ctx, &binding); err != nil {
			return SupervisorStatus{}, err
		}
		binding.Retained = append(binding.Retained, binding.Agent.ID)
		binding.State = "removed"
		if err := m.writeSupervisor(binding); err != nil {
			return SupervisorStatus{}, err
		}
	}
	if reset {
		settlement := binding.Settlement
		binding, err = m.planSupervisor(binding.Retained)
		if err != nil {
			return SupervisorStatus{}, err
		}
		binding.Settlement = settlement
		if err := m.finishSupervisorInitialization(&binding); err != nil {
			return SupervisorStatus{}, err
		}
	}
	return m.supervisorStatus(ctx, binding)
}

func (m *Manager) isSupervisor(id string) (bool, error) {
	binding, err := m.readSupervisor()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return binding.State != "removed" && binding.Agent.ID == id, err
}

func (m *Manager) supervisorAutostart(id string, desired bool) error {
	bound, err := m.isSupervisor(id)
	if err != nil || !bound {
		return err
	}
	_, err = m.deps.updateAgent(m.homeDir, id, agentstate.AgentUpdate{Autostart: &desired})
	return err
}

// SupervisorAction is the external lifecycle surface. Agent tools deliberately
// cannot target their own execution identity.
func (m *Manager) SupervisorAction(ctx context.Context, action string) (SupervisorStatus, error) {
	switch action {
	case "init":
		return m.EnsureSupervisor(ctx, true)
	case "repair":
		return m.RepairSupervisor(ctx)
	case "reset":
		return m.ResetSupervisor(ctx)
	case "remove":
		return m.RemoveSupervisor(ctx)
	case "status":
		return m.Supervisor(ctx)
	case "start", "stop", "enable", "disable":
	default:
		return SupervisorStatus{}, &ValidationError{Reason: "unsupported Supervisor action"}
	}
	guard, err := m.supervisorLock()
	if err != nil {
		return SupervisorStatus{}, err
	}
	defer func() { _ = guard.Close() }()
	binding, err := m.readSupervisor()
	if err != nil {
		return SupervisorStatus{}, err
	}
	if binding.State != "ready" {
		return SupervisorStatus{}, &ConflictError{Reason: "Supervisor is not ready; initialize or repair explicitly"}
	}
	switch action {
	case "start":
		_, err = m.Start(ctx, binding.Agent.ID)
	case "stop":
		_, err = m.Stop(ctx, binding.Agent.ID)
	case "enable":
		_, err = m.SetEnabled(ctx, binding.Agent.ID, true)
	case "disable":
		_, err = m.SetEnabled(ctx, binding.Agent.ID, false)
	}
	if err != nil {
		return SupervisorStatus{}, err
	}
	if action == "stop" || action == "disable" {
		if err := m.settleSupervisorWork(ctx, &binding); err != nil {
			return SupervisorStatus{}, err
		}
	}
	return m.supervisorStatus(ctx, binding)
}

func (m *Manager) settleSupervisorWork(ctx context.Context, binding *supervisorBinding) error {
	if m.settleSupervisor == nil {
		return nil
	}
	result, err := m.settleSupervisor(ctx, m.homeDir, binding.Agent.ID)
	result.AgentID = binding.Agent.ID
	if err != nil {
		result.Confirmed = false
		result.Reason = err.Error()
	}
	binding.Settlement = &result
	return m.writeSupervisor(*binding)
}
