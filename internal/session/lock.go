package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const sessionLockFile = "session.lock"
const sessionLockGuardFile = "session.lock.guard"

const unreadableLockStaleAfter = 5 * time.Second
const processStartTolerance = 2 * time.Second

type Lock struct {
	path string
}

type LockInfo struct {
	PID       int       `json:"pid"`
	Mode      string    `json:"mode"`
	SessionID string    `json:"session_id"`
	StartedAt time.Time `json:"started_at"`
}

type LockError struct {
	Path   string
	Holder LockInfo
}

func (e *LockError) Error() string {
	if e == nil {
		return ""
	}
	if e.Holder.PID == 0 {
		return "session is locked: " + e.Path
	}
	return fmt.Sprintf("session %s is locked by pid %d (%s)", e.Holder.SessionID, e.Holder.PID, e.Holder.Mode)
}

func AcquireSessionLock(dir, mode string) (*Lock, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	guard, err := acquireLockGuard(filepath.Join(dir, sessionLockGuardFile))
	if err != nil {
		return nil, err
	}
	defer func() { _ = guard.Close() }()

	path := filepath.Join(dir, sessionLockFile)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if os.IsExist(err) {
				if attempt == 0 {
					cleared, clearErr := clearDeadProcessLock(path)
					if clearErr != nil {
						return nil, clearErr
					}
					if cleared {
						continue
					}
				}
				return nil, &LockError{Path: path, Holder: readLockInfo(path)}
			}
			return nil, err
		}
		info := LockInfo{
			PID:       os.Getpid(),
			Mode:      mode,
			SessionID: filepath.Base(dir),
			StartedAt: time.Now().UTC(),
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			f.Close()
			_ = os.Remove(path)
			return nil, err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		return &Lock{path: path}, nil
	}
	return nil, &LockError{Path: path, Holder: readLockInfo(path)}
}

func clearDeadProcessLock(path string) (bool, error) {
	info, ok := readLockInfoFile(path)
	if !ok || info.PID <= 0 {
		return clearUnreadableLockIfStale(path)
	}
	alive, err := processExists(info.PID)
	if err != nil {
		return false, nil
	}
	if !alive {
		return removeLockFile(path)
	}
	if info.StartedAt.IsZero() {
		return false, nil
	}
	startedAt, err := processStartedAt(info.PID)
	if err != nil {
		return false, nil
	}
	if startedAt.After(info.StartedAt.Add(processStartTolerance)) {
		return removeLockFile(path)
	}
	return false, nil
}

func clearUnreadableLockIfStale(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	if time.Since(info.ModTime()) <= unreadableLockStaleAfter {
		return false, nil
	}
	return removeLockFile(path)
}

func removeLockFile(path string) (bool, error) {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return true, nil
}

func readLockInfo(path string) LockInfo {
	info, _ := readLockInfoFile(path)
	return info
}

func readLockInfoFile(path string) (LockInfo, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LockInfo{}, false
	}
	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return LockInfo{}, false
	}
	return info, true
}

func (l *Lock) Close() error {
	if l == nil || l.path == "" {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
