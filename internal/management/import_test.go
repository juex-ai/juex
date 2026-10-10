package management

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
)

func TestAgentsImportRejectsInvalidUTF8(t *testing.T) {
	for _, field := range []string{"source", "identity", "name", "instructions", "hook command", "hook environment"} {
		t.Run(field, func(t *testing.T) {
			value := AgentsImport{ExpectedFleetID: uuid.NewString(), Source: "backup", SourceSHA256: strings.Repeat("a", 64), Agents: []ImportedAgent{{SourceAgentID: "first", Config: AgentConfig{Name: "Agent"}}}}
			invalid := string([]byte{0xff})
			switch field {
			case "source":
				value.Source = invalid
			case "identity":
				value.Agents[0].SourceAgentID = invalid
				value.Agents = append(value.Agents, ImportedAgent{SourceAgentID: string([]byte{0xfe}), Config: AgentConfig{Name: "Other"}})
			case "name":
				value.Agents[0].Config.Name = invalid
			case "instructions":
				value.Agents[0].Config.Instructions = invalid
			case "hook command", "hook environment":
				hook := hookpolicy.Declaration{ID: "hook", Events: []hookpolicy.Event{hookpolicy.Stop}, Command: []string{"/bin/tool"}}
				if field == "hook command" {
					hook.Command = append(hook.Command, invalid)
				} else {
					hook.Environment = map[string]string{invalid: "value"}
				}
				value.Agents[0].Config.Hooks = []hookpolicy.Declaration{hook}
			}
			if err := value.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid UTF-8 accepted", err)
			}
		})
	}
}

func TestAgentsImportValidation(t *testing.T) {
	valid := func() AgentsImport {
		return AgentsImport{ExpectedFleetID: uuid.NewString(), Source: "backup", SourceSHA256: strings.Repeat("a", 64), Agents: []ImportedAgent{{SourceAgentID: "source", Config: AgentConfig{Name: "Agent"}}}}
	}
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*AgentsImport){
		"missing Fleet":          func(v *AgentsImport) { v.ExpectedFleetID = "" },
		"zero Fleet":             func(v *AgentsImport) { v.ExpectedFleetID = uuid.Nil.String() },
		"noncanonical Fleet":     func(v *AgentsImport) { v.ExpectedFleetID = "{" + v.ExpectedFleetID + "}" },
		"empty source":           func(v *AgentsImport) { v.Source = "  " },
		"long source":            func(v *AgentsImport) { v.Source = strings.Repeat("x", 513) },
		"invalid hash":           func(v *AgentsImport) { v.SourceSHA256 = strings.Repeat("z", 64) },
		"uppercase hash":         func(v *AgentsImport) { v.SourceSHA256 = strings.Repeat("A", 64) },
		"short hash":             func(v *AgentsImport) { v.SourceSHA256 = "aa" },
		"no Agents":              func(v *AgentsImport) { v.Agents = nil },
		"too many Agents":        func(v *AgentsImport) { v.Agents = make([]ImportedAgent, 1001) },
		"no source identity":     func(v *AgentsImport) { v.Agents[0].SourceAgentID = "" },
		"padded source identity": func(v *AgentsImport) { v.Agents[0].SourceAgentID = " source " },
		"long source identity":   func(v *AgentsImport) { v.Agents[0].SourceAgentID = strings.Repeat("x", 129) },
		"duplicate identity":     func(v *AgentsImport) { v.Agents = append(v.Agents, v.Agents[0]) },
		"invalid configuration":  func(v *AgentsImport) { v.Agents[0].Config.Name = "" },
		"invalid policy": func(v *AgentsImport) {
			v.Agents[0].Config.Configuration = &Configuration{Modules: map[agentpolicy.Capability]bool{"unknown": true}}
		},
		"invalid model": func(v *AgentsImport) { v.Agents[0].Config.Configuration = &Configuration{Models: []string{"model"}} },
		"zero model": func(v *AgentsImport) {
			v.Agents[0].Config.Configuration = &Configuration{Models: []string{uuid.Nil.String()}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := valid()
			change(&value)
			if err := value.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid import accepted", err)
			}
		})
	}
}
