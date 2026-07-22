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
	if s.homeDir == "" {
		return nil, errors.New("homestore: home directory is required")
	}
	if !validLockScope(scope) {
		return nil, fmt.Errorf("homestore: invalid lock scope %q", scope)
	}
	if !validLockID(id) {
		return nil, fmt.Errorf("homestore: invalid lock id %q", id)
	}
	return AcquireLock(filepath.Join(s.homeDir, ".locks", string(scope), id+".lock"), mode)
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, parentMode); err != nil {
		return fmt.Errorf("homestore: create parent directory %s: %w", dir, err)
	}
	return writeFileAtomic(path, data, fileMode)
}

// WriteFileAtomicExisting publishes a file without creating its parent. It is
// used when recreating a removed state directory would violate ownership.
func WriteFileAtomicExisting(path string, data []byte, fileMode os.FileMode) error {
	return writeFileAtomic(path, data, fileMode)
}

func writeFileAtomic(path string, data []byte, fileMode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("homestore: create temporary file for %s: %w", path, err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(fileMode); err != nil {
		cleanup()
		return fmt.Errorf("homestore: set temporary file mode for %s: %w", path, err)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("homestore: write temporary file for %s: %w", path, err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("homestore: sync temporary file for %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("homestore: close temporary file for %s: %w", path, err)
	}
	if err := replaceFile(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("homestore: replace %s: %w", path, err)
	}
	if err := SyncDir(dir); err != nil {
		return fmt.Errorf("homestore: sync parent directory %s: %w", dir, err)
	}
	return nil
}
