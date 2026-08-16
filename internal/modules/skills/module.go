// Package skillsmodule adapts the Skill catalog, Tools, and provider context
// to the runtime Module framework.
package skillsmodule

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/sandbox"
	"github.com/juex-ai/juex/internal/skills"
	"github.com/juex-ai/juex/internal/tools"
)

const ModuleID runtimemodule.ID = "skills"

const defaultSkillSearchLimit = 20

type Options struct {
	Dirs          []skills.Dir
	LoaderOptions skills.LoaderOptions
	WorkDir       string
	Sandbox       sandbox.Policy
}

type Module struct {
	loader  *skills.Loader
	workDir string
	policy  sandbox.Policy
}

func New(options Options) (*Module, error) {
	loader := skills.NewLoaderFromDirsWithOptions(options.Dirs, options.LoaderOptions)
	if err := loader.Load(); err != nil {
		return nil, err
	}
	return NewWithLoader(loader, options.WorkDir, options.Sandbox), nil
}

func NewWithLoader(loader *skills.Loader, workDir string, policy sandbox.Policy) *Module {
	return &Module{loader: loader, workDir: workDir, policy: policy}
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	if m == nil || m.loader == nil {
		return nil, fmt.Errorf("skills module: loader is required")
	}
	guard := sandbox.NewPathGuard(m.workDir, m.policy)
	definitions := ToolDefinitions()
	return []tools.Tool{
		definitions[0].Bind(func(ctx context.Context, input map[string]any) (string, error) {
			_ = ctx
			query, _ := input["query"].(string)
			limit := intFromAny(input["limit"], defaultSkillSearchLimit)
			if limit <= 0 || limit > 100 {
				limit = defaultSkillSearchLimit
			}
			results := m.loader.Search(query, limit)
			summaries := make([]skillSearchResult, 0, len(results))
			for _, skill := range results {
				summaries = append(summaries, skillSearchResult{
					Name:        skill.Name,
					Description: skill.Description,
					Type:        skill.Type,
					Source:      skill.Source,
					Path:        skill.Path,
				})
			}
			body, err := json.MarshalIndent(map[string]any{
				"query":   strings.TrimSpace(query),
				"count":   len(summaries),
				"results": summaries,
			}, "", "  ")
			if err != nil {
				return "", err
			}
			return string(body), nil
		}),
		definitions[1].Bind(func(ctx context.Context, input map[string]any) (string, error) {
			_ = ctx
			name, _ := input["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				return "", fmt.Errorf("skill_load: name is required")
			}
			skill, ok := m.loader.Get(name)
			if !ok {
				return "", fmt.Errorf("skill_load: unknown skill %q; call skill_search to inspect available skills", name)
			}
			if body, ok := skill.BuiltinContent(); ok {
				return formatSkillLoadResult(skill, body), nil
			}
			if err := guard.Check(skill.Path); err != nil {
				return "", fmt.Errorf("skill_load: %w", err)
			}
			data, err := os.ReadFile(skill.Path)
			if err != nil {
				return "", err
			}
			return formatSkillLoadResult(skill, string(data)), nil
		}),
	}, nil
}

func (m *Module) Context(context.Context, runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || m.loader == nil {
		return nil, fmt.Errorf("skills module: loader is required")
	}
	text := m.loader.PromptSection()
	if text == "" {
		return nil, nil
	}
	return []runtimemodule.ContextSection{{
		Key:    "skills",
		Label:  "Available Skills",
		Source: "runtime",
		Text:   text,
	}}, nil
}

func (m *Module) All() []skills.Skill {
	if m == nil || m.loader == nil {
		return nil
	}
	return m.loader.All()
}

func (m *Module) PromptReport() skills.PromptBudgetReport {
	if m == nil || m.loader == nil {
		return skills.PromptBudgetReport{}
	}
	return m.loader.PromptReport()
}

func (m *Module) Filtered() []skills.FilteredSkill {
	if m == nil || m.loader == nil {
		return nil
	}
	return m.loader.Filtered()
}

func ToolDefinitions() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		{
			Name:        "skill_search",
			Group:       tools.ToolGroupSkill,
			Description: "Search the loaded skill catalog by name, description, type, or source. Use this when the compact skill prompt does not list the skill you need.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Optional case-insensitive search text. Empty lists the first matching skills.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum results to return. Defaults to 20.",
					},
				},
			},
		},
		{
			Name:        "skill_load",
			Group:       tools.ToolGroupSkill,
			Description: "Load a skill by name, including its SKILL.md path, directory, source, and full markdown body. Call this before following a skill from the compact skill catalog.",
			Schema: map[string]any{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Skill name from the Available Skills catalog or skill_search result.",
					},
				},
			},
		},
	}
}

func formatSkillLoadResult(skill skills.Skill, body string) string {
	return fmt.Sprintf("Skill: %s\nSource: %s\nPath: %s\nDirectory: %s\n\n--- SKILL.md ---\n%s", skill.Name, skill.Source, skill.Path, skillDirectory(skill), body)
}

func skillDirectory(skill skills.Skill) string {
	if skill.IsBuiltin() {
		return strings.TrimSuffix(skill.Path, "/SKILL.md")
	}
	return filepath.Dir(skill.Path)
}

type skillSearchResult struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	Source      string `json:"source"`
	Path        string `json:"path"`
}

func intFromAny(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, err := v.Int64()
		if err == nil {
			return int(i)
		}
	}
	return fallback
}
