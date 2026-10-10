package postgres

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/management"
)

func TestAgentImportProofBindsCompleteConfiguration(t *testing.T) {
	models := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	value := management.AgentsImport{ExpectedFleetID: uuid.NewString(), Source: "proof", SourceSHA256: strings.Repeat("a", 64), Agents: []management.ImportedAgent{{SourceAgentID: "one", Config: management.AgentConfig{Name: "Agent", Configuration: &management.Configuration{Models: models, Modules: map[agentpolicy.Capability]bool{agentpolicy.MCP: false}}}}}}
	_, want, err := prepareAgentImport(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*management.Configuration){
		func(c *management.Configuration) { slices.Reverse(c.Models[1:]) },
		func(c *management.Configuration) { c.Models = c.Models[:2] },
		func(c *management.Configuration) { delete(c.Modules, agentpolicy.MCP) },
		func(c *management.Configuration) { c.Modules[agentpolicy.MCP] = true },
		func(c *management.Configuration) { c.Modules[agentpolicy.Files] = true },
	} {
		other := value
		other.Agents = slices.Clone(value.Agents)
		declaration := value.Agents[0].Config.Configuration.Clone()
		edit(&declaration)
		other.Agents[0].Config.Configuration = &declaration
		_, got, err := prepareAgentImport(other)
		if err != nil || got == want {
			t.Fatal("configuration edit lost from proof", err)
		}
	}
}
