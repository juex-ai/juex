// Package memory owns durable Agent knowledge and its rebuildable index.
package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/homestore"
	"gopkg.in/yaml.v3"
)

const indexFile = "MEMORY.md"

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Entry is one authoritative Markdown document. The index never owns its body.
type Entry struct {
	Name        string    `yaml:"name" json:"name"`
	Description string    `yaml:"description" json:"description"`
	Type        string    `yaml:"type" json:"type"`
	CreatedAt   time.Time `yaml:"created_at" json:"created_at"`
	UpdatedAt   time.Time `yaml:"updated_at" json:"updated_at"`
	Body        string    `yaml:"-" json:"body"`
}

// Store is a view of one Agent's shared on-disk knowledge. Different Store
// instances coordinate through a stable transaction lock, without a cache.
type Store struct{ agentDir, dir string }

// NewStore is pure; catalog construction and disabled modules do no file work.
func NewStore(agentDir string) *Store {
	return &Store{agentDir: agentDir, dir: filepath.Join(agentDir, "modules", "memory")}
}

func (s *Store) Search(ctx context.Context, query string) ([]Entry, error) {
	if !utf8.ValidString(query) {
		return nil, fmt.Errorf("memory query must be valid UTF-8")
	}
	matcher, err := regexp.Compile("(?i)" + regexp.QuoteMeta(query))
	if err != nil {
		return nil, err
	}
	var hits []Entry
	err = s.withLock(ctx, false, func(root *os.Root) error {
		entries, err := loadEntries(ctx, root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if matcher.MatchString(strings.Join([]string{entry.Name, entry.Description, entry.Type, entry.Body}, "\n")) {
				hits = append(hits, entry)
			}
		}
		return nil
	})
	return hits, err
}

// Write returns the committed entry even if a later index or durability step
// fails, so callers can distinguish partial publication from an unchanged store.
func (s *Store) Write(ctx context.Context, entry Entry) (Entry, error) {
	if err := validateEntry(entry); err != nil {
		return Entry{}, err
	}
	var committed Entry
	err := s.withLock(ctx, true, func(root *os.Root) error {
		name := entry.Name + ".md"
		if err := regularEntryOrMissing(root, name); err != nil {
			return err
		}
		now := time.Now().UTC()
		entry.CreatedAt, entry.UpdatedAt = now, now
		if previous, err := readEntry(root, name); err == nil && !previous.CreatedAt.IsZero() {
			entry.CreatedAt = previous.CreatedAt
		}
		header, err := yaml.Marshal(entry)
		if err != nil {
			return err
		}
		document := append([]byte("---\n"), header...)
		document = append(document, []byte("---\n"+entry.Body)...)
		if err := ctx.Err(); err != nil {
			return err
		}
		publishErr := homestore.WriteFileAtomicExisting(filepath.Join(s.dir, name), document, 0600)
		if publishErr != nil && !homestore.ReplacementOccurred(publishErr) {
			return publishErr
		}
		committed = entry
		indexErr := s.rebuildIndex(ctx, root)
		if err := errors.Join(publishErr, indexErr); err != nil {
			return fmt.Errorf("memory %q saved; index or durability maintenance failed: %w", entry.Name, err)
		}
		return nil
	})
	return committed, err
}

func (s *Store) Delete(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return s.withLock(ctx, true, func(root *os.Root) error {
		file := name + ".md"
		if err := regularEntryOrMissing(root, file); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := root.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		syncErr := homestore.SyncDir(s.dir)
		indexErr := s.rebuildIndex(ctx, root)
		if err := errors.Join(syncErr, indexErr); err != nil {
			return fmt.Errorf("memory %q deleted; index or durability maintenance failed: %w", name, err)
		}
		return nil
	})
}

func (s *Store) RebuildIndex(ctx context.Context) error {
	return s.withLock(ctx, true, func(root *os.Root) error { return s.rebuildIndex(ctx, root) })
}

func (s *Store) rebuildIndex(ctx context.Context, root *os.Root) error {
	entries, err := loadEntries(ctx, root)
	if err != nil {
		return err
	}
	var index strings.Builder
	index.WriteString("# Memory Index\n\n")
	for _, entry := range entries {
		fmt.Fprintf(&index, "- [%s](%s.md) - %s - %s\n", entry.Name, entry.Name, entry.Type, entry.Description)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return homestore.WriteFileAtomicExisting(filepath.Join(s.dir, indexFile), []byte(index.String()), 0600)
}

// No transaction removes the lock file: replacing its inode could admit two
// owners simultaneously. LockTry avoids an uncancellable OS lock wait.
func (s *Store) withLock(ctx context.Context, create bool, fn func(*os.Root) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || !filepath.IsAbs(s.agentDir) {
		return fmt.Errorf("memory requires an absolute Agent state directory")
	}
	agent, err := os.OpenRoot(s.agentDir)
	if err != nil {
		return err
	}
	defer func() { _ = agent.Close() }()
	for _, name := range []string{"modules", filepath.Join("modules", "memory")} {
		info, err := agent.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			if !create {
				return nil
			}
			if err = agent.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = agent.Lstat(name)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("memory directory %q is not a physical directory", name)
		}
	}
	root, err := agent.OpenRoot(filepath.Join("modules", "memory"))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := regularEntryOrMissing(root, ".lock"); err != nil {
		return err
	}
	var lock *homestore.Lock
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		lock, err = homestore.AcquireLock(filepath.Join(s.dir, ".lock"), homestore.LockTry)
		if err == nil {
			break
		}
		if !errors.Is(err, homestore.ErrLockBusy) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer func() { _ = lock.Close() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(root)
}

func regularEntryOrMissing(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("memory file %q is not a regular file", name)
	}
	return nil
}

func loadEntries(ctx context.Context, root *os.Root) ([]Entry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	files, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	var entries []Entry
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := file.Name()
		if !strings.HasSuffix(name, ".md") || strings.EqualFold(name, indexFile) || !file.Type().IsRegular() {
			continue
		}
		entry, err := readEntry(root, name)
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func readEntry(root *os.Root, name string) (Entry, error) {
	if err := regularEntryOrMissing(root, name); err != nil {
		return Entry{}, err
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return Entry{}, err
	}
	if !utf8.Valid(data) || !strings.HasPrefix(string(data), "---\n") {
		return Entry{}, fmt.Errorf("invalid Memory frontmatter in %s", name)
	}
	header, body, ok := strings.Cut(string(data[4:]), "\n---\n")
	if !ok {
		return Entry{}, fmt.Errorf("missing Memory frontmatter end in %s", name)
	}
	var entry Entry
	if err := yaml.Unmarshal([]byte(header), &entry); err != nil {
		return Entry{}, err
	}
	entry.Body = body
	if entry.Name != strings.TrimSuffix(name, ".md") {
		return Entry{}, fmt.Errorf("memory name does not match %s", name)
	}
	if err := validateEntry(entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func validateName(name string) error {
	if !safeName.MatchString(name) || strings.EqualFold(name, "MEMORY") {
		return fmt.Errorf("memory name must be a safe 1-128 character slug; MEMORY is reserved")
	}
	return nil
}

func validateEntry(entry Entry) error {
	if err := validateName(entry.Name); err != nil {
		return err
	}
	switch entry.Type {
	case "user", "feedback", "project", "reference":
	default:
		return fmt.Errorf("memory type must be user, feedback, project, or reference")
	}
	if strings.TrimSpace(entry.Description) == "" || !utf8.ValidString(entry.Description) || strings.ContainsAny(entry.Description, "\n\r\v\f\x1c\x1d\x1e\u0085\u2028\u2029") {
		return fmt.Errorf("memory description must be one non-empty UTF-8 line")
	}
	if !utf8.ValidString(entry.Body) {
		return fmt.Errorf("memory body must be valid UTF-8")
	}
	return nil
}
