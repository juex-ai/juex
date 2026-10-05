package migration

import (
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func agentConfigFixture(minimal bool) (ResolvedConfig, legacy.AgentDefinition, AgentConfigBindings) {
	modules := map[string]bool{}
	for name, enabled := range sourceModules {
		modules[name] = !minimal || enabled
	}
	files, shell, calendar, collaboration, instructions := true, true, false, false, "Resolved static instructions"
	return ResolvedConfig{AgentID: "abc234", Modules: modules, WorkerDepth: 2},
		legacy.AgentDefinition{ID: "abc234", Name: "Source Agent", Workspace: "/source/work"},
		AgentConfigBindings{ModelID: "c31b08bf-7a0c-4c8b-9b02-d3124c10f7ab", FilesEnabled: &files, ShellEnabled: &shell, CalendarEnabled: &calendar, CollaborationEnabled: &collaboration, Instructions: &instructions}
}

func TestConvertAgentConfigPreservesPolicyWithoutInferringNewAuthority(t *testing.T) {
	for _, minimal := range []bool{false, true} {
		source, definition, bindings := agentConfigFixture(minimal)
		got, err := ConvertAgentConfig(source, definition, bindings)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != definition.Name || got.ModelID != bindings.ModelID || got.Instructions != *bindings.Instructions || got.WorkerDepth != 2 || got.Capabilities == nil || got.DynamicInstructions == nil || got.DynamicInstructions.Enabled == minimal || got.DynamicInstructions.GlobalPath != "" || len(got.Hooks) != 0 {
			t.Fatal("initial Agent configuration changed", minimal)
		}
		want := agentpolicy.Policy{Disabled: []agentpolicy.Capability{agentpolicy.Calendar, agentpolicy.Collaboration}}
		if minimal {
			want.Disabled = append(want.Disabled, agentpolicy.Workers, agentpolicy.MCP, agentpolicy.Observations, agentpolicy.Memory, agentpolicy.Hooks, agentpolicy.Extensions)
		}
		if !reflect.DeepEqual(*got.Capabilities, want.Normalized()) {
			t.Fatal("incorrect grouped policy", minimal, got.Capabilities)
		}
		// Source Supervisor tools do not decide permission for peer messaging;
		// Calendar likewise has no equivalent source module switch.
		source.Modules["fleet-management"] = false
		source.Modules["mcp"] = false
		*bindings.CalendarEnabled, *bindings.CollaborationEnabled = true, true
		*bindings.Instructions = ""
		next, err := ConvertAgentConfig(source, definition, bindings)
		if err != nil || !next.Capabilities.Allows(agentpolicy.Calendar) || !next.Capabilities.Allows(agentpolicy.Collaboration) || next.Capabilities.Allows(agentpolicy.MCP) || next.Instructions != "" {
			t.Fatal("explicit target policies were ignored", err)
		}
		if got.Instructions != "Resolved static instructions" || got.Capabilities.Allows(agentpolicy.Calendar) {
			t.Fatal("converted value aliases bindings")
		}
	}
}

func TestConvertAgentConfigDisabledModulesAndDynamicSources(t *testing.T) {
	source, definition, bindings := agentConfigFixture(false)
	*bindings.FilesEnabled, *bindings.ShellEnabled = false, false
	for name := range source.Modules {
		source.Modules[name] = false
	}
	got, err := ConvertAgentConfig(source, definition, bindings)
	if err != nil || len(got.Capabilities.Disabled) != 10 || got.DynamicInstructions.Enabled {
		t.Fatal("disabled capabilities were re-enabled", err)
	}
	*bindings.FilesEnabled = true
	source.Modules["basic-file-tools"], source.Modules["agents-md"], source.UserResources = true, true, true
	bindings.GlobalInstructionPath = "/target/user/.agents/AGENTS.md"
	got, err = ConvertAgentConfig(source, definition, bindings)
	if err != nil || !got.DynamicInstructions.Enabled || got.DynamicInstructions.GlobalPath != bindings.GlobalInstructionPath {
		t.Fatal("explicit Execution-side global guidance binding was lost", err)
	}
	// User resources alone do not activate guidance when agents-md is disabled.
	source.Modules["agents-md"] = false
	bindings.GlobalInstructionPath = ""
	got, err = ConvertAgentConfig(source, definition, bindings)
	if err != nil || got.DynamicInstructions.Enabled || got.DynamicInstructions.GlobalPath != "" {
		t.Fatal("disabled dynamic guidance was activated", err)
	}
}

func TestConvertAgentConfigRequiresExplicitFilesPolicy(t *testing.T) {
	source, definition, bindings := agentConfigFixture(true)
	bindings.FilesEnabled = nil
	if _, err := ConvertAgentConfig(source, definition, bindings); err == nil {
		t.Fatal("accepted minimal file policy without explicit target grouping choice")
	}
	source.Modules["basic-file-tools"], source.Modules["file-search"] = false, true
	if _, err := ConvertAgentConfig(source, definition, bindings); err == nil {
		t.Fatal("accepted search-only policy without explicit target grouping choice")
	}
}

func TestConvertAgentConfigDoesNotSilentlyDisableSourceCommands(t *testing.T) {
	source, definition, bindings := agentConfigFixture(false)
	source.Modules["agents-md"], source.Modules["shell"] = false, false
	bindings.ShellEnabled = nil
	if _, err := ConvertAgentConfig(source, definition, bindings); err == nil {
		t.Fatal("accepted independent source Hook/Observable commands without target Shell choice")
	}
	shell := false
	bindings.ShellEnabled = &shell
	for _, module := range []string{"hooks", "observables"} {
		source.Modules["hooks"], source.Modules["observables"] = false, false
		source.Modules[module] = true
		if _, err := ConvertAgentConfig(source, definition, bindings); err == nil {
			t.Fatal("accepted source command module without required target Shell", module)
		}
	}
	shell = true
	got, err := ConvertAgentConfig(source, definition, bindings)
	if err != nil || !got.Capabilities.Allows(agentpolicy.Shell) {
		t.Fatal("explicit target Shell choice ignored", err)
	}
}

func TestConvertAgentConfigUsesExplicitFilesChoiceForIndependentSourceTools(t *testing.T) {
	for _, basic := range []bool{false, true} {
		for _, files := range []bool{false, true} {
			source, definition, bindings := agentConfigFixture(true)
			source.Modules["basic-file-tools"], source.Modules["file-search"] = basic, !basic
			bindings.FilesEnabled = &files
			got, err := ConvertAgentConfig(source, definition, bindings)
			if err != nil || got.Capabilities.Allows(agentpolicy.Files) != files {
				t.Fatal("source tool switches overrode explicit target Files choice", basic, files, err)
			}
		}
	}
	// The explicit target choice, not the old basic-file-tools switch, decides
	// whether dynamic guidance can actually run after migration.
	source, definition, bindings := agentConfigFixture(false)
	source.Modules["basic-file-tools"] = false
	if _, err := ConvertAgentConfig(source, definition, bindings); err != nil {
		t.Fatal("rejected explicit target Files enabling guidance", err)
	}
}

func TestConvertAgentConfigRejectsIncompleteOrUnrepresentableBindings(t *testing.T) {
	tests := []struct {
		name string
		edit func(*ResolvedConfig, *legacy.AgentDefinition, *AgentConfigBindings)
	}{
		{"source identity missing", func(s *ResolvedConfig, d *legacy.AgentDefinition, _ *AgentConfigBindings) { s.AgentID, d.ID = "", "" }},
		{"source identity mismatch", func(s *ResolvedConfig, _ *legacy.AgentDefinition, _ *AgentConfigBindings) { s.AgentID = "other" }},
		{"modules missing", func(s *ResolvedConfig, _ *legacy.AgentDefinition, _ *AgentConfigBindings) { s.Modules = nil }},
		{"module missing", func(s *ResolvedConfig, _ *legacy.AgentDefinition, _ *AgentConfigBindings) { delete(s.Modules, "shell") }},
		{"module unknown", func(s *ResolvedConfig, _ *legacy.AgentDefinition, _ *AgentConfigBindings) {
			s.Modules["unknown"] = true
		}},
		{"module replaced", func(s *ResolvedConfig, _ *legacy.AgentDefinition, _ *AgentConfigBindings) {
			delete(s.Modules, "shell")
			s.Modules["unknown"] = true
		}},
		{"depth missing", func(s *ResolvedConfig, _ *legacy.AgentDefinition, _ *AgentConfigBindings) { s.WorkerDepth = 0 }},
		{"depth too large", func(s *ResolvedConfig, _ *legacy.AgentDefinition, _ *AgentConfigBindings) { s.WorkerDepth = 3 }},
		{"model missing", func(_ *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) { b.ModelID = "" }},
		{"model malformed", func(_ *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) { b.ModelID = "local:model" }},
		{"model zero", func(_ *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) {
			b.ModelID = "00000000-0000-0000-0000-000000000000"
		}},
		{"model noncanonical", func(_ *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) {
			b.ModelID = strings.ToUpper(b.ModelID)
		}},
		{"calendar unspecified", func(_ *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) { b.CalendarEnabled = nil }},
		{"collaboration unspecified", func(_ *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) {
			b.CollaborationEnabled = nil
		}},
		{"instructions unresolved", func(_ *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) { b.Instructions = nil }},
		{"instructions too large", func(_ *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) {
			*b.Instructions = strings.Repeat("a", (64<<10)+1)
		}},
		{"name missing", func(_ *ResolvedConfig, d *legacy.AgentDefinition, _ *AgentConfigBindings) { d.Name = " " }},
		{"name too long", func(_ *ResolvedConfig, d *legacy.AgentDefinition, _ *AgentConfigBindings) {
			d.Name = strings.Repeat("名", 101)
		}},
		{"guidance without Files", func(_ *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) {
			*b.FilesEnabled = false
		}},
		{"global path missing", func(s *ResolvedConfig, _ *legacy.AgentDefinition, _ *AgentConfigBindings) { s.UserResources = true }},
		{"global path relative", func(s *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) {
			s.UserResources = true
			b.GlobalInstructionPath = ".agents/AGENTS.md"
		}},
		{"global path with disabled resources", func(_ *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) {
			b.GlobalInstructionPath = "/user/.agents/AGENTS.md"
		}},
		{"global path with disabled guidance", func(s *ResolvedConfig, _ *legacy.AgentDefinition, b *AgentConfigBindings) {
			s.UserResources = true
			s.Modules["agents-md"] = false
			b.GlobalInstructionPath = "/user/.agents/AGENTS.md"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source, definition, bindings := agentConfigFixture(false)
			tc.edit(&source, &definition, &bindings)
			if _, err := ConvertAgentConfig(source, definition, bindings); err == nil {
				t.Fatal("accepted incomplete or unrepresentable configuration")
			}
		})
	}
}
