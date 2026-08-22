package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/homestore"
)

func ValidateWorkspaceConfig(content []byte, workDir string) (Config, error) {
	cfg, err := validateWorkspaceConfig(content, workDir)
	if err != nil {
		return cfg, err
	}
	cfg.pendingImportCache = nil
	return cfg, nil
}

func validateWorkspaceConfig(content []byte, workDir string) (Config, error) {
	cfg, err := loadUserConfigForWorkDir(workDir)
	if err != nil {
		return cfg, err
	}
	loader := configImportLoaderFor(&cfg)
	if err := applyYAMLContentWithImportLoader(
		&cfg,
		content,
		workspaceYAMLSource(cfg.RuntimeConfigPath()),
		loader,
		applyYAMLDataOptions{},
	); err != nil {
		cfg.pendingImportCache = nil
		cfg.importLoader = nil
		return cfg, errors.Join(err, loader.closeConfigImportCacheLock())
	}
	if err := finalizeConfigLoadForValidationWithoutImportCache(&cfg, nil, true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func WriteWorkspaceConfig(content []byte, workDir string) (string, error) {
	return writeWorkspaceConfig(content, workDir, func(cfg *Config) error {
		return commitConfigImportCachesWhileLocked(cfg, func(path string, data []byte) error {
			return homestore.WriteFileAtomic(path, data, 0o600, 0o700)
		})
	})
}

func writeWorkspaceConfig(content []byte, workDir string, commitImportCache func(*Config) error) (writtenPath string, returnErr error) {
	cfg, err := validateWorkspaceConfig(content, workDir)
	if err != nil {
		return "", err
	}
	path := cfg.RuntimeConfigPath()
	if path == "" {
		return "", fmt.Errorf("config: workspace config path is empty")
	}
	lockHome := strings.TrimSpace(cfg.HomeJuexDir)
	if lockHome == "" {
		return "", fmt.Errorf("config: workspace config update requires a configured JUEX_HOME")
	}
	lockPath := filepath.Join(lockHome, ".locks", "workspace-config-"+sourceDigest(path)+".lock")
	lock, err := homestore.AcquireLock(lockPath, homestore.LockWait)
	if err != nil {
		return "", fmt.Errorf("config: lock workspace config update: %w", err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("config: unlock workspace config update: %w", err))
		}
	}()
	if len(cfg.pendingImportCache) > 0 {
		cacheLock, err := homestore.AcquireLock(configImportCacheLockPath(lockHome), homestore.LockWait)
		if err != nil {
			return "", fmt.Errorf("config: lock import cache publication: %w", err)
		}
		defer func() {
			if err := cacheLock.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("config: unlock import cache publication: %w", err))
			}
		}()
	}
	snapshot, err := snapshotWorkspaceConfig(path)
	if err != nil {
		return "", err
	}
	if err := homestore.WriteFileAtomic(path, content, 0o600, 0o755); err != nil {
		if homestore.ReplacementOccurred(err) {
			return "", errors.Join(err, rollbackWorkspaceConfig(snapshot))
		}
		return "", err
	}
	if err := commitImportCache(&cfg); err != nil {
		return "", errors.Join(err, rollbackWorkspaceConfig(snapshot))
	}
	return path, nil
}

type workspaceConfigSnapshot struct {
	path    string
	data    []byte
	mode    os.FileMode
	existed bool
}

func snapshotWorkspaceConfig(path string) (workspaceConfigSnapshot, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return workspaceConfigSnapshot{path: path}, nil
	}
	if err != nil {
		return workspaceConfigSnapshot{}, fmt.Errorf("config: inspect workspace config %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return workspaceConfigSnapshot{}, fmt.Errorf("config: workspace config %s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return workspaceConfigSnapshot{}, fmt.Errorf("config: read workspace config %s: %w", path, err)
	}
	return workspaceConfigSnapshot{path: path, data: data, mode: info.Mode().Perm(), existed: true}, nil
}

func rollbackWorkspaceConfig(snapshot workspaceConfigSnapshot) error {
	if snapshot.existed {
		if err := homestore.WriteFileAtomic(snapshot.path, snapshot.data, snapshot.mode, 0o755); err != nil {
			return fmt.Errorf("config: roll back workspace config %s: %w", snapshot.path, err)
		}
		return nil
	}
	if err := os.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("config: roll back workspace config %s: %w", snapshot.path, err)
	}
	if err := homestore.SyncDir(filepath.Dir(snapshot.path)); err != nil {
		return fmt.Errorf("config: sync workspace config rollback %s: %w", snapshot.path, err)
	}
	return nil
}
