package thread

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/foundation/homestore"
)

const (
	contextRenewalBackupMarker = ".context-renewal-"
	contextRenewalManifestFile = "context-renewal.json"
)

var (
	errContextRenewalInProgress = errors.New("thread: Context renewal is in progress")
	contextRenewalTransactions  = struct {
		sync.Mutex
		active map[string]int
		files  map[string]bool
	}{active: make(map[string]int), files: make(map[string]bool)}
)

type contextRenewalFile struct {
	Path         string `json:"path"`
	GenerationID string `json:"generation_id"`
}
type contextRenewalManifest struct {
	Version int                  `json:"version"`
	Files   []contextRenewalFile `json:"files"`
}

// ContextRenewalFileClear is a durable staged file removal associated with a
// Context Generation. Thread owns only the generic file transaction; modules
// choose which files participate.
type ContextRenewalFileClear struct {
	Finalize func() error
	Rollback func() error
}

// StageContextRenewalFileClear records a Thread-relative file transaction before
// renaming its authority file. The actual Thread root is independent of the
// file's parent directory, so nested module files survive interrupted renewal.
func StageContextRenewalFileClear(threadDir, path, generationID string) (ContextRenewalFileClear, error) {
	threadDir = filepath.Clean(threadDir)
	relative, err := filepath.Rel(threadDir, path)
	if err != nil {
		return ContextRenewalFileClear{}, err
	}
	entry := contextRenewalFile{Path: filepath.ToSlash(relative), GenerationID: generationID}
	if err := validateContextRenewalFile(threadDir, entry); err != nil {
		return ContextRenewalFileClear{}, err
	}
	key := threadDir + "\x00" + entry.Path
	contextRenewalTransactions.Lock()
	defer contextRenewalTransactions.Unlock()
	if contextRenewalTransactions.files[key] {
		return ContextRenewalFileClear{}, errContextRenewalInProgress
	}
	manifest, err := readContextRenewalManifest(threadDir)
	if err != nil {
		return ContextRenewalFileClear{}, err
	}
	// Recover only this file. Other files from the same /new may already be staged.
	for _, previous := range manifest.Files {
		if previous.Path != entry.Path {
			continue
		}
		if err := recoverContextRenewalFile(threadDir, previous, generationID); err != nil {
			return ContextRenewalFileClear{}, err
		}
		manifest.Files = slices.DeleteFunc(manifest.Files, func(candidate contextRenewalFile) bool { return candidate.Path == entry.Path })
		if err := writeContextRenewalManifest(threadDir, manifest); err != nil {
			return ContextRenewalFileClear{}, err
		}
		break
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return noOpContextRenewalFileClear(), nil
	}
	if err != nil {
		return ContextRenewalFileClear{}, err
	}
	if !info.Mode().IsRegular() {
		return ContextRenewalFileClear{}, errors.New("thread: Context renewal state path is not a regular file")
	}
	backup := contextRenewalBackupPath(path, generationID)
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return ContextRenewalFileClear{}, fmt.Errorf("thread: Context renewal backup already exists: %w", errors.Join(err, os.ErrExist))
	}
	manifest.Files = append(manifest.Files, entry)
	if err := writeContextRenewalManifest(threadDir, manifest); err != nil {
		return ContextRenewalFileClear{}, err
	}
	if err := os.Rename(path, backup); err != nil {
		return ContextRenewalFileClear{}, err
	}
	if err := homestore.SyncDir(filepath.Dir(path)); err != nil {
		rollbackErr := os.Rename(backup, path)
		if rollbackErr == nil {
			rollbackErr = homestore.SyncDir(filepath.Dir(path))
		}
		return ContextRenewalFileClear{}, errors.Join(err, rollbackErr)
	}
	contextRenewalTransactions.active[threadDir]++
	contextRenewalTransactions.files[key] = true
	released, completed := false, false
	finish := func(restore bool) error {
		contextRenewalTransactions.Lock()
		defer contextRenewalTransactions.Unlock()
		if completed {
			return nil
		}
		defer func() {
			if released {
				return
			}
			released = true
			delete(contextRenewalTransactions.files, key)
			contextRenewalTransactions.active[threadDir]--
			if contextRenewalTransactions.active[threadDir] == 0 {
				delete(contextRenewalTransactions.active, threadDir)
			}
		}()
		current, err := readContextRenewalManifest(threadDir)
		if err != nil {
			return err
		}
		if slices.Contains(current.Files, entry) {
			if err := finishContextRenewalFile(threadDir, entry, restore); err != nil {
				return err
			}
			current.Files = slices.DeleteFunc(current.Files, func(candidate contextRenewalFile) bool { return candidate == entry })
			if err := writeContextRenewalManifest(threadDir, current); err != nil {
				return err
			}
		}
		completed = true
		return nil
	}
	return ContextRenewalFileClear{Finalize: func() error { return finish(false) }, Rollback: func() error { return finish(true) }}, nil
}
func noOpContextRenewalFileClear() ContextRenewalFileClear {
	return ContextRenewalFileClear{Finalize: func() error { return nil }, Rollback: func() error { return nil }}
}

// Recovery uses only Thread-owned transaction paths and the committed
// Generation. It does not discover modules or interpret their resource state.
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
	manifest, err := readContextRenewalManifest(threadDir)
	if err != nil {
		return err
	}
	for _, entry := range slices.Clone(manifest.Files) {
		if err := recoverContextRenewalFile(threadDir, entry, currentGenerationID); err != nil {
			return err
		}
		manifest.Files = slices.DeleteFunc(manifest.Files, func(candidate contextRenewalFile) bool { return candidate == entry })
		if err := writeContextRenewalManifest(threadDir, manifest); err != nil {
			return err
		}
	}
	return nil
}
func recoverContextRenewalFile(threadDir string, entry contextRenewalFile, currentGenerationID string) error {
	return finishContextRenewalFile(threadDir, entry, entry.GenerationID == currentGenerationID)
}
func finishContextRenewalFile(threadDir string, entry contextRenewalFile, restore bool) error {
	if err := validateContextRenewalFile(threadDir, entry); err != nil {
		return err
	}
	path := filepath.Join(threadDir, filepath.FromSlash(entry.Path))
	backup := contextRenewalBackupPath(path, entry.GenerationID)
	info, err := os.Lstat(backup)
	// Registration may have committed before rename, rollback may already have
	// finished, or the resource owner may have retired. Never recreate a missing
	// resource directory or overwrite canonical state in these cases.
	if errors.Is(err, os.ErrNotExist) {
		return syncContextRenewalParent(threadDir, path)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("thread: Context renewal backup is not a regular file")
	}
	if restore {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("thread: cannot restore %s over an existing state path: %w", filepath.Base(backup), errors.Join(err, os.ErrExist))
		}
		if err := os.Rename(backup, path); err != nil {
			return err
		}
	} else {
		if err := os.Remove(backup); err != nil {
			return err
		}
	}
	return syncContextRenewalParent(threadDir, path)
}
func syncContextRenewalParent(threadDir, path string) error {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		err := homestore.SyncDir(dir)
		if !errors.Is(err, os.ErrNotExist) || dir == threadDir {
			return err
		}
	}
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
	contextRenewalTransactions.Lock()
	defer contextRenewalTransactions.Unlock()
	manifest, err := readContextRenewalManifest(threadDir)
	return len(manifest.Files) > 0, err
}
func contextRenewalBackupPath(path, generationID string) string {
	return path + contextRenewalBackupMarker + generationID
}

func readContextRenewalManifest(threadDir string) (contextRenewalManifest, error) {
	manifest := contextRenewalManifest{Version: 1}
	path := filepath.Join(threadDir, contextRenewalManifestFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return manifest, nil
	}
	if err != nil {
		return manifest, err
	}
	if !info.Mode().IsRegular() {
		return manifest, errors.New("thread: Context renewal manifest is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return manifest, errors.New("thread: trailing Context renewal manifest content")
	}
	if manifest.Version != 1 {
		return manifest, errors.New("thread: unsupported Context renewal manifest version")
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Files {
		if err := validateContextRenewalFile(threadDir, entry); err != nil {
			return manifest, err
		}
		if seen[entry.Path] {
			return manifest, errors.New("thread: duplicate Context renewal transaction")
		}
		seen[entry.Path] = true
	}
	return manifest, nil
}
func writeContextRenewalManifest(threadDir string, manifest contextRenewalManifest) error {
	path := filepath.Join(threadDir, contextRenewalManifestFile)
	if len(manifest.Files) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return homestore.SyncDir(threadDir)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return homestore.WriteFileAtomicExisting(path, append(data, '\n'), 0600)
}
func validateContextRenewalFile(threadDir string, entry contextRenewalFile) error {
	if _, err := parseGenerationID(entry.GenerationID); err != nil {
		return err
	}
	path := filepath.FromSlash(entry.Path)
	if !filepath.IsLocal(path) || path == "." || strings.Contains(entry.Path, `\`) || filepath.ToSlash(filepath.Clean(path)) != entry.Path {
		return errors.New("thread: invalid Context renewal relative state path")
	}
	for dir := filepath.Dir(filepath.Join(threadDir, path)); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("thread: Context renewal state parent is not a real directory")
		}
		if dir == threadDir {
			break
		}
	}
	return nil
}
