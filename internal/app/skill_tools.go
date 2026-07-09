package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/skills"
	"github.com/juex-ai/juex/internal/tools"
)

const defaultSkillSearchLimit = 20

func skillLoaderOptions(cfg config.Config) skills.LoaderOptions {
	policy := cfg.SkillPolicy()
	return skills.LoaderOptions{Policy: skills.Policy{
		Include:           policy.Include,
		Exclude:           policy.Exclude,
		PromptBudgetChars: policy.PromptBudgetChars,
	}}
}

func registerSkillTools(reg *tools.Registry, loader *skills.Loader) error {
	if err := reg.Register(tools.Tool{
		Name:        "skill_search",
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
		Handler: func(ctx context.Context, input map[string]any) (string, error) {
			_ = ctx
			query, _ := input["query"].(string)
			limit := intFromAny(input["limit"], defaultSkillSearchLimit)
			if limit <= 0 || limit > 100 {
				limit = defaultSkillSearchLimit
			}
			results := loader.Search(query, limit)
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
		},
	}); err != nil {
		return err
	}
	return reg.Register(tools.Tool{
		Name:        "skill_load",
		Description: "Load the full SKILL.md body for a skill by name. Call this before following a skill from the compact skill catalog.",
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
		Handler: func(ctx context.Context, input map[string]any) (string, error) {
			_ = ctx
			name, _ := input["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				return "", fmt.Errorf("skill_load: name is required")
			}
			skill, ok := loader.Get(name)
			if !ok {
				return "", fmt.Errorf("skill_load: unknown skill %q; call skill_search to inspect available skills", name)
			}
			data, err := os.ReadFile(skill.Path)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	})
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
