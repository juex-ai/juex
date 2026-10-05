//go:build postgres

package e2e

import (
	"context"
	"reflect"
	"testing"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/app/migration"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/providers/profile"
)

func TestMigrationModelPlanPreservesProfilesAndFallbacksThroughManagement(t *testing.T) {
	_, directory := managementDatabase(t)
	ctx := context.Background()
	configs := []profile.Config{
		{ID: "openai-codex", Model: "main", BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "fixture-codex-token", Headers: map[string]string{"ChatGPT-Account-ID": "fixture-account", "X-Thread": "${juex_thread_id}"}, Query: map[string]string{"route": "private-fixture-route"}, ThinkingEffort: "high", Compat: llm.CompatOptions{CodexTransport: "sse"}},
		{ID: "anthropic", Model: "backup", BaseURL: "https://api.anthropic.com", APIKey: "fixture-anthropic-token", ThinkingEffort: "low"},
		{ID: "local-label", Protocol: string(llm.ProtocolOpenAIChat), Model: "local", BaseURL: "http://localhost:17777/v1", APIKey: "fixture-local-token"},
	}
	var models []migration.ResolvedModel
	originals := map[migration.ModelKey]llm.ProviderProfile{}
	reserves := map[migration.ModelKey]int{}
	for _, config := range configs {
		config.Authentication = "api_key"
		p, err := profile.ResolveProfile(config)
		if err != nil {
			t.Fatal(err)
		}
		key := migration.ModelKey{Provider: p.ID, Name: p.Model}
		originals[key], reserves[key] = p, 8192
		models = append(models, migration.ResolvedModel{Ref: "source:" + p.Model, ContextWindow: 32768, Profile: p})
	}
	values := []migration.ResolvedModels{{AgentID: "one", Models: models[:2]}, {AgentID: "two", Models: models[:2]}, {AgentID: "three", Models: models[:2]}, {AgentID: "minimal", Models: models[2:]}}
	plan, err := migration.ConvertModels(values, reserves)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[migration.ModelKey]string{}
	var allowed []string
	for _, model := range plan.Catalog {
		published, err := managed.ConfigureModel(ctx, directory, model.Configuration)
		if err != nil {
			t.Fatal("owner rejected converted model", model.Key, err)
		}
		ids[model.Key] = published.ID
		allowed = append(allowed, published.ID)
	}
	// Empty tails must be actionable replacements, not omitted instructions.
	local, codex := migration.ModelKey{Provider: models[2].Profile.ID, Name: models[2].Profile.Model}, migration.ModelKey{Provider: models[0].Profile.ID, Name: models[0].Profile.Model}
	if err := directory.SetModelFallbacks(ctx, ids[local], []string{ids[codex]}); err != nil {
		t.Fatal(err)
	}
	for _, tail := range plan.Fallbacks {
		candidates := make([]string, 0, len(tail.Candidates))
		for _, key := range tail.Candidates {
			candidates = append(candidates, ids[key])
		}
		if err := directory.SetModelFallbacks(ctx, ids[tail.Primary], candidates); err != nil {
			t.Fatal(err)
		}
	}
	user, err := directory.CreateUser(ctx, "model-import@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := directory.CreateTenant(ctx, "Model import", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.SetTenantModels(ctx, tenant.ID, false, allowed); err != nil {
		t.Fatal(err)
	}
	authority := managed.RuntimeAuthority{Directory: directory}
	for _, binding := range plan.Agents {
		agent, err := directory.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: binding.SourceAgentID, ModelID: ids[binding.Primary]})
		if err != nil {
			t.Fatal(err)
		}
		scope, err := authority.Authorize(ctx, user.ID, tenant.ID, agent.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		frozen, err := authority.Snapshot(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		want := []migration.ModelKey{codex, {Provider: models[1].Profile.ID, Name: models[1].Profile.Model}}
		if binding.SourceAgentID == "minimal" {
			want = []migration.ModelKey{local}
		}
		if len(frozen.Models) != len(want) {
			t.Fatal("wrong fallback count", binding.SourceAgentID, len(frozen.Models))
		}
		for i, candidate := range frozen.Models {
			if candidate.ModelID != ids[want[i]] || candidate.MaxOutput != 0 || candidate.OutputReserve != reserves[want[i]] {
				t.Fatal("selection or output policy changed", binding.SourceAgentID, i)
			}
			got, err := authority.Profile(ctx, scope, candidate)
			if err != nil || !reflect.DeepEqual(got, originals[want[i]]) {
				t.Fatal("private profile changed after owner publication", binding.SourceAgentID, i, err)
			}
		}
	}
}
