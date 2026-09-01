package agentstate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/homestore"
)

func publishNewAgent(address AgentAddress, agent Agent) (err error) {
	agentDir := address.StateDir()
	agentsDir := filepath.Dir(agentDir)
	stageDir, err := os.MkdirTemp(agentsDir, "."+agent.ID+".creating-")
	if err != nil {
		return fmt.Errorf("agentstate: create staging directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(stageDir)
		}
	}()

	for _, name := range []string{"threads", filepath.Join("archive", "threads"), "logs"} {
		if err := os.MkdirAll(filepath.Join(stageDir, name), 0o755); err != nil {
			return err
		}
	}
	if err := atomicWriteJSON(filepath.Join(stageDir, agentFileName), agent, 0o644); err != nil {
		return err
	}
	if err := homestore.SyncDir(stageDir); err != nil {
		return fmt.Errorf("agentstate: sync staging directory: %w", err)
	}
	if err := os.Rename(stageDir, agentDir); err != nil {
		return fmt.Errorf("agentstate: publish agent directory %s: %w", agentDir, err)
	}
	if err := homestore.SyncDir(agentsDir); err != nil {
		_ = os.RemoveAll(agentDir)
		_ = homestore.SyncDir(agentsDir)
		return fmt.Errorf("agentstate: sync agent registry: %w", err)
	}
	return nil
}
