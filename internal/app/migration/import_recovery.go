package migration

import (
	"reflect"
	"slices"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/importproof"
)

// The complete original shared chains must be representable before a v1
// receipt can prove an input. Different per-Agent tails are valid new input,
// but cannot be projected to the same historical proof.
func modelProofV1(value management.ModelsImport, plan ModelPublicationPlan) (importproof.ModelsV1, error) {
	proof := importproof.ModelsV1{TenantID: value.TenantID, Source: value.Source, SourceSHA256: value.SourceSHA256}
	tails, err := sharedModelTails(plan)
	if err != nil {
		return proof, err
	}
	for _, item := range value.Models {
		config, err := importproof.FreezeModel(item.Configuration)
		if err != nil {
			return proof, err
		}
		model := importproof.ModelV1{Configuration: config}
		for _, key := range tails[ModelKey{Provider: config.Provider, Name: config.Name}] {
			model.Fallbacks = append(model.Fallbacks, management.ModelKey(key))
		}
		proof.Models = append(proof.Models, model)
	}
	return proof, nil
}

func sharedModelTails(plan ModelPublicationPlan) (map[ModelKey][]ModelKey, error) {
	tails := map[ModelKey][]ModelKey{}
	for _, agent := range plan.Agents {
		if len(agent.Models) == 0 {
			return nil, management.ErrConflict
		}
		primary, tail := agent.Models[0], agent.Models[1:]
		if prior, exists := tails[primary]; exists && !slices.Equal(prior, tail) {
			return nil, management.ErrConflict
		}
		tails[primary] = tail
	}
	return tails, nil
}

// Called only after exact recovery of the original model receipt. The request
// is built from that receipt's identities and the same complete model plan.
func agentProofV1(value management.AgentsImport) (importproof.AgentsV1, error) {
	proof := importproof.AgentsV1{ExpectedFleetID: value.ExpectedFleetID, Source: value.Source, SourceSHA256: value.SourceSHA256}
	for _, item := range value.Agents {
		c := item.Config
		if c.Configuration == nil || len(c.Configuration.Models) == 0 || len(c.Configuration.Modules) != len(importproof.ModulesV1()) {
			return proof, management.ErrConflict
		}
		policy := agentpolicy.Policy{Disabled: []agentpolicy.Capability{}}
		for _, key := range importproof.ModulesV1() {
			enabled, present := c.Configuration.Modules[key]
			if !present {
				return proof, management.ErrConflict
			}
			if !enabled {
				policy.Disabled = append(policy.Disabled, key)
			}
		}
		slices.Sort(policy.Disabled)
		proof.Agents = append(proof.Agents, importproof.AgentV1{SourceAgentID: item.SourceAgentID, Config: importproof.AgentConfigV1{DynamicInstructions: c.DynamicInstructions, Capabilities: &policy, Hooks: c.Hooks, WorkerDepth: c.WorkerDepth, Name: c.Name, Instructions: c.Instructions, ModelID: c.Configuration.Models[0]}})
	}
	lifted, _, err := proof.Prepare()
	if err != nil {
		return proof, err
	}
	for i, item := range lifted.Agents {
		// Tails are independently covered by the verified model proof. Everything
		// else must survive this projection exactly, including explicit module keys.
		item.Config.Configuration.Models = slices.Clone(value.Agents[i].Config.Configuration.Models)
		if !reflect.DeepEqual(item, value.Agents[i]) {
			return proof, management.ErrConflict
		}
	}
	return proof, nil
}
