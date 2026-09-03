package thread

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/homestore"
)

const contextRenewalBackupMarker = ".context-renewal-"

var (
	errContextRenewalInProgress = errors.New("thread: Context renewal is in progress")
	contextRenewalTransactions  = struct {
		sync.Mutex
		active map[string]int
	}{active: make(map[string]int)}
)

// ContextRenewalFileClear is a durable staged file removal associated with a
// Context Generation. The Thread layer owns only the generic file transaction;
// modules choose which files participate.
type ContextRenewalFileClear struct {
	Finalize func() error
	Rollback func() error
}

// StageContextRenewalFileClear makes path absent while retaining a durable
// Generation-labeled backup until the caller knows whether the Journal
// boundary committed.
func StageContextRenewalFileClear(path, generationID string) (ContextRenewalFileClear, error) {
	if _, err := parseGenerationID(generationID); err != nil {
		return ContextRenewalFileClear{}, err
	}
	if strings.TrimSpace(path) == "" || filepath.Base(path) == "." {
		return ContextRenewalFileClear{}, fmt.Errorf("thread: Context renewal state path is required")
	}
	release, err := beginContextRenewalFileClear(path, generationID)
	if err != nil {
		return ContextRenewalFileClear{}, err
	}
	keepActive := false
	defer func() {
		if !keepActive {
			release()
		}
	}()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return noOpContextRenewalFileClear(), nil
	}
	if err != nil {
		return ContextRenewalFileClear{}, err
	}
	if !info.Mode().IsRegular() {
		return ContextRenewalFileClear{}, fmt.Errorf("thread: Context renewal state path is not a regular file")
	}
	backupPath := contextRenewalBackupPath(path, generationID)
	if _, err := os.Lstat(backupPath); err == nil {
		return ContextRenewalFileClear{}, fmt.Errorf("thread: Context renewal recovery backup already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return ContextRenewalFileClear{}, err
	}
	if err := os.Rename(path, backupPath); err != nil {
		return ContextRenewalFileClear{}, err
	}
	if err := homestore.SyncDir(filepath.Dir(path)); err != nil {
		rollbackErr := os.Rename(backupPath, path)
		if rollbackErr == nil {
			rollbackErr = homestore.SyncDir(filepath.Dir(path))
		}
		return ContextRenewalFileClear{}, errors.Join(err, rollbackErr)
	}
	keepActive = true
	return contextRenewalFileClearWithRelease(path, backupPath, release), nil
}

func noOpContextRenewalFileClear() ContextRenewalFileClear {
	return ContextRenewalFileClear{
		Finalize: func() error { return nil },
		Rollback: func() error { return nil },
	}
}

func contextRenewalFileClearWithRelease(path, backupPath string, release func()) ContextRenewalFileClear {
	return ContextRenewalFileClear{
		Finalize: func() error {
			err := removeContextRenewalBackup(backupPath)
			release()
			return err
		},
		Rollback: func() error {
			err := restoreContextRenewalBackup(path, backupPath)
			release()
			return err
		},
	}
}

func beginContextRenewalFileClear(path, generationID string) (func(), error) {
	threadDir := filepath.Clean(filepath.Dir(path))
	contextRenewalTransactions.Lock()
	defer contextRenewalTransactions.Unlock()
	if err := recoverContextRenewalFile(path, generationID); err != nil {
		return nil, err
	}
	contextRenewalTransactions.active[threadDir]++
	var once sync.Once
	return func() {
		once.Do(func() {
			contextRenewalTransactions.Lock()
			defer contextRenewalTransactions.Unlock()
			remaining := contextRenewalTransactions.active[threadDir] - 1
			if remaining > 0 {
				contextRenewalTransactions.active[threadDir] = remaining
			} else {
				delete(contextRenewalTransactions.active, threadDir)
			}
		})
	}, nil
}

// recoverContextRenewalFiles resolves every module-staged file in threadDir.
// Matching the current Generation means the boundary did not commit and the
// backup is restored; a different Generation means it committed and the
// backup is discarded.
func recoverContextRenewalFiles(threadDir, currentGenerationID string) error {
	threadDir = filepath.Clean(threadDir)
	contextRenewalTransactions.Lock()
	defer contextRenewalTransactions.Unlock()
	if contextRenewalTransactions.active[threadDir] > 0 {
		return errContextRenewalInProgress
	}
	if _, err := parseGenerationID(currentGenerationID); err != nil {
		return err
	}
	entries, err := os.ReadDir(threadDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	seen := make(map[string]string)
	for _, entry := range entries {
		marker := strings.LastIndex(entry.Name(), contextRenewalBackupMarker)
		if marker <= 0 {
			continue
		}
		canonicalName := entry.Name()[:marker]
		backupGenerationID := entry.Name()[marker+len(contextRenewalBackupMarker):]
		if _, err := parseGenerationID(backupGenerationID); err != nil {
			return fmt.Errorf("thread: invalid Context renewal backup %q: %w", entry.Name(), err)
		}
		if previous, ok := seen[canonicalName]; ok {
			return fmt.Errorf("thread: multiple Context renewal backups for %s: %s and %s", canonicalName, previous, entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("thread: Context renewal backup %q is not a regular file", entry.Name())
		}
		seen[canonicalName] = entry.Name()
		canonicalPath := filepath.Join(threadDir, canonicalName)
		backupPath := filepath.Join(threadDir, entry.Name())
		if backupGenerationID != currentGenerationID {
			if err := removeContextRenewalBackup(backupPath); err != nil {
				return err
			}
			continue
		}
		if err := restoreContextRenewalBackup(canonicalPath, backupPath); err != nil {
			return err
		}
	}
	return nil
}

func recoverContextRenewalFilesFromMetadata(threadDir, threadID string) error {
	present, err := contextRenewalFilesPresent(threadDir)
	if err != nil || !present {
		return err
	}
	metadata, err := readProjectionFile(threadDir, threadID)
	if err != nil {
		return err
	}
	return recoverContextRenewalFiles(threadDir, metadata.CurrentGeneration.ID)
}

func contextRenewalFilesPresent(threadDir string) (bool, error) {
	entries, err := os.ReadDir(threadDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if strings.LastIndex(entry.Name(), contextRenewalBackupMarker) > 0 {
			return true, nil
		}
	}
	return false, nil
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
	var backupName string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if backupName != "" {
			return fmt.Errorf("thread: multiple Context renewal backups for %s", filepath.Base(path))
		}
		backupName = entry.Name()
	}
	if backupName == "" {
		return nil
	}
	backupGenerationID := strings.TrimPrefix(backupName, prefix)
	if _, err := parseGenerationID(backupGenerationID); err != nil {
		return fmt.Errorf("thread: invalid Context renewal backup %q: %w", backupName, err)
	}
	backupPath := filepath.Join(filepath.Dir(path), backupName)
	if backupGenerationID != currentGenerationID {
		return removeContextRenewalBackup(backupPath)
	}
	return restoreContextRenewalBackup(path, backupPath)
}

func restoreContextRenewalBackup(path, backupPath string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("thread: cannot restore %s over an existing state path", filepath.Base(backupPath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(backupPath, path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return homestore.SyncDir(filepath.Dir(path))
}

func removeContextRenewalBackup(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return homestore.SyncDir(filepath.Dir(path))
}

func contextRenewalBackupPath(path, generationID string) string {
	return path + contextRenewalBackupMarker + generationID
}
