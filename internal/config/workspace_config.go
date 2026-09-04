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
	cfg, err := loadUserConfigForWorkDir(workDir, "")
	if err != nil {
		return cfg, err
	}
	loader := configImportLoaderFor(&cfg)
	if err := applyYAMLContentWithImportLoader(
		&cfg,
		content,
		workspaceYAMLSource(cfg.WorkspaceConfigPath()),
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
	return writeValidatedConfig(content, &cfg, cfg.WorkspaceConfigPath(), "workspace", 0o755, commitImportCache)
}

func writeValidatedConfig(
	content []byte,
	cfg *Config,
	path string,
	kind string,
	dirPerm os.FileMode,
	commitImportCache func(*Config) error,
) (writtenPath string, returnErr error) {
	if cfg.importLoader == nil {
		return "", fmt.Errorf("config: %s config update lost the import cache lock", kind)
	}
	if cfg.importLoader.cacheLock == nil {
		if err := cfg.importLoader.ensureConfigImportCacheLock(); err != nil {
			return "", err
		}
	}
	cacheLock := cfg.importLoader.takeConfigImportCacheLock()
	cfg.importLoader = nil
	if cacheLock == nil {
		return "", fmt.Errorf("config: %s config update requires the import cache lock", kind)
	}
	defer func() {
		if err := cacheLock.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("config: unlock import cache publication: %w", err))
		}
	}()
	if path == "" {
		return "", fmt.Errorf("config: %s config path is empty", kind)
	}
	lockHome := strings.TrimSpace(cfg.HomeJuexDir)
	if lockHome == "" {
		return "", fmt.Errorf("config: %s config update requires a configured JUEX_HOME", kind)
	}
	lockPath := filepath.Join(lockHome, ".locks", kind+"-config-"+sourceDigest(path)+".lock")
	lock, err := homestore.AcquireLock(lockPath, homestore.LockWait)
	if err != nil {
		return "", fmt.Errorf("config: lock %s config update: %w", kind, err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("config: unlock workspace config update: %w", err))
		}
	}()
	snapshot, err := snapshotConfigFile(path)
	if err != nil {
		return "", err
	}
	writes := uniqueConfigImportCacheWrites(cfg.pendingImportCache)
	commits, err := prepareConfigImportCacheCommits(writes)
	if err != nil {
		return "", err
	}
	journalPath, err := beginConfigImportCachePublicationWithConfig(lockHome, commits, &snapshot)
	if err != nil {
		return "", err
	}
	recoverPreparedPublication := func(operationErr error) error {
		return errors.Join(operationErr, recoverConfigImportCachePublicationAt(journalPath))
	}
	if err := homestore.WriteFileAtomic(path, content, 0o600, dirPerm); err != nil {
		return "", recoverPreparedPublication(err)
	}
	if err := commitImportCache(cfg); err != nil {
		return "", recoverPreparedPublication(err)
	}
	if err := markConfigImportCachePublicationCommitted(journalPath); err != nil {
		if journal, readErr := readConfigImportCacheJournal(journalPath); readErr == nil && journal.State == configImportJournalCommitted {
			_ = clearConfigImportCacheJournal(journalPath)
			return path, nil
		}
		return "", recoverPreparedPublication(fmt.Errorf("config: commit %s config publication: %w", kind, err))
	}
	_ = clearConfigImportCacheJournal(journalPath)
	return path, nil
}

type configFileSnapshot struct {
	path    string
	data    []byte
	mode    os.FileMode
	existed bool
}

func snapshotConfigFile(path string) (configFileSnapshot, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return configFileSnapshot{path: path}, nil
	}
	if err != nil {
		return configFileSnapshot{}, fmt.Errorf("config: inspect managed config %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return configFileSnapshot{}, fmt.Errorf("config: managed config %s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return configFileSnapshot{}, fmt.Errorf("config: read managed config %s: %w", path, err)
	}
	return configFileSnapshot{path: path, data: data, mode: info.Mode().Perm(), existed: true}, nil
}

func rollbackConfigFile(snapshot configFileSnapshot) error {
	if snapshot.existed {
		if err := homestore.WriteFileAtomic(snapshot.path, snapshot.data, snapshot.mode, 0o755); err != nil {
			return fmt.Errorf("config: roll back managed config %s: %w", snapshot.path, err)
		}
		return nil
	}
	if err := os.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("config: roll back managed config %s: %w", snapshot.path, err)
	}
	if err := homestore.SyncDir(filepath.Dir(snapshot.path)); err != nil {
		return fmt.Errorf("config: sync managed config rollback %s: %w", snapshot.path, err)
	}
	return nil
}
