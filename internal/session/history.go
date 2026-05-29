package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const metadataFile = "session.json"

const (
	KindPrimary = "primary"
	KindSide    = "side"
)

type metadata struct {
	Alias string `json:"alias,omitempty"`
	Kind  string `json:"kind,omitempty"`
}

type History struct {
	Sessions []Info `json:"sessions"`
	Active   *Info  `json:"active,omitempty"`
	Last     *Info  `json:"last,omitempty"`
}

func SetAlias(dir, alias string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	m, err := loadMetadata(dir)
	if err != nil {
		return err
	}
	m.Alias = alias
	return saveMetadata(dir, m)
}

func SetKind(dir, kind string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	m, err := loadMetadata(dir)
	if err != nil {
		return err
	}
	m.Kind = NormalizeKind(kind)
	return saveMetadata(dir, m)
}

func LoadAlias(dir string) (string, error) {
	m, err := loadMetadata(dir)
	if err != nil {
		return "", err
	}
	return m.Alias, nil
}

func LoadKind(dir string) (string, error) {
	m, err := loadMetadata(dir)
	if err != nil {
		return "", err
	}
	return m.Kind, nil
}

func loadMetadata(dir string) (metadata, error) {
	data, err := os.ReadFile(filepath.Join(dir, metadataFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return metadata{Kind: KindPrimary}, nil
		}
		return metadata{}, err
	}
	var m metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return metadata{}, err
	}
	m.Kind = NormalizeKind(m.Kind)
	return m, nil
}

func saveMetadata(dir string, m metadata) error {
	m.Kind = NormalizeKind(m.Kind)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(filepath.Join(dir, metadataFile), data, 0o644)
}

func NormalizeKind(kind string) string {
	switch kind {
	case KindSide:
		return KindSide
	case KindPrimary, "":
		return KindPrimary
	default:
		return KindPrimary
	}
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
	return normalizeHistory(h), nil
}

func RecordHistory(path string, info Info) error {
	if err := RecordSession(path, info); err != nil {
		return err
	}
	if NormalizeKind(info.Kind) == KindSide {
		return nil
	}
	return SetActive(path, info)
}

func RecordSession(path string, info Info) error {
	if path == "" {
		return nil
	}
	return withHistoryLock(path, func() error {
		h, err := LoadHistory(path)
		if err != nil {
			return err
		}
		info = normalizeInfo(info)
		upsertHistorySession(&h, info)
		if h.Active != nil && h.Active.ID == info.ID && info.Kind == KindPrimary {
			active := info
			active.Active = true
			h.Active = &active
		}
		return writeHistory(path, h)
	})
}

func SetActive(path string, info Info) error {
	if path == "" {
		return nil
	}
	info = normalizeInfo(info)
	if info.Kind != KindPrimary {
		return fmt.Errorf("session: cannot activate %s session %s", info.Kind, info.ID)
	}
	return withHistoryLock(path, func() error {
		h, err := LoadHistory(path)
		if err != nil {
			return err
		}
		upsertHistorySession(&h, info)
		active := info
		active.Active = true
		h.Active = &active
		return writeHistory(path, h)
	})
}

// Delete removes one on-disk session and drops its entry from history.
func Delete(root, historyPath, id string) error {
	dir, ok := sessionDir(root, id)
	if !ok {
		return os.ErrNotExist
	}
	if _, err := os.Stat(filepath.Join(dir, conversationFile)); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return RemoveHistory(historyPath, id)
}

// RemoveHistory drops id from history.json. Missing history is a no-op.
func RemoveHistory(path, id string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return withHistoryLock(path, func() error {
		h, err := LoadHistory(path)
		if err != nil {
			return err
		}
		kept := h.Sessions[:0]
		removedActive := h.Active != nil && h.Active.ID == id
		for _, info := range h.Sessions {
			if info.ID == id {
				continue
			}
			kept = append(kept, info)
		}
		h.Sessions = kept
		if removedActive {
			h.Active = newestHistoryPrimary(h.Sessions)
		}
		if len(h.Sessions) == 0 {
			h.Active = nil
		}
		return writeHistory(path, h)
	})
}

func upsertHistorySession(h *History, info Info) {
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
}

func writeHistory(path string, h History) error {
	h.Last = nil
	h = normalizeHistory(h)
	payload := struct {
		Active   *Info  `json:"active,omitempty"`
		Sessions []Info `json:"sessions"`
	}{
		Active:   h.Active,
		Sessions: h.Sessions,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, 0o644)
}

func normalizeHistory(h History) History {
	if h.Sessions == nil {
		h.Sessions = []Info{}
	}
	for i := range h.Sessions {
		h.Sessions[i] = normalizeInfo(h.Sessions[i])
	}
	if h.Active == nil && h.Last != nil {
		active := normalizeInfo(*h.Last)
		active.Kind = KindPrimary
		h.Active = &active
	}
	if h.Active != nil {
		active := normalizeInfo(*h.Active)
		active.Kind = KindPrimary
		active.Active = true
		h.Active = &active
	}
	activeID := ""
	if h.Active != nil {
		activeID = h.Active.ID
	}
	for i := range h.Sessions {
		h.Sessions[i].Active = activeID != "" && h.Sessions[i].ID == activeID
	}
	return h
}

func normalizeInfo(info Info) Info {
	info.Kind = NormalizeKind(info.Kind)
	return info
}

func newestHistorySession(infos []Info) *Info {
	if len(infos) == 0 {
		return nil
	}
	candidates := append([]Info(nil), infos...)
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].LastActiveAt.Equal(candidates[j].LastActiveAt) {
			return candidates[i].LastActiveAt.After(candidates[j].LastActiveAt)
		}
		return candidates[i].StartedAt.After(candidates[j].StartedAt)
	})
	return &candidates[0]
}

func newestHistoryPrimary(infos []Info) *Info {
	var primary []Info
	for _, info := range infos {
		info = normalizeInfo(info)
		if info.Kind == KindPrimary {
			primary = append(primary, info)
		}
	}
	active := newestHistorySession(primary)
	if active != nil {
		active.Active = true
	}
	return active
}

func sessionDir(root, id string) (string, bool) {
	if root == "" || id == "" || id == "." || id == ".." {
		return "", false
	}
	if filepath.Clean(id) != id || filepath.Base(id) != id {
		return "", false
	}
	return filepath.Join(root, id), true
}

func withHistoryLock(path string, fn func() error) error {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			f.Close()
			defer func() { _ = os.Remove(lockPath) }()
			return fn()
		}
		if !errors.Is(err, os.ErrExist) && !errors.Is(err, os.ErrPermission) {
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
