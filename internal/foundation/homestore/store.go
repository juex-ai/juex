// Package homestore owns crash-safe file publication and advisory locking for
// state rooted at an effective JUEX_HOME.
package homestore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type LockMode uint8

const (
	LockWait LockMode = iota
	LockTry
)

type LockScope string

const (
	AgentLocks    LockScope = "agents"
	EndpointLocks LockScope = "endpoints"
	FleetLocks    LockScope = "fleet"
)

var ErrLockBusy = errors.New("home store lock is already held")

// AtomicWriteError reports whether the target was replaced before publication
// failed. Callers can use that fact to decide whether rollback owns the target.
type AtomicWriteError struct {
	Operation string
	Path      string
	Replaced  bool
	Err       error
}

func (e *AtomicWriteError) Error() string {
	return fmt.Sprintf("homestore: %s %s: %v", e.Operation, e.Path, e.Err)
}

func (e *AtomicWriteError) Unwrap() error {
	return e.Err
}

// ReplacementOccurred reports whether an atomic write error happened after
// the destination had been replaced.
func ReplacementOccurred(err error) bool {
	var writeErr *AtomicWriteError
	return errors.As(err, &writeErr) && writeErr.Replaced
}

type Store struct {
	homeDir string
}

func New(homeDir string) Store {
	if strings.TrimSpace(homeDir) == "" {
		return Store{}
	}
	return Store{homeDir: filepath.Clean(homeDir)}
}

func (s Store) Lock(scope LockScope, id string, mode LockMode) (*Lock, error) {
	path, err := s.LockPath(scope, id)
	if err != nil {
		return nil, err
	}
	return AcquireLock(path, mode)
}

// LockPath returns the canonical path for one home-scoped lock.
func (s Store) LockPath(scope LockScope, id string) (string, error) {
	if s.homeDir == "" {
		return "", errors.New("homestore: home directory is required")
	}
	if !validLockScope(scope) {
		return "", fmt.Errorf("homestore: invalid lock scope %q", scope)
	}
	if !validLockID(id) {
		return "", fmt.Errorf("homestore: invalid lock id %q", id)
	}
	return filepath.Join(s.homeDir, ".locks", string(scope), id+".lock"), nil
}

func AcquireLock(path string, mode LockMode) (*Lock, error) {
	if mode != LockWait && mode != LockTry {
		return nil, fmt.Errorf("homestore: invalid lock mode %d", mode)
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("homestore: lock path is required")
	}
	return acquireLock(path, mode)
}

func validLockScope(scope LockScope) bool {
	switch scope {
	case AgentLocks, EndpointLocks, FleetLocks:
		return true
	default:
		return false
	}
}

func validLockID(id string) bool {
	return id != "" && filepath.Base(id) == id &&
		id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}

func WriteFileAtomic(path string, data []byte, fileMode, parentMode os.FileMode) error {
	return writeFileAtomicWith(path, data, fileMode, parentMode, true, SyncDir)
}

// WriteFileAtomicExisting publishes a file without creating its parent. It is
// used when recreating a removed state directory would violate ownership.
func WriteFileAtomicExisting(path string, data []byte, fileMode os.FileMode) error {
	return writeFileAtomicWith(path, data, fileMode, 0, false, SyncDir)
}

func writeFileAtomicWith(
	path string,
	data []byte,
	fileMode, parentMode os.FileMode,
	createParent bool,
	syncDir func(string) error,
) error {
	dir := filepath.Dir(path)
	var missingDirs []string
	if createParent {
		var err error
		missingDirs, err = missingParentDirectories(dir)
		if err != nil {
			return atomicWriteError("inspect parent directory", dir, false, err)
		}
		if err := os.MkdirAll(dir, parentMode); err != nil {
			return atomicWriteError("create parent directory", dir, false, err)
		}
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return atomicWriteError("create temporary file for", path, false, err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(fileMode); err != nil {
		cleanup()
		return atomicWriteError("set temporary file mode for", path, false, err)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return atomicWriteError("write temporary file for", path, false, err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return atomicWriteError("sync temporary file for", path, false, err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return atomicWriteError("close temporary file for", path, false, err)
	}
	if err := replaceFile(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return atomicWriteError("replace", path, false, err)
	}
	if err := syncDir(dir); err != nil {
		return atomicWriteError("sync parent directory", dir, true, err)
	}
	for _, created := range missingDirs {
		parent := filepath.Dir(created)
		if err := syncDir(parent); err != nil {
			return atomicWriteError("sync created directory parent", parent, true, err)
		}
	}
	return nil
}

func missingParentDirectories(path string) ([]string, error) {
	var missing []string
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("%s is not a directory", dir)
			}
			return missing, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			return missing, nil
		}
	}
}

func atomicWriteError(operation, path string, replaced bool, err error) error {
	return &AtomicWriteError{Operation: operation, Path: path, Replaced: replaced, Err: err}
}
