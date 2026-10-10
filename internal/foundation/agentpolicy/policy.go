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
	Files          Capability = "files"
	FileSearch     Capability = "file-search"
	Skills         Capability = "skills"
	Shell          Capability = "shell"
	Workers        Capability = "workers"
	Collaboration  Capability = "collaboration"
	MCP            Capability = "mcp"
	Observations   Capability = "observations"
	Memory         Capability = "memory"
	Calendar       Capability = "calendar"
	Hooks          Capability = "hooks"
	Extensions     Capability = "extensions"
	Notes          Capability = "notes"
	Tasks          Capability = "tasks"
	ContextControl Capability = "context-control"
	WorkingFiles   Capability = "working-files"
	InputTracking  Capability = "input_tracking"
	ApplyPatch     Capability = "apply-patch"
	ChunkedWrite   Capability = "chunked-write"
)

// The zero policy retains the existing Agent behavior. Unknown capabilities
// fail closed, including callers compiled against a newer protocol.
type Policy struct {
	Disabled     []Capability `json:"disabled"`
	Enabled      []Capability `json:"enabled,omitempty"`
	Version      int          `json:"version,omitempty"`
	SkillSources bool         `json:"skill_sources,omitempty"`
}

func Capabilities() []Capability {
	return []Capability{Files, FileSearch, Skills, Shell, Workers, Collaboration, MCP, Observations, Memory, Calendar, Hooks, Extensions, Notes, Tasks, ContextControl, WorkingFiles, InputTracking, ApplyPatch, ChunkedWrite}
}

func known(capability Capability) bool {
	switch capability {
	case Files, FileSearch, Skills, Shell, Workers, Collaboration, MCP, Observations, Memory, Calendar, Hooks, Extensions, Notes, Tasks, ContextControl, WorkingFiles, InputTracking, ApplyPatch, ChunkedWrite:
		return true
	default:
		return false
	}
}

func (p Policy) Allows(capability Capability) bool {
	if p.Version < 0 || p.Version > 1 {
		return false
	}
	// Persisted requests predating independent resource permissions retain the
	// authority with which their tool catalog and operation were frozen.
	if p.Version == 0 {
		switch capability {
		case FileSearch:
			return p.Allows(Files)
		case Skills:
			return p.Allows(Extensions)
		}
	}
	return known(capability) && !slices.Contains(p.Disabled, capability) && (DefaultEnabled(capability) || slices.Contains(p.Enabled, capability))
}

// Optional tools need an explicit grant; upgrading services must not expand an
// existing frozen Turn's authority or enable them in an undeclared layer.
func DefaultEnabled(capability Capability) bool {
	return known(capability) && capability != ApplyPatch && capability != ChunkedWrite
}

func (p Policy) Validate() error {
	if p.Version < 0 || p.Version > 1 {
		return fmt.Errorf("unsupported Agent policy version %d", p.Version)
	}
	seen := make(map[Capability]bool, len(p.Disabled))
	if p.Version == 0 && p.SkillSources || p.SkillSources && !p.Allows(Skills) {
		return fmt.Errorf("invalid skill source authority")
	}
	for _, capability := range p.Disabled {
		if !known(capability) || seen[capability] || p.Version == 0 && (capability == Skills || capability == FileSearch) {
			return fmt.Errorf("invalid or repeated Agent capability %q", capability)
		}
		seen[capability] = true
	}
	for _, capability := range p.Enabled {
		if !known(capability) || DefaultEnabled(capability) || seen[capability] {
			return fmt.Errorf("invalid or repeated optional Agent capability %q", capability)
		}
		seen[capability] = true
	}
	return nil
}

func (p Policy) Normalized() Policy {
	result := Policy{Version: p.Version, SkillSources: p.SkillSources, Disabled: append([]Capability{}, p.Disabled...), Enabled: slices.Clone(p.Enabled)}
	slices.Sort(result.Disabled)
	slices.Sort(result.Enabled)
	return result
}

// Restricts reports any newly disabled capability. Enabling a capability never
// revives authority belonging to an earlier execution epoch.
func (p Policy) Restricts(previous Policy) bool {
	if previous.CanInspectSkills() && !p.CanInspectSkills() {
		return true
	}
	for _, capability := range Capabilities() {
		if previous.Allows(capability) && !p.Allows(capability) {
			return true
		}
	}
	return false
}

// Reading a new skills directory requires file authority or an independently
// granted Skills source. Existing frozen extension guidance grants neither.
func (p Policy) CanInspectSkills() bool {
	return p.Version == 1 && p.Allows(Skills) && (p.Allows(Files) || p.SkillSources)
}
