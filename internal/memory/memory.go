// Package memory implements Layer 1 (AGENTS.md hierarchy) and Layer 2
// (memory entries with frontmatter + a MEMORY.md index) of the Juex memory
// model.
//
// v0.1 deliberately uses substring matching for search and writes the index
// from scratch on every mutation. Embedding-based retrieval is intentionally
// out of scope — once entry counts exceed what the system prompt can hold
// we will reconsider.
package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/frontmatter"
	"github.com/juex-ai/juex/internal/tools"
)

const indexFile = "MEMORY.md"

var validTypes = map[string]bool{
	"user": true, "feedback": true, "project": true, "reference": true,
}

type Entry struct {
	Name        string
	Description string
	Type        string
	Body        string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Store is a Layer-2 memory store backed by `<dir>/<name>.md` files.
type Store struct {
	dir string
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

// Load returns every entry under the store directory, sorted by name.
// Missing directory yields an empty slice.
func (s *Store) Load() ([]Entry, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, e := range entries {
		if e.IsDir() || e.Name() == indexFile || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		entry, err := s.read(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Write persists e (creating the dir if needed) and rebuilds the index.
func (s *Store) Write(e Entry) error {
	if e.Name == "" {
		return fmt.Errorf("memory: entry name required")
	}
	if e.Type != "" && !validTypes[e.Type] {
		return fmt.Errorf("memory: invalid type %q (allowed: user, feedback, project, reference)", e.Type)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	now := time.Now().UTC()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now

	fields := map[string]string{
		"name":        e.Name,
		"description": e.Description,
		"type":        e.Type,
		"created_at":  e.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":  e.UpdatedAt.Format(time.RFC3339Nano),
	}
	doc := frontmatter.Format(fields,
		[]string{"name", "description", "type", "created_at", "updated_at"},
		e.Body)
	if err := os.WriteFile(filepath.Join(s.dir, sanitize(e.Name)+".md"), []byte(doc), 0o644); err != nil {
		return err
	}
	return s.rebuildIndex()
}

// Delete removes the entry's file (if present) and rebuilds the index.
func (s *Store) Delete(name string) error {
	path := filepath.Join(s.dir, sanitize(name)+".md")
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}
	return s.rebuildIndex()
}

// Search returns entries whose name, description, type, or body contain q
// (case-insensitive). v0.1: simple substring match, no ranking.
func (s *Store) Search(q string) ([]Entry, error) {
	all, err := s.Load()
	if err != nil {
		return nil, err
	}
	if q == "" {
		return all, nil
	}
	q = strings.ToLower(q)
	var hits []Entry
	for _, e := range all {
		if strings.Contains(strings.ToLower(e.Name), q) ||
			strings.Contains(strings.ToLower(e.Description), q) ||
			strings.Contains(strings.ToLower(e.Type), q) ||
			strings.Contains(strings.ToLower(e.Body), q) {
			hits = append(hits, e)
		}
	}
	return hits, nil
}

// PromptSection renders all loaded entries into a system-prompt section.
// v0.1 dumps every entry; if there are zero entries the section is empty.
func (s *Store) PromptSection() (string, error) {
	all, err := s.Load()
	if err != nil {
		return "", err
	}
	if len(all) == 0 {
		return "", nil
	}
	var sb strings.Builder
	sb.WriteString("## Memory\nPersisted facts about the user, project, and prior feedback.\n\n")
	for _, e := range all {
		fmt.Fprintf(&sb, "### %s (%s)\n%s\n\n", e.Name, e.Type, e.Description)
	}
	return sb.String(), nil
}

func (s *Store) read(path string) (Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	fm, err := frontmatter.Parse(string(data))
	if err != nil {
		return Entry{}, err
	}
	e := Entry{
		Name:        fm.Fields["name"],
		Description: fm.Fields["description"],
		Type:        fm.Fields["type"],
		Body:        fm.Body,
	}
	if t, err := time.Parse(time.RFC3339Nano, fm.Fields["created_at"]); err == nil {
		e.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, fm.Fields["updated_at"]); err == nil {
		e.UpdatedAt = t
	}
	if e.Name == "" {
		e.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	return e, nil
}

func (s *Store) rebuildIndex() error {
	all, err := s.Load()
	if err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("# Memory Index\n\n")
	for _, e := range all {
		fmt.Fprintf(&sb, "- [%s](%s.md) — %s — %s\n", e.Name, sanitize(e.Name), e.Type, e.Description)
	}
	return os.WriteFile(filepath.Join(s.dir, indexFile), []byte(sb.String()), 0o644)
}

// RegisterTools adds memory_write / memory_search / memory_delete to reg.
func (s *Store) RegisterTools(reg *tools.Registry) error {
	if err := reg.Register(tools.Tool{
		Name:        "memory_write",
		Description: "Persist a memory entry. Types: user, feedback, project, reference.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"type":        map[string]any{"type": "string", "enum": []string{"user", "feedback", "project", "reference"}},
				"body":        map[string]any{"type": "string"},
			},
			"required": []string{"name", "description", "type", "body"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			e := Entry{
				Name:        getStr(in, "name"),
				Description: getStr(in, "description"),
				Type:        getStr(in, "type"),
				Body:        getStr(in, "body"),
			}
			if err := s.Write(e); err != nil {
				return "", err
			}
			return "saved memory: " + e.Name, nil
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(tools.Tool{
		Name:        "memory_search",
		Description: "Substring search across memory entries (name/description/type/body). Empty query returns all.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			hits, err := s.Search(getStr(in, "query"))
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				return "(no matches)", nil
			}
			var sb strings.Builder
			for _, e := range hits {
				fmt.Fprintf(&sb, "## %s (%s)\n%s\n\n%s\n\n", e.Name, e.Type, e.Description, e.Body)
			}
			return sb.String(), nil
		},
	}); err != nil {
		return err
	}
	return reg.Register(tools.Tool{
		Name:        "memory_delete",
		Description: "Delete a memory entry by name.",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
			"required":   []string{"name"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			name := getStr(in, "name")
			if err := s.Delete(name); err != nil {
				return "", err
			}
			return "deleted memory: " + name, nil
		},
	})
}

// LoadAgentsMD returns concatenated AGENTS.md content with file headers.
// The global file is loaded first, followed by workspace directories in
// caller-provided order.
func LoadAgentsMD(globalPath string, dirs []string) string {
	var parts []string
	if globalPath != "" {
		if data, err := os.ReadFile(globalPath); err == nil && len(data) > 0 {
			parts = append(parts, fmt.Sprintf("# AGENTS.md (%s)\n\n%s", globalPath, string(data)))
		}
	}
	for _, d := range dirs {
		path := filepath.Join(d, "AGENTS.md")
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			parts = append(parts, fmt.Sprintf("# AGENTS.md (%s)\n\n%s", path, string(data)))
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func getStr(in map[string]any, k string) string {
	v, _ := in[k].(string)
	return v
}

// sanitize maps a memory name to a filesystem-safe slug.
func sanitize(name string) string {
	out := strings.Builder{}
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			out.WriteRune(r)
		default:
			out.WriteRune('_')
		}
	}
	return out.String()
}
