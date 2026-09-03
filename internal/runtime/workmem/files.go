package workmem

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const contextRenewalBackupMarker = ".context-renewal-"

func replaceFileAtomic(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	keepTemp = false
	return syncParentDirectory(path)
}

func clearFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncParentDirectory(path)
}

func stageFileClearForContextRenewal(path, generationID string) (finalize, rollback func() error, err error) {
	if err := validateContextRenewalGenerationID(generationID); err != nil {
		return nil, nil, err
	}
	if err := recoverContextRenewalFile(path, generationID); err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return func() error { return nil }, func() error { return nil }, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("state path is not a regular file")
	}
	backupPath := contextRenewalBackupPath(path, generationID)
	if _, err := os.Lstat(backupPath); err == nil {
		return nil, nil, fmt.Errorf("recovery backup already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if err := os.Rename(path, backupPath); err != nil {
		return nil, nil, err
	}
	if err := syncParentDirectory(path); err != nil {
		rollbackErr := os.Rename(backupPath, path)
		if rollbackErr == nil {
			rollbackErr = syncParentDirectory(path)
		}
		return nil, nil, errors.Join(err, rollbackErr)
	}
	finalize = func() error {
		return clearFile(backupPath)
	}
	rollback = func() error {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("cannot restore over an existing state path")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(backupPath, path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		return syncParentDirectory(path)
	}
	return finalize, rollback, nil
}

func RecoverContextRenewalFiles(threadDir, currentGenerationID string) error {
	if strings.TrimSpace(threadDir) == "" {
		return nil
	}
	if err := validateContextRenewalGenerationID(currentGenerationID); err != nil {
		return err
	}
	return errors.Join(
		recoverContextRenewalFile(filepath.Join(threadDir, goalStateFile), currentGenerationID),
		recoverContextRenewalFile(filepath.Join(threadDir, NotesFileName), currentGenerationID),
	)
}

func recoverContextRenewalFile(path, currentGenerationID string) error {
	entries, err := os.ReadDir(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	prefix := filepath.Base(path) + contextRenewalBackupMarker
	var backups []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			backups = append(backups, entry.Name())
		}
	}
	if len(backups) > 1 {
		return fmt.Errorf("multiple context renewal backups for %s", filepath.Base(path))
	}
	if len(backups) == 0 {
		return nil
	}
	backupName := backups[0]
	backupGenerationID := strings.TrimPrefix(backupName, prefix)
	if err := validateContextRenewalGenerationID(backupGenerationID); err != nil {
		return fmt.Errorf("invalid context renewal backup %q: %w", backupName, err)
	}
	backupPath := filepath.Join(filepath.Dir(path), backupName)
	if backupGenerationID != currentGenerationID {
		return clearFile(backupPath)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("cannot restore %s over an existing state path", backupName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(backupPath, path); err != nil {
		return err
	}
	return syncParentDirectory(path)
}

func contextRenewalBackupPath(path, generationID string) string {
	return path + contextRenewalBackupMarker + generationID
}

func validateContextRenewalGenerationID(generationID string) error {
	if strings.TrimSpace(generationID) == "" || filepath.Base(generationID) != generationID || strings.ContainsAny(generationID, `/\\`) {
		return fmt.Errorf("invalid context renewal Generation ID %q", generationID)
	}
	return nil
}

func syncParentDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}
