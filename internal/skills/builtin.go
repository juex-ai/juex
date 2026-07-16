package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/juex-ai/juex/internal/frontmatter"
)

const SourceBuiltin = "builtin"

var builtinSkillNames = []string{
	"juex-chunked-write",
	"juex-observables",
	"juex-session-state",
}

//go:embed builtin/*/SKILL.md
var builtinSkillFS embed.FS

func loadBuiltinSkills() ([]Skill, error) {
	return loadBuiltinSkillsFromFS(builtinSkillFS)
}

func loadBuiltinSkillsFromFS(source fs.FS) ([]Skill, error) {
	paths, err := fs.Glob(source, "builtin/*/SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("skills: list builtin skills: %w", err)
	}
	sort.Strings(paths)
	if len(paths) != len(builtinSkillNames) {
		return nil, fmt.Errorf("skills: builtin catalog has %d guides, want %d", len(paths), len(builtinSkillNames))
	}
	loaded := make([]Skill, 0, len(builtinSkillNames))
	for i, name := range builtinSkillNames {
		embeddedPath := "builtin/" + name + "/SKILL.md"
		if paths[i] != embeddedPath {
			return nil, fmt.Errorf("skills: builtin catalog path %q, want %q", paths[i], embeddedPath)
		}
		data, err := fs.ReadFile(source, embeddedPath)
		if err != nil {
			return nil, fmt.Errorf("skills: read builtin %s: %w", embeddedPath, err)
		}
		parsed, err := frontmatter.Parse(string(data))
		if err != nil {
			return nil, fmt.Errorf("skills: parse builtin %s: %w", embeddedPath, err)
		}
		if parsed.Fields["name"] != name {
			return nil, fmt.Errorf("skills: builtin %s declares name %q, want %q", embeddedPath, parsed.Fields["name"], name)
		}
		if parsed.Fields["description"] == "" || parsed.Fields["type"] != "builtin-guide" || parsed.Body == "" {
			return nil, fmt.Errorf("skills: builtin %s requires description, type builtin-guide, and body", embeddedPath)
		}
		loaded = append(loaded, Skill{
			Name:            name,
			Description:     parsed.Fields["description"],
			Type:            parsed.Fields["type"],
			Body:            parsed.Body,
			raw:             string(data),
			Source:          SourceBuiltin,
			Path:            "builtin://skills/" + name + "/SKILL.md",
			builtin:         true,
			promptHidden:    true,
			strictConflicts: true,
		})
	}
	return loaded, nil
}
