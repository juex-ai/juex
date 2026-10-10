package migration

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestSourceRequestEpochMatchesFixedSourceGolden(t *testing.T) {
	// Produced by 281889e5 BuildRequestEpoch and ValidateRequestEpoch,
	// independently of this decoder. Tools was an explicit empty slice.
	raw, err := os.ReadFile("testdata/source_request_epoch.json")
	if err != nil {
		t.Fatal(err)
	}
	var epoch sourceRequestEpoch
	if err := json.Unmarshal(raw, &epoch); err != nil {
		t.Fatal(err)
	}
	const want = "695fff469ac0874046fbac952635f033404997a4a739877327684a87c9fae0a0"
	if epoch.digest() != want || epoch.RequestDigest != want {
		t.Fatal("fixed source digest changed")
	}
	epoch.Provider.Model = "changed"
	if epoch.digest() == want {
		t.Fatal("provider is not bound by request digest")
	}
}

func originFixture(t *testing.T) (legacy.Agent, []ResolvedModels) {
	t.Helper()
	p := llm.ProviderProfile{ID: "openai-codex", Protocol: llm.ProtocolOpenAICodexResponses, Model: "fixture", Headers: map[string]string{"ChatGPT-Account-ID": "fixture-account"}, Capabilities: llm.ProviderCapabilities{ReasoningReplay: true}}
	message := llm.Message{ID: "answer", Role: llm.RoleAssistant, Model: "display-label-is-not-authority", Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "", Signature: "opaque-fixture"}, {Type: llm.BlockText, Text: "answer"}}}
	epoch := sourceRequestEpoch{EpochID: "epoch", Purpose: "turn", Iter: 0, Attempt: 1, Provider: sourceProvider(p), ContextWindow: 8192, HistoryDigest: strings.Repeat("a", 64), HistoryMessageIDs: []string{}, Messages: []sourceMessageRef{}}
	epoch.RequestDigest = epoch.digest()
	event := func(id, kind string, payload any) legacy.Fact {
		data, err := json.Marshal(map[string]any{"id": id, "type": kind, "turn_id": "turn", "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		return legacy.Fact{Type: "event.recorded", Event: data}
	}
	facts := []legacy.Fact{
		event("epoch", "provider.request_epoch", map[string]any{"epoch": epoch}),
		event("request", "llm.requested", map[string]any{"epoch_id": "epoch", "request_digest": epoch.RequestDigest, "purpose": "turn", "iter": 0}),
		event("response", "llm.responded", map[string]any{"epoch_id": "epoch", "request_digest": epoch.RequestDigest, "message_id": message.ID, "iter": 0, "blocks": message.Blocks}),
		// Old retained facts need not carry Turn IDs; request provenance does.
		{Type: "message.appended", Message: &message},
	}
	return legacy.Agent{Definition: legacy.AgentDefinition{ID: "agent"}, Threads: []legacy.Thread{{Metadata: legacy.ThreadMetadata{ThreadID: "0"}, Context: []llm.Message{message}, Commits: []legacy.Commit{{Facts: facts}}}}}, []ResolvedModels{{AgentID: "other-agent", Models: []ResolvedModel{{Profile: p}}}}
}

func mutateOriginEvent(t *testing.T, fact *legacy.Fact, mutate func(map[string]any)) {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(fact.Event, &value); err != nil {
		t.Fatal(err)
	}
	mutate(value)
	fact.Event, _ = json.Marshal(value)
}

func TestSourceModelOriginsJoinGlobalCatalogAndOwnerReceipt(t *testing.T) {
	agent, profiles := originFixture(t)
	origins, err := sourceModelOrigins(agent, profiles)
	key := ModelKey{Provider: "openai-codex", Name: "fixture"}
	if err != nil || origins["0"]["answer"] != key {
		t.Fatal(origins, err)
	}
	plan := ModelPublicationPlan{Catalog: []CatalogModel{{Key: key, Configuration: management.ModelConfiguration{Provider: key.Provider, Name: key.Name, Protocol: llm.ProtocolOpenAICodexResponses, Endpoint: "https://chatgpt.com/backend-api"}}}}
	receipt := map[ModelKey]management.ModelImportIdentity{key: {ID: "11111111-1111-4111-8111-111111111111", ModelAuthorizationEpoch: 7}}
	bound, err := bindModelOrigins(origins, plan, receipt)
	if err != nil || bound["0"]["answer"].ModelID != receipt[key].ID || bound["0"]["answer"].ModelAuthorizationEpoch != 7 || bound["0"]["answer"].TenantAccessEpoch != 0 {
		t.Fatal(bound, err)
	}
	if bound["0"]["answer"].Endpoint != plan.Catalog[0].Configuration.Endpoint {
		t.Fatal("origin did not use published endpoint")
	}
	// Same source message IDs remain independent across Threads.
	agent.Threads = append(agent.Threads, agent.Threads[0])
	agent.Threads[1].Metadata.ThreadID = "worker"
	second, err := sourceModelOrigins(agent, profiles)
	if err != nil || !reflect.DeepEqual(second["0"], second["worker"]) {
		t.Fatal(second, err)
	}
}

func TestSourceModelOriginsRejectUnprovedReasoning(t *testing.T) {
	for name, mutate := range map[string]func(*legacy.Agent, *[]ResolvedModels){
		"missing epoch": func(a *legacy.Agent, _ *[]ResolvedModels) { a.Threads[0].Commits[0].Facts[0].Event = nil },
		"duplicate epoch": func(a *legacy.Agent, _ *[]ResolvedModels) {
			a.Threads[0].Commits[0].Facts = append(a.Threads[0].Commits[0].Facts, a.Threads[0].Commits[0].Facts[0])
		},
		"duplicate response": func(a *legacy.Agent, _ *[]ResolvedModels) {
			a.Threads[0].Commits[0].Facts = append(a.Threads[0].Commits[0].Facts, a.Threads[0].Commits[0].Facts[2])
		},
		"missing request": func(a *legacy.Agent, _ *[]ResolvedModels) { a.Threads[0].Commits[0].Facts[1].Event = nil },
		"order": func(a *legacy.Agent, _ *[]ResolvedModels) {
			f := a.Threads[0].Commits[0].Facts
			f[0], f[1] = f[1], f[0]
		},
		"wrong turn": func(a *legacy.Agent, _ *[]ResolvedModels) {
			mutateOriginEvent(t, &a.Threads[0].Commits[0].Facts[2], func(v map[string]any) { v["turn_id"] = "other" })
		},
		"wrong digest": func(a *legacy.Agent, _ *[]ResolvedModels) {
			mutateOriginEvent(t, &a.Threads[0].Commits[0].Facts[2], func(v map[string]any) { v["payload"].(map[string]any)["request_digest"] = strings.Repeat("b", 64) })
		},
		"wrong iter": func(a *legacy.Agent, _ *[]ResolvedModels) {
			mutateOriginEvent(t, &a.Threads[0].Commits[0].Facts[2], func(v map[string]any) { v["payload"].(map[string]any)["iter"] = 2 })
		},
		"changed reasoning": func(a *legacy.Agent, _ *[]ResolvedModels) {
			a.Threads[0].Context[0].Blocks = []llm.Block{{Type: llm.BlockReasoning, Signature: "changed"}}
		},
		"wrong profile": func(_ *legacy.Agent, p *[]ResolvedModels) {
			(*p)[0].Models[0].Profile.Headers["ChatGPT-Account-ID"] = "other-account"
		},
		"endpoint normalized early": func(_ *legacy.Agent, p *[]ResolvedModels) {
			(*p)[0].Models[0].Profile.BaseURL = "https://chatgpt.com/backend-api"
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, p := originFixture(t)
			mutate(&a, &p)
			if _, err := sourceModelOrigins(a, p); err == nil {
				t.Fatal("unproved reasoning accepted")
			}
		})
	}
}

func TestSourceOpaqueAccountRequiresFixedSourceHeader(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"absent": {}, "empty": {"ChatGPT-Account-ID": " "}, "template": {"ChatGPT-Account-ID": "${ACCOUNT}"},
		"escaped template": {"ChatGPT-Account-ID": "$${ACCOUNT}"}, "upper only": {"CHATGPT-ACCOUNT-ID": "fixture"},
		"duplicate": {"ChatGPT-Account-ID": "fixture", "chatgpt-account-id": "fixture"},
	} {
		t.Run(name, func(t *testing.T) {
			if sourceOpaqueAccount(llm.ProviderProfile{Protocol: llm.ProtocolOpenAICodexResponses, Headers: headers}) {
				t.Fatal("unproved account accepted")
			}
		})
	}
	for _, name := range []string{"ChatGPT-Account-ID", "chatgpt-account-id"} {
		if !sourceOpaqueAccount(llm.ProviderProfile{Protocol: llm.ProtocolOpenAICodexResponses, Headers: map[string]string{name: "fixture"}}) {
			t.Fatal("fixed source account rejected")
		}
	}
}

func TestSourceReplayRouteDoesNotAuthorizeArbitraryDefaults(t *testing.T) {
	p := llm.ProviderProfile{Protocol: llm.ProtocolOpenAICodexResponses}
	for _, endpoint := range []string{"https://chatgpt.com/backend-api", "https://chatgpt.com/backend-api/codex", "https://chatgpt.com/backend-api/codex/responses"} {
		if !sourceReplayRoute(p, endpoint) {
			t.Fatal("fixed source default rejected")
		}
	}
	for _, endpoint := range []string{"", "https://other.example/codex", "https://chatgpt.com/backend-api/codex?account=other"} {
		if sourceReplayRoute(p, endpoint) {
			t.Fatal("unproved default endpoint accepted")
		}
	}
	p.Protocol = llm.ProtocolOpenAIChat
	if sourceReplayRoute(p, "https://api.openai.com/v1") {
		t.Fatal("environment-dependent SDK default inferred")
	}
}
