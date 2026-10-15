package agentstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/foundation/homestore"
)

// PlanIdentity allocates an ordinary identity without publishing it. An owner
// can persist the plan before creating recoverable system Agent resources.
func PlanIdentity(opts Options) (Agent, error) {
	workspace, err := canonicalExistingDir(opts.WorkDir)
	if err != nil {
		return Agent{}, err
	}
	home, err := resolveHome(opts.HomeDir)
	if err != nil {
		return Agent{}, err
	}
	for range 10 {
		id, err := randomID()
		if err != nil {
			return Agent{}, err
		}
		if _, err := os.Lstat(filepath.Join(home, "agents", id)); errors.Is(err, os.ErrNotExist) {
			return Agent{ID: id, Name: filepath.Base(workspace), Workspace: workspace, Enabled: true, CreatedAt: now().UTC()}, nil
		} else if err != nil {
			return Agent{}, err
		}
	}
	return Agent{}, errors.New("agentstate: could not allocate identity")
}

// RestoreIdentity publishes a previously persisted identity or repairs its
// missing registry metadata, preserving configuration and history. The owner
// must exclude live execution before repair. Existing conflicting metadata is
// never overwritten.
func RestoreIdentity(home string, expected Agent) (Resolution, error) {
	address, err := NewAgentAddress(home, expected.ID)
	if err != nil {
		return Resolution{}, err
	}
	entry := RegistryEntry{ID: expected.ID, Agent: expected, Address: address}
	if binding := InspectBinding(entry); binding.Kind != WorkspaceBound {
		return Resolution{}, errors.New(binding.Reason)
	}
	wg, err := homestore.AcquireLock(workspaceLockPath(home, expected.Workspace), homestore.LockWait)
	if err != nil {
		return Resolution{}, err
	}
	defer func() { _ = wg.Close() }()
	ag, err := acquireAgentLock(home, expected.ID)
	if err != nil {
		return Resolution{}, err
	}
	defer func() { _ = ag.Close() }()
	entries, err := ListRegistry(home)
	if err != nil {
		return Resolution{}, err
	}
	for _, other := range entries {
		if other.ID != expected.ID && other.Agent.Workspace == expected.Workspace {
			return Resolution{}, fmt.Errorf("agentstate: workspace already belongs to %s", other.ID)
		}
	}
	if current, err := loadValidRegistryEntry(home, expected.ID); err == nil {
		if current.Agent.Workspace != expected.Workspace {
			return Resolution{}, errors.New("agentstate: restored identity has a different workspace")
		}
		return Resolution{Agent: current.Agent, Address: address}, nil
	}
	info, err := os.Lstat(address.StateDir())
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(address.StateDir()), 0755); err != nil {
			return Resolution{}, err
		}
		if err := publishNewAgent(address, expected); err != nil {
			return Resolution{}, err
		}
		return Resolution{Agent: expected, Address: address, Created: true}, nil
	}
	if err != nil {
		return Resolution{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Resolution{}, errors.New("agentstate: identity path is not a directory")
	}
	metadata := filepath.Join(address.StateDir(), agentFileName)
	if _, err := os.Lstat(metadata); !errors.Is(err, os.ErrNotExist) {
		return Resolution{}, errors.New("agentstate: refusing to replace existing invalid identity metadata")
	}
	if err := atomicWriteJSON(metadata, expected, 0644); err != nil {
		return Resolution{}, err
	}
	return Resolution{Agent: expected, Address: address}, nil
}
