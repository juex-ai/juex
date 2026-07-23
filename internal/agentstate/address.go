package agentstate

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/homestore"
)

// AgentAddress binds a stored Resident Agent identity to its owned state and
// external endpoint guard without exposing the underlying layout to consumers.
type AgentAddress struct {
	id               string
	stateDir         string
	endpointLockPath string
}

func NewAgentAddress(homeDir, agentID string) (AgentAddress, error) {
	if strings.TrimSpace(homeDir) == "" {
		return AgentAddress{}, errors.New("agentstate: effective home is required for agent address")
	}
	if !validAgentID.MatchString(agentID) {
		return AgentAddress{}, fmt.Errorf("agentstate: invalid agent id %q", agentID)
	}
	homeDir, err := canonicalPath(homeDir)
	if err != nil {
		return AgentAddress{}, fmt.Errorf("agentstate: resolve agent address home: %w", err)
	}
	lockPath, err := homestore.New(homeDir).LockPath(homestore.EndpointLocks, agentID)
	if err != nil {
		return AgentAddress{}, fmt.Errorf("agentstate: resolve endpoint lock for agent %q: %w", agentID, err)
	}
	return AgentAddress{
		id:               agentID,
		stateDir:         filepath.Join(homeDir, "agents", agentID),
		endpointLockPath: lockPath,
	}, nil
}

func (a AgentAddress) ID() string {
	return a.id
}

func (a AgentAddress) StateDir() string {
	return a.stateDir
}

func (a AgentAddress) EndpointLockPath() string {
	return a.endpointLockPath
}
