package management

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
)

func TestModulePresetDoesNotEraseExplicitLowerLayer(t *testing.T) {
	layers := ConfigurationLayers{Tenant: ConfigurationLayer{Version: 1, Declaration: Configuration{ModulePreset: "standard", Modules: map[agentpolicy.Capability]bool{agentpolicy.Notes: true, agentpolicy.ApplyPatch: false}}}, Agent: ConfigurationLayer{Version: 3, Declaration: Configuration{ModulePreset: "minimal"}}}
	value := layers.Resolve()
	if !value.Policy().Allows(agentpolicy.Files) || !value.Policy().Allows(agentpolicy.Shell) || !value.Policy().Allows(agentpolicy.Notes) || value.Policy().Allows(agentpolicy.Workers) || value.Policy().Allows(agentpolicy.ApplyPatch) || value.Policy().Allows(agentpolicy.FileSearch) || value.Policy().Allows(agentpolicy.Skills) {
		t.Fatal("preset erased override or did not supply defaults", value)
	}
	if value.Modules[agentpolicy.Workers].Source.Preset != "minimal" || value.Modules[agentpolicy.Workers].Source.Layer != "agent" || value.Modules[agentpolicy.Notes].Source.Preset != "" {
		t.Fatal("preset provenance lost", value)
	}
	layers.Agent.Declaration.ModulePreset = ""
	value = layers.Resolve()
	if !value.Policy().Allows(agentpolicy.ChunkedWrite) || !value.Policy().Allows(agentpolicy.FileSearch) || !value.Policy().CanInspectSkills() {
		t.Fatal("inherited standard did not resolve", value)
	}
	if (Configuration{ModulePreset: "all"}).Validate() == nil {
		t.Fatal("unknown preset accepted")
	}
}

func TestIndependentResourceConfiguration(t *testing.T) {
	layers := ConfigurationLayers{Fleet: ConfigurationLayer{Version: 3, Declaration: Configuration{Modules: map[agentpolicy.Capability]bool{agentpolicy.Files: false, agentpolicy.Extensions: false}}}}
	before := layers.Resolve()
	if before.Policy().Allows(agentpolicy.FileSearch) || before.Policy().Allows(agentpolicy.Skills) {
		t.Fatal("new defaults expanded existing restricted configuration", before)
	}
	layers.Agent.Declaration = Configuration{Modules: map[agentpolicy.Capability]bool{agentpolicy.FileSearch: true, agentpolicy.Skills: true}}
	current := layers.Resolve()
	if !current.Policy().Allows(agentpolicy.FileSearch) || !current.Policy().Allows(agentpolicy.Skills) || current.Policy().Allows(agentpolicy.Files) || current.Policy().Allows(agentpolicy.Extensions) {
		t.Fatal("independent permission did not resolve", current)
	}
	if !before.Policy().Restricts(current.Policy()) {
		t.Fatal("removing independent grant must fence old operations")
	}
}

func TestConfigurationJSONModuleDeclaration(t *testing.T) {
	for _, raw := range []string{`null`, `{"modules":{"files":null}}`, `{"modules":{"files":"false"}}`, `{"unknown":true}`} {
		value := Configuration{Models: []string{"unchanged"}}
		if err := json.Unmarshal([]byte(raw), &value); err == nil || !slices.Equal(value.Models, []string{"unchanged"}) {
			t.Fatalf("invalid input changed declaration: %s: %+v: %v", raw, value, err)
		}
	}
	for _, item := range []struct {
		raw               string
		explicit, enabled bool
	}{{`{}`, false, false}, {`{"modules":{}}`, false, false}, {`{"modules":{"files":false}}`, true, false}, {`{"modules":{"files":true}}`, true, true}} {
		var value Configuration
		if err := json.Unmarshal([]byte(item.raw), &value); err != nil {
			t.Fatal(item.raw, err)
		}
		if enabled, explicit := value.Modules[agentpolicy.Files]; explicit != item.explicit || enabled != item.enabled {
			t.Fatalf("lost explicit declaration: %s: %+v", item.raw, value)
		}
	}
}

func TestConfigurationPrecedenceAndSources(t *testing.T) {
	models := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	layers := ConfigurationLayers{
		Tenant:    ConfigurationLayer{Version: 1, Declaration: Configuration{Models: models[:2], Modules: map[agentpolicy.Capability]bool{agentpolicy.MCP: false, agentpolicy.Shell: false}}},
		Fleet:     ConfigurationLayer{Version: 2, Declaration: Configuration{Models: models[1:], Modules: map[agentpolicy.Capability]bool{agentpolicy.MCP: true}}},
		Workspace: ConfigurationLayer{Version: 3, Declaration: Configuration{Models: models[2:], Modules: map[agentpolicy.Capability]bool{agentpolicy.Shell: true}}},
		Agent:     ConfigurationLayer{Version: 4, Declaration: Configuration{Models: []string{models[0], models[2]}, Modules: map[agentpolicy.Capability]bool{agentpolicy.MCP: false}}},
	}
	got := layers.Resolve()
	if !slices.Equal(got.Models, []string{models[0], models[2]}) || got.ModelSource.Layer != "agent" || got.ModelSource.Version != 4 || got.Modules[agentpolicy.MCP].Enabled || got.Modules[agentpolicy.MCP].Source.Layer != "agent" || !got.Modules[agentpolicy.Shell].Enabled || got.Modules[agentpolicy.Shell].Source.Layer != "workspace" || !got.Policy().Allows(agentpolicy.Files) {
		t.Fatalf("incorrect effective settings or provenance: %+v", got)
	}
	layers.Agent.Declaration = Configuration{}
	got = layers.Resolve()
	if !slices.Equal(got.Models, models[2:]) || got.ModelSource.Layer != "workspace" || !got.Modules[agentpolicy.MCP].Enabled || got.Modules[agentpolicy.MCP].Source.Layer != "fleet" {
		t.Fatalf("remove Agent override: %+v", got)
	}
	layers.Workspace.Declaration = Configuration{}
	layers.Fleet.Declaration = Configuration{}
	got = layers.Resolve()
	if !slices.Equal(got.Models, models[:2]) || got.ModelSource.Layer != "tenant" || got.Policy().Allows(agentpolicy.Shell) {
		t.Fatalf("remove lower overrides: %+v", got)
	}
	layers.Tenant.Declaration = Configuration{}
	got = layers.Resolve()
	if len(got.Models) != 0 || got.ModelSource.Layer != "default" || !got.Policy().Allows(agentpolicy.MCP) {
		t.Fatalf("no hidden model selection: %+v", got)
	}
}

func TestConfigurationValidation(t *testing.T) {
	id := uuid.NewString()
	for _, invalid := range []Configuration{
		{Models: []string{}}, {Models: []string{id, id}}, {Models: []string{"provider:model"}}, {Models: []string{uuid.Nil.String()}},
		{Models: []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}},
		{Modules: map[agentpolicy.Capability]bool{"unknown": true}},
	} {
		if invalid.Validate() == nil {
			t.Fatalf("accepted invalid declaration: %+v", invalid)
		}
	}
	for _, valid := range []Configuration{{}, {Models: []string{id}}, {Modules: map[agentpolicy.Capability]bool{agentpolicy.MCP: false}}} {
		if err := valid.Validate(); err != nil {
			t.Fatalf("valid declaration rejected: %+v: %v", valid, err)
		}
	}
}

func TestOptionalModuleInheritanceAndRevocation(t *testing.T) {
	layers := ConfigurationLayers{}
	old := layers.Resolve().Policy()
	if old.Allows(agentpolicy.ApplyPatch) {
		t.Fatal("undeclared patch enabled")
	}
	layers.Tenant = ConfigurationLayer{Version: 1, Declaration: Configuration{Modules: map[agentpolicy.Capability]bool{agentpolicy.ApplyPatch: true}}}
	got := layers.Resolve()
	if !got.Policy().Allows(agentpolicy.ApplyPatch) || got.Modules[agentpolicy.ApplyPatch].Source.Layer != "tenant" {
		t.Fatal(got)
	}
	layers.Agent = ConfigurationLayer{Version: 2, Declaration: Configuration{Modules: map[agentpolicy.Capability]bool{agentpolicy.ApplyPatch: false}}}
	blocked := layers.Resolve()
	if blocked.Policy().Allows(agentpolicy.ApplyPatch) || !blocked.Policy().Restricts(got.Policy()) {
		t.Fatal("Agent override did not revoke patch", blocked)
	}
	layers.Agent.Declaration = Configuration{}
	if !layers.Resolve().Policy().Allows(agentpolicy.ApplyPatch) {
		t.Fatal("removing override did not restore inheritance")
	}
}
