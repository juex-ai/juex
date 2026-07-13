package workmem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	NotesFileName      = "notes.md"
	MaxNotesCharacters = 2048
)

type NotesSnapshot struct {
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type NotesStore struct {
	SessionDir string
	mu         sync.Mutex
}

func NewNotesStore(sessionDir string) *NotesStore {
	return &NotesStore{SessionDir: sessionDir}
}

func (s *NotesStore) Snapshot() (NotesSnapshot, error) {
	if s == nil {
		return NotesSnapshot{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, _, err := s.snapshotLocked()
	return snapshot, err
}

func (s *NotesStore) StatusSnapshot() (*NotesSnapshot, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, present, err := s.snapshotLocked()
	if err != nil || !present {
		return nil, err
	}
	return &snapshot, nil
}

func (s *NotesStore) Update(content string) (NotesSnapshot, error) {
	if s == nil || strings.TrimSpace(s.SessionDir) == "" {
		return NotesSnapshot{}, fmt.Errorf("notes store requires a session directory")
	}
	if err := validateNotesContent(content); err != nil {
		return NotesSnapshot{}, err
	}
	content = redactWorkmemText(content)
	if err := validateNotesContent(content); err != nil {
		return NotesSnapshot{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.SessionDir, 0o700); err != nil {
		return NotesSnapshot{}, fmt.Errorf("notes mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(s.SessionDir, ".notes.md-*")
	if err != nil {
		return NotesSnapshot{}, fmt.Errorf("notes create temp: %w", err)
	}
	tmpPath := tmp.Name()
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return NotesSnapshot{}, fmt.Errorf("notes chmod temp: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return NotesSnapshot{}, fmt.Errorf("notes write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return NotesSnapshot{}, fmt.Errorf("notes sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return NotesSnapshot{}, fmt.Errorf("notes close temp: %w", err)
	}
	path := filepath.Join(s.SessionDir, NotesFileName)
	if err := os.Rename(tmpPath, path); err != nil {
		return NotesSnapshot{}, fmt.Errorf("notes replace: %w", err)
	}
	keepTemp = false
	info, err := os.Stat(path)
	if err != nil {
		return NotesSnapshot{}, fmt.Errorf("notes stat: %w", err)
	}
	return NotesSnapshot{Content: content, UpdatedAt: info.ModTime().UTC()}, nil
}

func (s *NotesStore) snapshotLocked() (NotesSnapshot, bool, error) {
	if strings.TrimSpace(s.SessionDir) == "" {
		return NotesSnapshot{}, false, nil
	}
	path := filepath.Join(s.SessionDir, NotesFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NotesSnapshot{}, false, nil
		}
		return NotesSnapshot{}, false, fmt.Errorf("notes read: %w", err)
	}
	content := string(data)
	if err := validateNotesContent(content); err != nil {
		return NotesSnapshot{}, false, fmt.Errorf("notes read: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return NotesSnapshot{}, false, fmt.Errorf("notes stat: %w", err)
	}
	return NotesSnapshot{
		Content:   redactWorkmemText(content),
		UpdatedAt: info.ModTime().UTC(),
	}, true, nil
}

func (s NotesSnapshot) RenderProviderContext() (string, bool) {
	content := strings.TrimSpace(s.Content)
	if content == "" {
		return "", false
	}
	return "Current working notes (model-owned; rewrite with update_notes):\n" + content, true
}

func validateNotesContent(content string) error {
	if !utf8.ValidString(content) {
		return fmt.Errorf("notes content must be valid UTF-8")
	}
	count := utf8.RuneCountInString(content)
	if count > MaxNotesCharacters {
		return fmt.Errorf("notes content is %d characters; maximum is %d; shorten the notes and move long material to scratchpad files", count, MaxNotesCharacters)
	}
	return nil
}
