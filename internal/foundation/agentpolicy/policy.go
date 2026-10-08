// Package agentpolicy describes the capabilities permitted for an Agent.
// Each service owns enforcement for its operations; these switches do not
// restrict the OS permissions of a native shell.
package agentpolicy

import (
	"fmt"
	"slices"
)

type Capability string

const (
	Files         Capability = "files"
	Shell         Capability = "shell"
	Workers       Capability = "workers"
	Collaboration Capability = "collaboration"
	MCP           Capability = "mcp"
	Observations  Capability = "observations"
	Memory        Capability = "memory"
	Calendar      Capability = "calendar"
	Hooks         Capability = "hooks"
	Extensions    Capability = "extensions"
)

// The zero policy retains the existing Agent behavior. Unknown capabilities
// fail closed, including callers compiled against a newer protocol.
type Policy struct {
	Disabled []Capability `json:"disabled"`
}

func known(capability Capability) bool {
	switch capability {
	case Files, Shell, Workers, Collaboration, MCP, Observations, Memory, Calendar, Hooks, Extensions:
		return true
	default:
		return false
	}
}

func (p Policy) Allows(capability Capability) bool {
	return known(capability) && !slices.Contains(p.Disabled, capability)
}

func (p Policy) Validate() error {
	seen := make(map[Capability]bool, len(p.Disabled))
	for _, capability := range p.Disabled {
		if !known(capability) || seen[capability] {
			return fmt.Errorf("invalid or repeated Agent capability %q", capability)
		}
		seen[capability] = true
	}
	return nil
}

func (p Policy) Normalized() Policy {
	result := Policy{Disabled: append([]Capability{}, p.Disabled...)}
	slices.Sort(result.Disabled)
	return result
}

// Restricts reports any newly disabled capability. Enabling a capability never
// revives authority belonging to an earlier execution epoch.
func (p Policy) Restricts(previous Policy) bool {
	for _, capability := range p.Disabled {
		if previous.Allows(capability) {
			return true
		}
	}
	return false
}
