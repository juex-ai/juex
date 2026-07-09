// Package skills loads SKILL.md files from user, extension, and project
// resource directories.
//
// Behaviour:
//   - the system prompt's "Available Skills" section lists a compact,
//     budgeted skill index
//   - the model loads a skill's full body by calling the runtime-provided
//     `skill_load` tool with the skill name, or uses `skill_search` when the
//     prompt budget omitted a lower-priority entry
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

type Policy struct {
	Include           []string
	Exclude           []string
	PromptBudgetChars int
}

type LoaderOptions struct {
	Policy Policy
}

type FilteredSkill struct {
	Name   string
	Source string
	Reason string
}

type PromptBudgetReport struct {
	BudgetChars int
	UsedChars   int
	Compacted   bool
	Omitted     []PromptOmittedSkill
}

type PromptOmittedSkill struct {
	Name   string
	Source string
	Reason string
}

const skillPromptHeader = "## Available Skills\n" +
	"Reusable instructions stored as markdown files. To apply a skill, call `skill_load` with its name to load the full SKILL.md. Use `skill_search` when unsure or when a prompt budget omitted lower-priority skills.\n\n"

const compactSkillPromptHeader = "## Available Skills\n" +
	"Use `skill_search` to find skills and `skill_load` to read a full SKILL.md.\n\n"

const minimalSkillPromptHeader = "Skills: `skill_search`, `skill_load`.\n"

type Dir struct {
	Path            string
	Source          string
	StrictConflicts bool
}

type Loader struct {
	dirs     []Dir // ordered: lowest precedence first
	policy   Policy
	skills   map[string]Skill
	filtered []FilteredSkill
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
	return NewLoaderFromDirsWithOptions(dirs, LoaderOptions{})
}

func NewLoaderFromDirsWithOptions(dirs []Dir, opts LoaderOptions) *Loader {
	refs := make([]Dir, 0, len(dirs))
	for _, dir := range dirs {
		if dir.Source == "" {
			dir.Source = "skill"
		}
		refs = append(refs, dir)
	}
	return &Loader{dirs: refs, policy: normalizePolicy(opts.Policy), skills: make(map[string]Skill)}
}

// Load scans all configured directories for `<name>/SKILL.md`.
// Errors on individual skills are reported but do not abort the load.
func (l *Loader) Load() error {
	l.skills = make(map[string]Skill)
	l.filtered = nil
	for _, dir := range l.dirs {
		if strings.TrimSpace(dir.Path) == "" {
			continue
		}
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
	l.applyNameFilters()
	return nil
}

func (l *Loader) applyNameFilters() {
	include := nameSet(l.policy.Include)
	exclude := nameSet(l.policy.Exclude)
	for name, skill := range l.skills {
		if len(include) > 0 {
			if _, ok := include[name]; !ok {
				l.filtered = append(l.filtered, FilteredSkill{Name: name, Source: skill.Source, Reason: "not included"})
				delete(l.skills, name)
			}
			continue
		}
		if _, ok := exclude[name]; ok {
			l.filtered = append(l.filtered, FilteredSkill{Name: name, Source: skill.Source, Reason: "excluded"})
			delete(l.skills, name)
		}
	}
	sort.Slice(l.filtered, func(i, j int) bool {
		if l.filtered[i].Name != l.filtered[j].Name {
			return l.filtered[i].Name < l.filtered[j].Name
		}
		return l.filtered[i].Source < l.filtered[j].Source
	})
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

func (l *Loader) Search(query string, limit int) []Skill {
	all := l.All()
	query = strings.ToLower(strings.TrimSpace(query))
	var out []Skill
	for _, skill := range all {
		if query != "" {
			haystack := strings.ToLower(skill.Name + "\n" + skill.Description + "\n" + skill.Type + "\n" + skill.Source)
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		out = append(out, skill)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (l *Loader) Filtered() []FilteredSkill {
	return append([]FilteredSkill(nil), l.filtered...)
}

func (l *Loader) Policy() Policy {
	return clonePolicy(l.policy)
}

func (l *Loader) PromptReport() PromptBudgetReport {
	_, report := l.promptSectionAndReport()
	report.Omitted = append([]PromptOmittedSkill(nil), report.Omitted...)
	return report
}

// PromptSection renders the skills index for the system prompt. Each skill
// is listed compactly; full skill bodies are loaded on demand with skill_load.
func (l *Loader) PromptSection() string {
	section, _ := l.promptSectionAndReport()
	return section
}

func (l *Loader) promptSectionAndReport() (string, PromptBudgetReport) {
	all := l.All()
	if len(all) == 0 {
		return "", PromptBudgetReport{}
	}
	section, report := renderPromptSection(all, l.policy.PromptBudgetChars)
	return section, report
}

func renderPromptSection(all []Skill, budgetChars int) (string, PromptBudgetReport) {
	renderLines := func(skills []Skill, descLimit int) []string {
		lines := make([]string, 0, len(skills))
		for _, s := range skills {
			lines = append(lines, promptSkillLine(s, descLimit))
		}
		return lines
	}
	fullLines := renderLines(all, 0)
	full := promptSectionWithLines(skillPromptHeader, fullLines)
	if budgetChars <= 0 || len(full) <= budgetChars {
		return full, PromptBudgetReport{BudgetChars: budgetChars, UsedChars: len(full)}
	}
	compactLines := renderLines(all, 120)
	header := compactSkillPromptHeader
	compact := promptSectionWithLines(header, compactLines)
	if len(compact) <= budgetChars {
		return compact, PromptBudgetReport{BudgetChars: budgetChars, UsedChars: len(compact), Compacted: true}
	}
	if len(header) > budgetChars {
		header = minimalSkillPromptHeader
	}
	if len(header) > budgetChars {
		section := header[:budgetChars]
		return section, PromptBudgetReport{BudgetChars: budgetChars, UsedChars: len(section), Compacted: true, Omitted: promptBudgetOmissions(all)}
	}
	ordered := append([]Skill(nil), all...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if sourceRank(ordered[i].Source) != sourceRank(ordered[j].Source) {
			return sourceRank(ordered[i].Source) < sourceRank(ordered[j].Source)
		}
		return ordered[i].Name < ordered[j].Name
	})
	keptLines := make([]string, 0, len(ordered))
	var omitted []PromptOmittedSkill
	usedChars := len(header)
	for _, s := range ordered {
		line := promptSkillLine(s, 80)
		nextChars := usedChars + len(line) + 1
		if nextChars <= budgetChars {
			keptLines = append(keptLines, line)
			usedChars = nextChars
			continue
		}
		omitted = append(omitted, PromptOmittedSkill{Name: s.Name, Source: s.Source, Reason: "prompt budget"})
	}
	section := promptSectionWithLines(header, keptLines)
	return section, PromptBudgetReport{BudgetChars: budgetChars, UsedChars: len(section), Compacted: true, Omitted: omitted}
}

func promptBudgetOmissions(skills []Skill) []PromptOmittedSkill {
	omitted := make([]PromptOmittedSkill, 0, len(skills))
	for _, skill := range skills {
		omitted = append(omitted, PromptOmittedSkill{Name: skill.Name, Source: skill.Source, Reason: "prompt budget"})
	}
	return omitted
}

func promptSkillLine(s Skill, descLimit int) string {
	desc := strings.TrimSpace(s.Description)
	if descLimit > 0 {
		desc = trimRunes(desc, descLimit)
	}
	if desc == "" {
		desc = "no description"
	}
	return fmt.Sprintf("- **%s** (%s) — %s", s.Name, s.Source, desc)
}

func promptSectionWithLines(header string, lines []string) string {
	var sb strings.Builder
	sb.WriteString(header)
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func normalizePolicy(policy Policy) Policy {
	return Policy{
		Include:           cleanNameList(policy.Include),
		Exclude:           cleanNameList(policy.Exclude),
		PromptBudgetChars: policy.PromptBudgetChars,
	}
}

func clonePolicy(policy Policy) Policy {
	policy.Include = append([]string(nil), policy.Include...)
	policy.Exclude = append([]string(nil), policy.Exclude...)
	return policy
}

func cleanNameList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func nameSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func trimRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func sourceRank(source string) int {
	switch {
	case source == "project":
		return 0
	case strings.HasPrefix(source, "ext:"):
		return 1
	case source == "user":
		return 2
	default:
		return 3
	}
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
