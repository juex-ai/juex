package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const metadataFile = "session.json"

type metadata struct {
	Alias string `json:"alias,omitempty"`
}

type History struct {
	Sessions []Info `json:"sessions"`
	Last     *Info  `json:"last"`
}

func SetAlias(dir, alias string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(metadata{Alias: alias}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(filepath.Join(dir, metadataFile), data, 0o644)
}

func LoadAlias(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, metadataFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	var m metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return "", err
	}
	return m.Alias, nil
}

func LoadHistory(path string) (History, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return History{Sessions: []Info{}}, nil
		}
		return History{}, err
	}
	var h History
	if err := json.Unmarshal(data, &h); err != nil {
		return History{}, err
	}
	if h.Sessions == nil {
		h.Sessions = []Info{}
	}
	return h, nil
}

func RecordHistory(path string, info Info) error {
	if path == "" {
		return nil
	}
	return withHistoryLock(path, func() error {
		h, err := LoadHistory(path)
		if err != nil {
			return err
		}
		replaced := false
		for i := range h.Sessions {
			if h.Sessions[i].ID == info.ID {
				h.Sessions[i] = info
				replaced = true
				break
			}
		}
		if !replaced {
			h.Sessions = append(h.Sessions, info)
		}
		infoCopy := info
		h.Last = &infoCopy
		data, err := json.MarshalIndent(h, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		return atomicWriteFile(path, data, 0o644)
	})
}

func withHistoryLock(path string, fn func() error) error {
	lockPath := path + ".lock"
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			f.Close()
			defer func() { _ = os.Remove(lockPath) }()
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if st, statErr := os.Stat(lockPath); statErr == nil && time.Since(st.ModTime()) > 30*time.Second {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("session: timed out waiting for history lock %s", lockPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
