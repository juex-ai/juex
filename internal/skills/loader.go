// Package skills loads SKILL.md files from user, extension, and project
// resource directories.
//
// Behaviour:
//   - the system prompt's "Available Skills" section lists every loaded
//     skill with its absolute path and one-line description
//   - the model loads a skill's full body by calling the standard `read`
//     builtin against the path printed in that section — there is no
//     dedicated `read_skill` tool (one fewer thing for the model to
//     hallucinate; one less surface area to maintain)
//   - precedence: project-scoped > user-scoped for non-extension resources
//   - extension skill names must be unique and reject collisions instead of
//     silently overriding another resource
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/frontmatter"
)

type Skill struct {
	Name        string
	Description string
	Type        string
	Body        string
	Source      string // "project" | "user" | "ext:<name>"
	Path        string // SKILL.md absolute path

	strictConflicts bool
}

type Dir struct {
	Path            string
	Source          string
	StrictConflicts bool
}

type Loader struct {
	dirs   []Dir // ordered: lowest precedence first
	skills map[string]Skill
}

// NewLoader creates a loader. dirs are scanned in the supplied order; later
// entries override earlier entries with the same skill name.
func NewLoader(dirs ...string) *Loader {
	refs := make([]Dir, 0, len(dirs))
	for _, dir := range dirs {
		refs = append(refs, Dir{Path: dir, Source: sourceLabel(dir, dirs)})
	}
	return NewLoaderFromDirs(refs)
}

func NewLoaderFromDirs(dirs []Dir) *Loader {
	refs := make([]Dir, 0, len(dirs))
	for _, dir := range dirs {
		if dir.Source == "" {
			dir.Source = "skill"
		}
		refs = append(refs, dir)
	}
	return &Loader{dirs: refs, skills: make(map[string]Skill)}
}

// Load scans all configured directories for `<name>/SKILL.md`.
// Errors on individual skills are reported but do not abort the load.
func (l *Loader) Load() error {
	l.skills = make(map[string]Skill)
	for _, dir := range l.dirs {
		entries, err := os.ReadDir(dir.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, e := range entries {
			skillDir, ok := skillDirPath(dir.Path, e)
			if !ok {
				continue
			}
			path := filepath.Join(skillDir, "SKILL.md")
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
			if existing, ok := l.skills[name]; ok && (existing.strictConflicts || dir.StrictConflicts) {
				return fmt.Errorf("skills: duplicate skill %q from %s and %s", name, existing.Source, dir.Source)
			}
			l.skills[name] = Skill{
				Name:            name,
				Description:     fm.Fields["description"],
				Type:            fm.Fields["type"],
				Body:            fm.Body,
				Source:          dir.Source,
				Path:            path,
				strictConflicts: dir.StrictConflicts,
			}
		}
	}
	return nil
}

func skillDirPath(root string, e os.DirEntry) (string, bool) {
	path := filepath.Join(root, e.Name())
	if e.IsDir() {
		return path, true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return path, true
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

// PromptSection renders the skills index for the system prompt. Each skill
// is listed with its absolute path so the model can `read` it directly —
// no dedicated tool needed.
func (l *Loader) PromptSection() string {
	all := l.All()
	if len(all) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Available Skills\n")
	sb.WriteString("Reusable instructions stored as markdown files. To apply a skill, " +
		"call `read` against the file path shown below to load its full body, then follow it.\n\n")
	for _, s := range all {
		fmt.Fprintf(&sb, "- **%s** (%s) — `%s` — %s\n", s.Name, s.Source, s.Path, s.Description)
	}
	return sb.String()
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
