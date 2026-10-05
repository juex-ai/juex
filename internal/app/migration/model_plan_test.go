package migration

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func planModel(provider, name string) ResolvedModel {
	return ResolvedModel{Ref: provider + ":" + name, ContextWindow: 16000, Profile: llm.ProviderProfile{ID: provider, Model: name, Protocol: llm.ProtocolOpenAIChat, BaseURL: "https://provider.example/v1", APIKey: "private-fixture-key", Authentication: "api_key", ThinkingEffort: "high", Headers: map[string]string{"X-Account": "private-fixture-header"}, Query: map[string]string{"route": "private-fixture-query"}, Capabilities: llm.ProviderCapabilities{Tools: true, Streaming: true, MaxOutputTokens: true}, Compat: llm.CompatOptions{ReasoningReplayFields: []string{"reasoning_content"}}}}
}

func TestConvertModelsPreservesSharedCatalogAndDirectChains(t *testing.T) {
	primary, backup, local := planModel("primary", "model"), planModel("backup", "fallback"), planModel("local", "small")
	source := []ResolvedModels{{AgentID: "one", Models: []ResolvedModel{primary, backup}}, {AgentID: "two", Models: []ResolvedModel{primary, backup}}, {AgentID: "three", Models: []ResolvedModel{primary, backup}}, {AgentID: "minimal", Models: []ResolvedModel{local}}}
	reserves := map[ModelKey]int{{"primary", "model"}: 4096, {"backup", "fallback"}: 2048, {"local", "small"}: 1024}
	got, err := ConvertModels(source, reserves)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Catalog) != 3 || len(got.Agents) != 4 || len(got.Fallbacks) != 2 {
		t.Fatal(got)
	}
	for _, tail := range got.Fallbacks {
		want := []ModelKey{}
		if tail.Primary.Provider == "primary" {
			want = []ModelKey{{"backup", "fallback"}}
		}
		if !reflect.DeepEqual(tail.Candidates, want) {
			t.Fatal("fallback changed", tail)
		}
	}
	for _, item := range got.Catalog {
		c := item.Configuration
		if c.MaxOutput != 0 || c.OutputReserve != reserves[item.Key] || !c.Enabled || c.Options.Capabilities.Vision == nil || *c.Options.Capabilities.Vision || c.Options.Capabilities.Tools == nil || !*c.Options.Capabilities.Tools {
			t.Fatal("source policy changed", item.Key)
		}
	}
	raw, err := json.Marshal(got)
	if err != nil || strings.Contains(string(raw), "private-fixture") || strings.Contains(string(raw), "provider.example") {
		t.Fatal("private configuration in report", err)
	}
	got.Catalog[0].Configuration.Options.Headers["X-Account"] = "changed"
	got.Catalog[0].Configuration.Options.Query["route"] = "changed"
	got.Catalog[0].Configuration.Options.Compat.ReasoningReplayFields[0] = "changed"
	if backup.Profile.Headers["X-Account"] != "private-fixture-header" || backup.Profile.Query["route"] != "private-fixture-query" || backup.Profile.Compat.ReasoningReplayFields[0] != "reasoning_content" {
		t.Fatal("plan aliases captured input")
	}
}

func TestConvertModelsUsesEffectiveIdentityAndStableOrder(t *testing.T) {
	first, second := planModel("effective", "one"), planModel("second", "two")
	first.Ref = "unrelated:configured"
	values := []ResolvedModels{{AgentID: "b", Models: []ResolvedModel{second, first}}, {AgentID: "a", Models: []ResolvedModel{first, second}}}
	reserves := map[ModelKey]int{{"effective", "one"}: 4096, {"second", "two"}: 4096}
	left, err := ConvertModels(values, reserves)
	if err != nil {
		t.Fatal(err)
	}
	values[0], values[1] = values[1], values[0]
	right, err := ConvertModels(values, reserves)
	if err != nil || !reflect.DeepEqual(left, right) {
		t.Fatal("unordered plan", err)
	}
	if left.Agents[0].Primary != (ModelKey{"effective", "one"}) {
		t.Fatal(left.Agents)
	}
	// Direct fallback chains can point at one another; Runtime never expands them.
	if len(left.Fallbacks) != 2 {
		t.Fatal(left.Fallbacks)
	}
}

func TestConvertModelsRejectsPrivateAndChainConflicts(t *testing.T) {
	for _, field := range []string{"credential", "endpoint", "header", "query", "capability", "compat", "thinking", "context", "cap", "protocol", "tail", "tail order"} {
		t.Run(field, func(t *testing.T) {
			a, b, c := planModel("primary", "model"), planModel("backup", "one"), planModel("backup", "two")
			other := planModel("primary", "model")
			tail := []ResolvedModel{b, c}
			switch field {
			case "credential":
				other.Profile.APIKey = "different-private-key"
			case "endpoint":
				other.Profile.BaseURL = "https://other.example/v1"
			case "header":
				other.Profile.Headers["X-Account"] = "different-private-header"
			case "query":
				other.Profile.Query["route"] = "different-private-query"
			case "capability":
				other.Profile.Capabilities.Vision = true
			case "compat":
				other.Profile.Compat.MaxTokensField = "max_completion_tokens"
			case "thinking":
				other.Profile.ThinkingEffort = "low"
			case "context":
				other.ContextWindow = 32000
			case "cap":
				other.MaxOutputTokens = 1024
			case "protocol":
				other.Profile.Protocol = llm.ProtocolOpenAIResponses
			case "tail":
				tail = []ResolvedModel{}
			case "tail order":
				tail = []ResolvedModel{c, b}
			}
			_, err := ConvertModels([]ResolvedModels{{AgentID: "one", Models: []ResolvedModel{a, b, c}}, {AgentID: "two", Models: append([]ResolvedModel{other}, tail...)}}, map[ModelKey]int{{"primary", "model"}: 4096, {"backup", "one"}: 4096, {"backup", "two"}: 4096})
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("conflict accepted or secret exposed", err)
			}
		})
	}
}

func TestConvertModelsRejectsIncompletePolicy(t *testing.T) {
	for _, kind := range []string{"empty", "empty Agent", "duplicate Agent", "empty chain", "long chain", "duplicate key", "missing reserve", "extra reserve", "zero reserve", "large reserve", "cap exceeds reserve", "anthropic reserve", "media directory", "implicit endpoint"} {
		t.Run(kind, func(t *testing.T) {
			values := []ResolvedModels{{AgentID: "one", Models: []ResolvedModel{planModel("primary", "model")}}}
			reserves := map[ModelKey]int{{"primary", "model"}: 4096}
			switch kind {
			case "empty":
				values = nil
			case "empty Agent":
				values[0].AgentID = ""
			case "duplicate Agent":
				values = append(values, values[0])
			case "empty chain":
				values[0].Models = nil
			case "long chain":
				for range 5 {
					values[0].Models = append(values[0].Models, planModel("extra", "model"))
				}
			case "duplicate key":
				values[0].Models = append(values[0].Models, planModel("primary", "model"))
			case "missing reserve":
				reserves = nil
			case "extra reserve":
				reserves[ModelKey{"unused", "model"}] = 4096
			case "zero reserve":
				reserves[ModelKey{"primary", "model"}] = 0
			case "large reserve":
				reserves[ModelKey{"primary", "model"}] = 16000
			case "cap exceeds reserve":
				values[0].Models[0].MaxOutputTokens = 4097
			case "anthropic reserve":
				values[0].Models[0].Profile.Protocol = llm.ProtocolAnthropicMessages
				reserves[ModelKey{"primary", "model"}] = 1024
			case "implicit endpoint":
				values[0].Models[0].Profile.BaseURL = ""
			case "media directory":
				values[0].Models[0].Profile.MediaDir = "/private/source/media"
			}
			if _, err := ConvertModels(values, reserves); err == nil {
				t.Fatal("incomplete or unsupported policy accepted")
			}
		})
	}
}

func TestConvertModelsKeepsPositiveCapSeparateFromReserve(t *testing.T) {
	model := planModel("primary", "model")
	model.MaxOutputTokens = 1024
	plan, err := ConvertModels([]ResolvedModels{{AgentID: "one", Models: []ResolvedModel{model}}}, map[ModelKey]int{{"primary", "model"}: 4096})
	if err != nil {
		t.Fatal(err)
	}
	got := plan.Catalog[0].Configuration
	if got.MaxOutput != 1024 || got.OutputReserve != 4096 {
		t.Fatal("cap replaced by reserve")
	}
}
