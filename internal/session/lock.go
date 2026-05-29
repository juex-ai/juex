package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const sessionLockFile = "session.lock"

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
	path := filepath.Join(dir, sessionLockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
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

func readLockInfo(path string) LockInfo {
	data, err := os.ReadFile(path)
	if err != nil {
		return LockInfo{}
	}
	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return LockInfo{}
	}
	return info
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
