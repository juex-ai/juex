package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCommandAgentStatePolicyCoversExecutableTree(t *testing.T) {
	root := newRootCmd()
	want := map[string]agentStatePolicy{
		"juex init":              agentStateNone,
		"juex doctor":            agentStateNone,
		"juex version":           agentStateNone,
		"juex bundle":            agentStateExisting,
		"juex listen":            agentStateMint,
		"juex send":              agentStateMint,
		"juex threads create":    agentStateMint,
		"juex threads list":      agentStateExisting,
		"juex threads show":      agentStateExisting,
		"juex threads rename":    agentStateExisting,
		"juex threads archive":   agentStateExisting,
		"juex threads unarchive": agentStateExisting,
		"juex threads stop":      agentStateExisting,
		"juex threads delete":    agentStateExisting,
	}
	visited := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command.Runnable() && command != root {
			path := command.CommandPath()
			policy, err := commandAgentStatePolicy(command)
			if err != nil {
				t.Errorf("%s: %v", path, err)
			} else if strings.HasPrefix(path, "juex fleet") {
				if policy != agentStateNone {
					t.Errorf("%s policy = %v", path, policy)
				}
			} else if expected, ok := want[path]; !ok {
				t.Errorf("%s missing from policy contract", path)
			} else if policy != expected {
				t.Errorf("%s policy = %v, want %v", path, policy, expected)
			}
			visited[path] = true
		}
		for _, child := range command.Commands() {
			if child.Name() != "help" && child.Name() != "completion" {
				walk(child)
			}
		}
	}
	walk(root)
	for path := range want {
		if !visited[path] {
			t.Errorf("expected command %s was not visited", path)
		}
	}
}
