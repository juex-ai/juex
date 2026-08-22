package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/homestore"
)

func ValidateWorkspaceConfig(content []byte, workDir string) (cfg Config, returnErr error) {
	cfg, returnErr = validateWorkspaceConfig(content, workDir)
	if returnErr != nil {
		return cfg, returnErr
	}
	cfg.pendingImportCache = nil
	if cfg.importLoader != nil {
		returnErr = cfg.importLoader.closeConfigImportCacheLock()
		cfg.importLoader = nil
	}
	return cfg, returnErr
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
	if err := finalizeConfigLoadForValidationRetainingImportCacheLock(&cfg, nil, true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func WriteWorkspaceConfig(content []byte, workDir string) (string, error) {
	return writeWorkspaceConfig(content, workDir, func(cfg *Config) error {
		return publishPendingConfigImportCachesWhileLocked(cfg, func(path string, data []byte) error {
			return homestore.WriteFileAtomic(path, data, 0o600, 0o700)
		})
	})
}

func writeWorkspaceConfig(content []byte, workDir string, commitImportCache func(*Config) error) (writtenPath string, returnErr error) {
	cfg, err := validateWorkspaceConfig(content, workDir)
	if err != nil {
		return "", err
	}
	if cfg.importLoader == nil {
		return "", errors.New("config: workspace config update lost the import cache lock")
	}
	if cfg.importLoader.cacheLock == nil {
		if err := cfg.importLoader.ensureConfigImportCacheLock(); err != nil {
			return "", err
		}
	}
	cacheLock := cfg.importLoader.takeConfigImportCacheLock()
	cfg.importLoader = nil
	if cacheLock == nil {
		return "", errors.New("config: workspace config update requires the import cache lock")
	}
	defer func() {
		if err := cacheLock.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("config: unlock import cache publication: %w", err))
		}
	}()
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
	snapshot, err := snapshotWorkspaceConfig(path)
	if err != nil {
		return "", err
	}
	writes := uniqueConfigImportCacheWrites(cfg.pendingImportCache)
	commits, err := prepareConfigImportCacheCommits(writes)
	if err != nil {
		return "", err
	}
	journalPath, err := beginConfigImportCachePublicationWithWorkspace(lockHome, commits, &snapshot)
	if err != nil {
		return "", err
	}
	recoverPreparedPublication := func(operationErr error) error {
		return errors.Join(operationErr, recoverConfigImportCachePublicationAt(journalPath))
	}
	if err := homestore.WriteFileAtomic(path, content, 0o600, 0o755); err != nil {
		return "", recoverPreparedPublication(err)
	}
	if err := commitImportCache(&cfg); err != nil {
		return "", recoverPreparedPublication(err)
	}
	if err := markConfigImportCachePublicationCommitted(journalPath); err != nil {
		if journal, readErr := readConfigImportCacheJournal(journalPath); readErr == nil && journal.State == configImportJournalCommitted {
			_ = clearConfigImportCacheJournal(journalPath)
			return path, nil
		}
		return "", recoverPreparedPublication(fmt.Errorf("config: commit workspace config publication: %w", err))
	}
	_ = clearConfigImportCacheJournal(journalPath)
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
