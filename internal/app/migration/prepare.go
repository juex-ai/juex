package migration

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

// PreparedBundle contains private configuration and grants no runtime authority.
// Agent configs intentionally have no model UUID until Management publishes it.
type PreparedBundle struct {
	Models     ModelPublicationPlan                  `json:"-"`
	Agents     map[string]management.AgentConfig     `json:"-"`
	Extensions map[string][]extensionpolicy.Manifest `json:"-"`
}

// Prepare resolves frozen configuration without source, environment, clock or
// target I/O. Runtime/application conversion still requires fresh target scopes
// and real Artifact receipts; this step is not an activation or cutover check.
func (b *Bundle) Prepare() (PreparedBundle, error) {
	var empty PreparedBundle
	configs, err := ResolveConfig(b.source, b.inputs.Config)
	if err != nil {
		return empty, err
	}
	if len(configs) == 0 || len(b.inputs.Config.Contexts) != len(configs) || len(b.inputs.Agents) != len(configs) || len(b.inputs.Models) != len(configs) {
		return empty, errors.New("bundle must bind every source Agent exactly once")
	}
	evidence := map[string]ModelEvidence{}
	for _, m := range b.inputs.Models {
		if _, exists := evidence[m.AgentID]; exists {
			return empty, errors.New("duplicate Agent model evidence")
		}
		evidence[m.AgentID] = m
	}
	reserves := map[ModelKey]int{}
	endpoints := map[ModelKey]string{}
	for _, p := range b.inputs.ModelsPolicy {
		if _, exists := reserves[p.Key]; exists {
			return empty, errors.New("duplicate model policy")
		}
		reserves[p.Key] = p.OutputReserve
		endpoints[p.Key] = p.Endpoint
	}
	plan := PreparedBundle{Agents: map[string]management.AgentConfig{}, Extensions: map[string][]extensionpolicy.Manifest{}}
	models := make([]ResolvedModels, 0, len(configs))
	byAgent := make(map[string]ResolvedConfig, len(configs))
	sourceAgents := make(map[string]legacy.Agent, len(b.source.Agents))
	for _, agent := range b.source.Agents {
		sourceAgents[agent.Definition.ID] = agent
	}
	for _, c := range configs {
		byAgent[c.AgentID] = c
		agent := sourceAgents[c.AgentID]
		policy, ok := b.inputs.Agents[c.AgentID]
		if !ok || !filepath.IsAbs(policy.Workspace) || filepath.Clean(policy.Workspace) != policy.Workspace || policy.Workspace != agent.Definition.Workspace {
			return empty, errors.New("source Agent requires an explicit unchanged external Workspace binding")
		}
		if !agent.Definition.Enabled || policy.Activation != "on_demand" {
			return empty, errors.New("enabled source Agents require an explicit on-demand target lifecycle")
		}
		m, err := ResolveModels(c, evidence[c.AgentID])
		if err != nil {
			return empty, err
		}
		for i := range m.Models {
			profile := &m.Models[i].Profile
			key := ModelKey{Provider: profile.ID, Name: profile.Model}
			endpoint := endpoints[key]
			if endpoint != "" && profile.BaseURL != "" && endpoint != profile.BaseURL {
				return empty, errors.New("endpoint resolution cannot replace a captured provider route")
			}
			if profile.BaseURL == "" {
				profile.BaseURL = endpoint
			}
		}
		models = append(models, m)
		config, err := prepareAgentConfig(c, agent.Definition, AgentConfigBindings{Instructions: policy.Instructions, FilesEnabled: policy.FilesEnabled, ShellEnabled: policy.ShellEnabled, CalendarEnabled: policy.CalendarEnabled, CollaborationEnabled: policy.CollaborationEnabled, GlobalInstructionPath: policy.GlobalInstructionPath})
		if err != nil {
			return empty, err
		}
		plan.Agents[c.AgentID] = config
	}
	plan.Models, err = ConvertModels(models, reserves)
	if err != nil {
		return empty, err
	}
	seen := map[string]bool{}
	for _, e := range b.inputs.Extensions {
		c, ok := byAgent[e.AgentID]
		selection := legacy.ExtensionResources{MCP: c.Modules["mcp"], Hooks: c.Modules["hooks"], Observables: c.Modules["observables"], Skills: c.Modules["skills"]}
		if !ok || !c.Modules["extensions"] || !selection.MCP || e.Snapshot.Selection != selection {
			return empty, errors.New("extension selection must match the source Agent's enabled resource modules")
		}
		manifest, err := ConvertStdioExtension(e.Snapshot, e.Bindings)
		if err != nil {
			return empty, fmt.Errorf("source Agent %s extension: %w", e.AgentID, err)
		}
		if !slices.Contains(c.ExtensionAllow, manifest.Name) {
			return empty, errors.New("extension is not selected by the source Agent")
		}
		key := e.AgentID + "/" + manifest.Name
		if seen[key] {
			return empty, errors.New("duplicate Agent extension binding")
		}
		seen[key] = true
		plan.Extensions[e.AgentID] = append(plan.Extensions[e.AgentID], manifest)
	}
	return plan, nil
}
