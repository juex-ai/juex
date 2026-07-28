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

const (
	KindPrimary = "primary"
	KindSide    = "side"
)

var (
	historyLockTimeout    = 35 * time.Second
	historyLockStaleAfter = 30 * time.Second
	historyLockPoll       = 10 * time.Millisecond

	// ErrCannotActivateSide is returned when a caller tries to make a side
	// session the workspace active session.
	ErrCannotActivateSide = errors.New("session: side sessions cannot become active")

	// ErrSessionTimeUnavailable identifies pre-release session metadata that
	// does not contain the session-owned timestamps required by this version.
	ErrSessionTimeUnavailable = errors.New("session: owned time is unavailable")
)

type metadata struct {
	Alias          string `json:"alias,omitempty"`
	Kind           string `json:"kind,omitempty"`
	StartedAtMS    int64  `json:"started_at_ms"`
	LastActiveAtMS int64  `json:"last_active_at_ms"`
}

type History struct {
	Sessions []Info
	Active   *Info
}

type historyFile struct {
	ActiveID string         `json:"active_id,omitempty"`
	Sessions []historyEntry `json:"sessions"`
}

type historyEntry struct {
	ID         string                `json:"id"`
	Turns      int                   `json:"turns"`
	Preview    string                `json:"preview"`
	Transcript transcriptFingerprint `json:"transcript"`
}

// DeletePlan captures the validated inputs needed to remove one session.
type DeletePlan struct {
	dir              string
	historyPath      string
	id               string
	fallbackActiveID string
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
	path := filepath.Join(dir, metadataFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return metadata{}, fmt.Errorf("%w: %s is missing", ErrSessionTimeUnavailable, path)
		}
		return metadata{}, err
	}
	var m metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return metadata{}, err
	}
	m.Kind = NormalizeKind(m.Kind)
	if err := validateMetadata(m); err != nil {
		return metadata{}, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func saveMetadata(dir string, m metadata) error {
	m.Kind = NormalizeKind(m.Kind)
	if err := validateMetadata(m); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(filepath.Join(dir, metadataFile), data, 0o644)
}

func validateMetadata(m metadata) error {
	if m.StartedAtMS <= 0 || m.LastActiveAtMS <= 0 {
		return fmt.Errorf(
			"%w: started_at_ms and last_active_at_ms must be positive",
			ErrSessionTimeUnavailable,
		)
	}
	if m.LastActiveAtMS < m.StartedAtMS {
		return fmt.Errorf(
			"%w: last_active_at_ms must not precede started_at_ms",
			ErrSessionTimeUnavailable,
		)
	}
	return nil
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
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return History{Sessions: []Info{}}, nil
		}
		return History{}, err
	}
	var history History
	err := withHistoryLock(path, func() error {
		var err error
		history, err = loadHistoryFile(path)
		return err
	})
	return history, err
}

func loadHistoryFile(path string) (History, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return History{Sessions: []Info{}}, nil
		}
		return History{}, err
	}
	var stored historyFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return History{}, err
	}
	h := History{Sessions: make([]Info, 0, len(stored.Sessions))}
	for _, entry := range stored.Sessions {
		if entry.ID == "" {
			continue
		}
		h.Sessions = append(h.Sessions, Info{
			ID:         entry.ID,
			Turns:      entry.Turns,
			Preview:    entry.Preview,
			transcript: entry.Transcript,
		})
	}
	if stored.ActiveID != "" {
		active := Info{
			ID:     stored.ActiveID,
			Kind:   KindPrimary,
			Active: true,
		}
		h.Active = &active
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
		h, err := loadHistoryFile(path)
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
	_, err := activateInfo(path, info)
	return err
}

// Activate loads id from root and records it as the active primary session.
func Activate(root, historyPath, id string) (Info, error) {
	dir, ok := sessionDir(root, id)
	if !ok {
		return Info{}, os.ErrNotExist
	}
	info, _, err := LoadInfo(dir)
	if err != nil {
		return Info{}, err
	}
	return activateInfo(historyPath, info)
}

func activateInfo(path string, info Info) (Info, error) {
	info = normalizeInfo(info)
	if info.Kind != KindPrimary {
		return Info{}, fmt.Errorf("%w: %s", ErrCannotActivateSide, info.ID)
	}
	active := info
	active.Active = true
	if path == "" {
		return active, nil
	}
	if err := withHistoryLock(path, func() error {
		h, err := loadHistoryFile(path)
		if err != nil {
			return err
		}
		upsertHistorySession(&h, info)
		h.Active = &active
		return writeHistory(path, h)
	}); err != nil {
		return Info{}, err
	}
	return active, nil
}

// MarkActive returns copies of infos with normalized Kind and Active fields.
func MarkActive(path string, infos []Info) ([]Info, error) {
	if path == "" {
		return markActiveWithHistory(History{}, infos), nil
	}
	h, err := LoadHistory(path)
	if err != nil {
		return nil, err
	}
	return markActiveWithHistory(h, infos), nil
}

// MarkActiveInfo returns info with normalized Kind and Active fields.
func MarkActiveInfo(path string, info Info) (Info, error) {
	infos, err := MarkActive(path, []Info{info})
	if err != nil {
		return Info{}, err
	}
	if len(infos) == 0 {
		return normalizeInfo(info), nil
	}
	return infos[0], nil
}

func markActiveWithHistory(h History, infos []Info) []Info {
	h = normalizeHistory(h)
	activeID := ""
	if h.Active != nil {
		activeID = h.Active.ID
	}
	out := append([]Info(nil), infos...)
	for i := range out {
		out[i] = normalizeInfo(out[i])
		out[i].Active = activeID != "" && out[i].ID == activeID
	}
	return out
}

// PrepareDelete validates one on-disk session and any active-session fallback
// before callers stop a live runtime or remove persistent data.
func PrepareDelete(root, historyPath, id string) (*DeletePlan, error) {
	dir, ok := sessionDir(root, id)
	if !ok {
		return nil, os.ErrNotExist
	}
	if _, err := os.Stat(filepath.Join(dir, conversationFile)); err != nil {
		return nil, err
	}
	removedActive := false
	fallbackActiveID := ""
	if historyPath != "" {
		h, err := LoadHistory(historyPath)
		if err != nil {
			return nil, err
		}
		removedActive = h.Active != nil && h.Active.ID == id
		if removedActive {
			fallbackActiveID, err = newestPrimarySessionID(root, id)
			if err != nil {
				return nil, err
			}
		}
	}
	return &DeletePlan{
		dir:              dir,
		historyPath:      historyPath,
		id:               id,
		fallbackActiveID: fallbackActiveID,
	}, nil
}

// Commit removes the session represented by a validated delete plan.
func (p *DeletePlan) Commit() error {
	if p == nil {
		return os.ErrInvalid
	}
	if err := os.RemoveAll(p.dir); err != nil {
		return err
	}
	return removeHistory(p.historyPath, p.id, p.fallbackActiveID)
}

// Delete removes one on-disk session and drops its entry from history.
func Delete(root, historyPath, id string) error {
	plan, err := PrepareDelete(root, historyPath, id)
	if err != nil {
		return err
	}
	return plan.Commit()
}

// RemoveHistory drops id from history.json. Missing history is a no-op.
func RemoveHistory(path, id string) error {
	return removeHistory(path, id, "")
}

func removeHistory(path, id, fallbackActiveID string) error {
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
		h, err := loadHistoryFile(path)
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
			h.Active = nil
			if fallbackActiveID != "" {
				active := Info{
					ID:     fallbackActiveID,
					Kind:   KindPrimary,
					Active: true,
				}
				h.Active = &active
			}
		}
		return writeHistory(path, h)
	})
}

func upsertHistorySession(h *History, info Info) {
	sessions := make([]Info, 0, len(h.Sessions)+1)
	sessions = append(sessions, info)
	for _, recorded := range h.Sessions {
		if recorded.ID != info.ID {
			sessions = append(sessions, recorded)
		}
	}
	h.Sessions = sessions
}

func writeHistory(path string, h History) error {
	h = normalizeHistory(h)
	payload := historyFile{
		Sessions: make([]historyEntry, 0, len(h.Sessions)),
	}
	if h.Active != nil {
		payload.ActiveID = h.Active.ID
	}
	for _, info := range h.Sessions {
		payload.Sessions = append(payload.Sessions, historyEntry{
			ID:         info.ID,
			Turns:      info.Turns,
			Preview:    info.Preview,
			Transcript: info.transcript,
		})
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

func sessionDir(root, id string) (string, bool) {
	if root == "" || id == "" || id == "." || id == ".." {
		return "", false
	}
	if filepath.Clean(id) != id || filepath.Base(id) != id {
		return "", false
	}
	return filepath.Join(root, id), true
}

func newestPrimarySessionID(root, excludeID string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	var newestID string
	var newest metadata
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == excludeID {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if !HasConversation(dir) {
			continue
		}
		meta, err := loadMetadata(dir)
		if err != nil {
			if errors.Is(err, ErrSessionTimeUnavailable) {
				continue
			}
			return "", err
		}
		if meta.Kind != KindPrimary {
			continue
		}
		if newestID == "" ||
			meta.LastActiveAtMS > newest.LastActiveAtMS ||
			(meta.LastActiveAtMS == newest.LastActiveAtMS && meta.StartedAtMS > newest.StartedAtMS) {
			newestID = entry.Name()
			newest = meta
		}
	}
	return newestID, nil
}

func withHistoryLock(path string, fn func() error) error {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	deadline := time.Now().Add(historyLockTimeout)
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
		if st, statErr := os.Stat(lockPath); statErr == nil && time.Since(st.ModTime()) > historyLockStaleAfter {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("session: timed out waiting for history lock %s", lockPath)
		}
		time.Sleep(historyLockPoll)
	}
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
	return replaceFile(tmpName, path)
}
