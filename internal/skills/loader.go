// Package skills loads SKILL.md files from project- and user-scoped
// `.agents/skills/<name>/` directories.
//
// v0.1 behaviour:
//   - all loaded skill descriptions are concatenated into the system prompt
//   - the full body is loaded lazily via the `read_skill` tool
//   - precedence: project-scoped > user-scoped (project overrides user by name)
package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/frontmatter"
	"github.com/juex-ai/juex/internal/tools"
)

type Skill struct {
	Name        string
	Description string
	Type        string
	Body        string
	Source      string // "project" | "user"
	Path        string // SKILL.md absolute path
}

type Loader struct {
	dirs   []string // ordered: lowest precedence first
	skills map[string]Skill
}

// NewLoader creates a loader. dirs are scanned in the supplied order; later
// entries override earlier entries with the same skill name.
func NewLoader(dirs ...string) *Loader {
	return &Loader{dirs: dirs, skills: make(map[string]Skill)}
}

// Load scans all configured directories for `<name>/SKILL.md`.
// Errors on individual skills are reported but do not abort the load.
func (l *Loader) Load() error {
	l.skills = make(map[string]Skill)
	for _, dir := range l.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		source := sourceLabel(dir, l.dirs)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name(), "SKILL.md")
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, err := frontmatter.Parse(string(data))
			if err != nil {
				continue
			}
			name := fm.Fields["name"]
			if name == "" {
				name = e.Name()
			}
			l.skills[name] = Skill{
				Name:        name,
				Description: fm.Fields["description"],
				Type:        fm.Fields["type"],
				Body:        fm.Body,
				Source:      source,
				Path:        path,
			}
		}
	}
	return nil
}

// All returns every loaded skill, sorted by name.
func (l *Loader) All() []Skill {
	out := make([]Skill, 0, len(l.skills))
	for _, s := range l.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (l *Loader) Get(name string) (Skill, bool) {
	s, ok := l.skills[name]
	return s, ok
}

// PromptSection renders the skills index for the system prompt.
func (l *Loader) PromptSection() string {
	all := l.All()
	if len(all) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Available Skills\n")
	sb.WriteString("Skills are reusable behaviours. Call `read_skill` with the skill name to load the full instructions before applying it.\n\n")
	for _, s := range all {
		fmt.Fprintf(&sb, "- **%s** (%s) — %s\n", s.Name, s.Source, s.Description)
	}
	return sb.String()
}

// RegisterTool adds a `read_skill` tool that returns the full body of a skill.
func (l *Loader) RegisterTool(reg *tools.Registry) error {
	return reg.Register(tools.Tool{
		Name:        "read_skill",
		Description: "Return the full markdown body of a skill by name. Use after deciding a listed skill applies.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required": []string{"name"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			name, _ := in["name"].(string)
			s, ok := l.Get(name)
			if !ok {
				return "", fmt.Errorf("read_skill: unknown skill %q", name)
			}
			return s.Body, nil
		},
	})
}

func sourceLabel(dir string, all []string) string {
	if len(all) == 0 {
		return "skill"
	}
	if dir == all[len(all)-1] {
		return "project"
	}
	return "user"
}
