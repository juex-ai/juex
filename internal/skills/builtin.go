package skills

import (
	"embed"
	"fmt"
	"io/fs"

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
	if len(paths) != len(builtinSkillNames) {
		return nil, fmt.Errorf("skills: builtin catalog has %d guides, want %d", len(paths), len(builtinSkillNames))
	}
	expectedPaths := make(map[string]struct{}, len(builtinSkillNames))
	for _, name := range builtinSkillNames {
		embeddedPath := "builtin/" + name + "/SKILL.md"
		if _, exists := expectedPaths[embeddedPath]; exists {
			return nil, fmt.Errorf("skills: builtin catalog repeats %q", name)
		}
		expectedPaths[embeddedPath] = struct{}{}
	}
	for _, path := range paths {
		if _, ok := expectedPaths[path]; !ok {
			return nil, fmt.Errorf("skills: unexpected builtin catalog path %q", path)
		}
	}
	loaded := make([]Skill, 0, len(builtinSkillNames))
	for _, name := range builtinSkillNames {
		embeddedPath := "builtin/" + name + "/SKILL.md"
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
