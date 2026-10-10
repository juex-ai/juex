package migration

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/importproof"
)

func TestModelRecoveryProofBindsCompleteIndependentChains(t *testing.T) {
	a, b, c := planModel("p", "a"), planModel("p", "b"), planModel("p", "c")
	makePlan := func(tails []ResolvedModel) ModelPublicationPlan {
		t.Helper()
		result, err := ConvertModels([]ResolvedModels{{AgentID: "one", Models: []ResolvedModel{a, b, c}}, {AgentID: "two", Models: append([]ResolvedModel{a}, tails...)}}, map[ModelKey]int{{"p", "a"}: 4096, {"p", "b"}: 4096, {"p", "c"}: 4096})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	value := management.ModelsImport{TenantID: "370b8a1a-690f-4c27-9813-6b1be9fe99bc", Source: "source", SourceSHA256: strings.Repeat("a", 64)}
	plan := makePlan([]ResolvedModel{b, c})
	for _, item := range plan.Catalog {
		value.Models = append(value.Models, management.ImportedModel{Configuration: item.Configuration})
	}
	proof, err := modelProofV1(value, plan)
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := proof.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, tails := range [][]ResolvedModel{{c, b}, {b}, {}} {
		if _, err := modelProofV1(value, makePlan(tails)); !errors.Is(err, management.ErrConflict) {
			t.Fatal("lossy shared-chain projection accepted", err)
		}
	}
	changed := plan
	changed.Agents = slices.Clone(plan.Agents)
	for i := range changed.Agents {
		changed.Agents[i].Models = []ModelKey{{"p", "a"}, {"p", "c"}, {"p", "b"}}
	}
	other, err := modelProofV1(value, changed)
	if err != nil {
		t.Fatal(err)
	}
	_, changedHash, err := other.Prepare()
	if err != nil || changedHash == hash {
		t.Fatal("changed complete order lost from proof", err)
	}
}

func TestAgentRecoveryProofRequiresCompleteDeclaration(t *testing.T) {
	modules := map[agentpolicy.Capability]bool{}
	for _, key := range importproof.ModulesV1() {
		modules[key] = true
	}
	original := management.AgentsImport{ExpectedFleetID: "a54f2f66-7d1c-4d6d-8e42-4a09f30c8245", Source: "proof-fixture", SourceSHA256: strings.Repeat("a", 64), Agents: []management.ImportedAgent{{SourceAgentID: "one", Config: management.AgentConfig{Name: "Agent", WorkerDepth: 2, Configuration: &management.Configuration{Models: []string{"c31b08bf-7a0c-4c8b-9b02-d3124c10f7ab"}, Modules: modules}}}}}
	proof, err := agentProofV1(original)
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := proof.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*management.AgentConfig){func(c *management.AgentConfig) { delete(c.Configuration.Modules, agentpolicy.MCP) }, func(c *management.AgentConfig) { c.Configuration.Modules["future"] = true }} {
		value := original
		value.Agents = slices.Clone(original.Agents)
		decl := original.Agents[0].Config.Configuration.Clone()
		value.Agents[0].Config.Configuration = &decl
		mutate(&value.Agents[0].Config)
		if _, err := agentProofV1(value); !errors.Is(err, management.ErrConflict) {
			t.Fatal("incomplete declaration accepted", err)
		}
	}
	original.Agents[0].Config.Configuration.Modules[agentpolicy.MCP] = false
	changed, err := agentProofV1(original)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := changed.Prepare()
	if err != nil || other == hash {
		t.Fatal("module edit disappeared from proof", err)
	}
}
