package migration

import (
	"cmp"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

// ModelKey is the resolved catalog identity, not the source selector spelling.
type ModelKey struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

type CatalogModel struct {
	Key           ModelKey                      `json:"key"`
	Configuration management.ModelConfiguration `json:"-"`
}

type AgentModelBinding struct {
	SourceAgentID string   `json:"source_agent_id"`
	Primary       ModelKey `json:"primary"`
}

type ModelFallbacks struct {
	Primary    ModelKey   `json:"primary"`
	Candidates []ModelKey `json:"candidates"`
}

// ModelPublicationPlan proves consistency of supplied inputs only. Publishing
// still requires Management validation, an existing-private-state guard, fresh
// tenant/Agent authority and mapping keys to the UUIDs returned by Management.
// It grants no right to overwrite a catalog or change the platform default.
type ModelPublicationPlan struct {
	Catalog   []CatalogModel      `json:"catalog"`
	Agents    []AgentModelBinding `json:"agents"`
	Fallbacks []ModelFallbacks    `json:"fallbacks"`
}

// ConvertModels retains ordinary request caps and applies an explicit target
// reservation policy. It never reads environment, credentials or target state.
func ConvertModels(values []ResolvedModels, reserves map[ModelKey]int) (ModelPublicationPlan, error) {
	if len(values) == 0 {
		return ModelPublicationPlan{}, errors.New("model conversion requires source Agents")
	}
	plan := ModelPublicationPlan{}
	catalog := map[ModelKey]management.ModelConfiguration{}
	tails := map[ModelKey][]ModelKey{}
	agents := map[string]bool{}
	for _, value := range values {
		if strings.TrimSpace(value.AgentID) == "" || agents[value.AgentID] || len(value.Models) == 0 || len(value.Models) > 5 {
			return ModelPublicationPlan{}, errors.New("model conversion requires distinct Agents and one to five candidates")
		}
		agents[value.AgentID] = true
		chain := make([]ModelKey, 0, len(value.Models))
		seen := map[ModelKey]bool{}
		for _, model := range value.Models {
			p := model.Profile
			// A source SDK may obtain its default URL from process environment.
			// Catalog publication requires that route to be resolved explicitly.
			if strings.TrimSpace(p.BaseURL) == "" {
				return ModelPublicationPlan{}, errors.New("source provider endpoint requires explicit resolution before publication")
			}
			key := ModelKey{Provider: p.ID, Name: p.Model}
			reserve, ok := reserves[key]
			if !ok || strings.TrimSpace(key.Provider) == "" || strings.TrimSpace(key.Name) == "" || seen[key] || p.MediaDir != "" {
				return ModelPublicationPlan{}, errors.New("model conversion requires distinct resolved identities, explicit reserves and no source media directory")
			}
			if model.ContextWindow < 1024 || model.MaxOutputTokens < 0 || reserve <= 0 || model.MaxOutputTokens > reserve || reserve >= model.ContextWindow || p.Protocol == llm.ProtocolAnthropicMessages && model.MaxOutputTokens == 0 && reserve < llm.AnthropicDefaultOutputTokens {
				return ModelPublicationPlan{}, errors.New("model conversion has an invalid context, cap or reservation policy")
			}
			// Freeze every effective capability: the target's managed adapter does not
			// inherit a source provider preset's defaults.
			c := p.Capabilities
			options := management.ModelOptions{Authentication: p.Authentication, ThinkingEffort: p.ThinkingEffort, Headers: maps.Clone(p.Headers), Query: maps.Clone(p.Query), Capabilities: llm.CapabilityOverrides{Tools: &c.Tools, Vision: &c.Vision, Streaming: &c.Streaming, ReasoningEffort: &c.ReasoningEffort, ReasoningReplay: &c.ReasoningReplay, MaxOutputTokens: &c.MaxOutputTokens}, Compat: p.Compat}
			options.Compat.ReasoningReplayFields = slices.Clone(p.Compat.ReasoningReplayFields)
			config := management.ModelConfiguration{Provider: key.Provider, Name: key.Name, Endpoint: p.BaseURL, APIKey: p.APIKey, Protocol: p.Protocol, ContextWindow: model.ContextWindow, MaxOutput: model.MaxOutputTokens, OutputReserve: reserve, Enabled: true, Options: options.Normalized()}
			if prior, exists := catalog[key]; exists && !reflect.DeepEqual(prior, config) {
				return ModelPublicationPlan{}, errors.New("shared model identity has conflicting private configuration")
			}
			catalog[key], seen[key] = config, true
			chain = append(chain, key)
		}
		primary := chain[0]
		// An explicit empty chain must survive so publication can clear a prior
		// fallback policy. Chains are direct and are never recursively expanded.
		tail := append([]ModelKey{}, chain[1:]...)
		if prior, exists := tails[primary]; exists && !slices.Equal(prior, tail) {
			return ModelPublicationPlan{}, errors.New("shared primary model has conflicting ordered fallback policy")
		}
		tails[primary] = tail
		plan.Agents = append(plan.Agents, AgentModelBinding{SourceAgentID: value.AgentID, Primary: primary})
	}
	if len(reserves) != len(catalog) {
		return ModelPublicationPlan{}, errors.New("reservation policy includes an unused model identity")
	}
	for _, key := range slices.SortedFunc(maps.Keys(catalog), compareModelKey) {
		plan.Catalog = append(plan.Catalog, CatalogModel{Key: key, Configuration: catalog[key]})
	}
	for _, key := range slices.SortedFunc(maps.Keys(tails), compareModelKey) {
		plan.Fallbacks = append(plan.Fallbacks, ModelFallbacks{Primary: key, Candidates: tails[key]})
	}
	slices.SortFunc(plan.Agents, func(a, b AgentModelBinding) int { return strings.Compare(a.SourceAgentID, b.SourceAgentID) })
	return plan, nil
}

func compareModelKey(a, b ModelKey) int {
	if order := cmp.Compare(a.Provider, b.Provider); order != 0 {
		return order
	}
	return cmp.Compare(a.Name, b.Name)
}
