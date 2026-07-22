package agentstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/homestore"
)

func ensureGlobalExclude() error {
	excludesPath, err := globalExcludesPath()
	if err != nil {
		return err
	}
	lockPath := globalExcludeLockPath(excludesPath)
	guard, err := homestore.AcquireLock(lockPath, homestore.LockWait)
	if err != nil {
		return fmt.Errorf("agentstate: lock global Git excludes: %w", err)
	}
	defer func() { _ = guard.Close() }()

	data, err := os.ReadFile(excludesPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agentstate: read global Git excludes %s: %w", excludesPath, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == globalExclude {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(excludesPath), 0o755); err != nil {
		return fmt.Errorf("agentstate: create global Git excludes directory: %w", err)
	}
	var updated bytes.Buffer
	updated.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		updated.WriteByte('\n')
	}
	updated.WriteString(globalExclude)
	updated.WriteByte('\n')
	if err := homestore.WriteFileAtomic(excludesPath, updated.Bytes(), 0o644, 0o755); err != nil {
		return fmt.Errorf("agentstate: update global Git excludes %s: %w", excludesPath, err)
	}
	return nil
}

func globalExcludeLockPath(excludesPath string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(excludesPath)))
	return filepath.Join(
		os.TempDir(),
		"juex-agentstate-global-excludes",
		hex.EncodeToString(sum[:])+".lock",
	)
}

func globalExcludesPath() (string, error) {
	cmd := exec.Command("git", "config", "--global", "--path", "--get", "core.excludesFile")
	output, err := cmd.Output()
	if err == nil {
		path := strings.TrimSpace(string(output))
		if path != "" {
			return canonicalPath(path)
		}
	}
	var exitErr *exec.ExitError
	if errors.Is(err, exec.ErrNotFound) {
		err = nil
	}
	if err != nil && !errors.As(err, &exitErr) {
		return "", fmt.Errorf("agentstate: read git core.excludesFile: %w", err)
	}
	if configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); configHome != "" {
		return filepath.Join(configHome, "git", "ignore"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("agentstate: resolve home for default Git excludes: %w", err)
	}
	return filepath.Join(home, ".config", "git", "ignore"), nil
}
