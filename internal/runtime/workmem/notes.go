package workmem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/thread"
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
	ThreadDir string
	mu        sync.Mutex
}

func NewNotesStore(threadDir string) *NotesStore {
	return &NotesStore{ThreadDir: threadDir}
}

func (s *NotesStore) Clear() error {
	if s == nil || strings.TrimSpace(s.ThreadDir) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.ThreadDir, NotesFileName)
	if err := clearFile(path); err != nil {
		return fmt.Errorf("notes clear: %w", err)
	}
	return nil
}

func (s *NotesStore) StageClearForContextRenewal(generationID string) (finalize, rollback func() error, err error) {
	if s == nil || strings.TrimSpace(s.ThreadDir) == "" {
		return func() error { return nil }, func() error { return nil }, nil
	}
	s.mu.Lock()
	clear, stageErr := thread.StageContextRenewalFileClear(filepath.Join(s.ThreadDir, NotesFileName), generationID)
	s.mu.Unlock()
	finalize, rollback, err = clear.Finalize, clear.Rollback, stageErr
	if err != nil {
		return nil, nil, fmt.Errorf("notes stage clear: %w", err)
	}
	wrap := func(action func() error, label string) func() error {
		return func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if err := action(); err != nil {
				return fmt.Errorf("notes %s: %w", label, err)
			}
			return nil
		}
	}
	return wrap(finalize, "finalize clear"), wrap(rollback, "restore"), nil
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
	if s == nil || strings.TrimSpace(s.ThreadDir) == "" {
		return NotesSnapshot{}, fmt.Errorf("notes store requires a thread directory")
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
	path := filepath.Join(s.ThreadDir, NotesFileName)
	if err := replaceFileAtomic(path, []byte(content), 0o600); err != nil {
		return NotesSnapshot{}, fmt.Errorf("notes replace: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return NotesSnapshot{}, fmt.Errorf("notes stat: %w", err)
	}
	return NotesSnapshot{Content: content, UpdatedAt: info.ModTime().UTC()}, nil
}

func (s *NotesStore) snapshotLocked() (NotesSnapshot, bool, error) {
	if strings.TrimSpace(s.ThreadDir) == "" {
		return NotesSnapshot{}, false, nil
	}
	path := filepath.Join(s.ThreadDir, NotesFileName)
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
