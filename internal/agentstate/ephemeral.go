package agentstate

import (
	"fmt"
	"os"
	"path/filepath"
)

// Ephemeral owns one temporary, process-local agent home.
type Ephemeral struct {
	Resolution Resolution
	RootDir    string
}

// CreateEphemeral allocates identity-owned state without publishing a
// workspace marker or adding anything to the effective JUEX_HOME registry.
func CreateEphemeral(workDir string) (*Ephemeral, error) {
	canonicalWorkDir, err := canonicalExistingDir(workDir)
	if err != nil {
		return nil, fmt.Errorf("agentstate: resolve ephemeral workspace: %w", err)
	}
	rootDir, err := os.MkdirTemp("", "juex-ephemeral-")
	if err != nil {
		return nil, fmt.Errorf("agentstate: create ephemeral root: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(rootDir)
	}

	agentID, err := generateID()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("agentstate: generate ephemeral agent id: %w", err)
	}
	if !validAgentID.MatchString(agentID) {
		cleanup()
		return nil, fmt.Errorf("agentstate: generated invalid ephemeral agent id %q", agentID)
	}
	address, err := NewAgentAddress(rootDir, agentID)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
		cleanup()
		return nil, fmt.Errorf("agentstate: create ephemeral agent state: %w", err)
	}
	agent := Agent{
		ID:        agentID,
		Name:      filepath.Base(canonicalWorkDir),
		Workspace: canonicalWorkDir,
		Enabled:   false,
		Autostart: false,
		CreatedAt: now().UTC(),
	}
	return &Ephemeral{
		Resolution: Resolution{
			Agent:   agent,
			Address: address,
			Created: true,
		},
		RootDir: rootDir,
	}, nil
}

// Remove deletes the complete temporary home. It is safe to call more than
// once after a successful removal.
func (e *Ephemeral) Remove() error {
	if e == nil || e.RootDir == "" {
		return nil
	}
	if err := os.RemoveAll(e.RootDir); err != nil {
		return fmt.Errorf("agentstate: remove ephemeral root %s: %w", e.RootDir, err)
	}
	e.RootDir = ""
	return nil
}
