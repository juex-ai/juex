package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/juex-ai/juex/internal/agentstate"
)

func spawnDetached(executable, homeDir string, entry agentstate.RegistryEntry) (spawnedProcess, error) {
	logPath := fleetLogPath(entry.Address.StateDir())
	if err := os.MkdirAll(logsDir(entry.Address.StateDir()), 0o700); err != nil {
		return spawnedProcess{}, fmt.Errorf("fleet: create logs for agent %q: %w", entry.ID, err)
	}
	if err := rotateFleetLog(logPath); err != nil {
		return spawnedProcess{}, fmt.Errorf("fleet: rotate log for agent %q: %w", entry.ID, err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return spawnedProcess{}, fmt.Errorf("fleet: open log for agent %q: %w", entry.ID, err)
	}
	defer logFile.Close()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return spawnedProcess{}, fmt.Errorf("fleet: open null stdin: %w", err)
	}
	defer stdin.Close()

	cmd := exec.Command(executable, "-C", entry.Agent.Workspace, "serve", "--headless")
	cmd.Dir = entry.Agent.Workspace
	cmd.Env = withEnvironment(os.Environ(), "JUEX_HOME", homeDir)
	cmd.Stdin = stdin
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureDetached(cmd)
	if err := cmd.Start(); err != nil {
		return spawnedProcess{}, fmt.Errorf("fleet: start agent %q: %w (log: %s)", entry.ID, err, logPath)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	return spawnedProcess{PID: cmd.Process.Pid, Done: done, LogPath: logPath}, nil
}

func withEnvironment(environment []string, key, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		name, _, found := strings.Cut(item, "=")
		if found && strings.EqualFold(name, key) {
			continue
		}
		result = append(result, item)
	}
	return append(result, key+"="+value)
}
