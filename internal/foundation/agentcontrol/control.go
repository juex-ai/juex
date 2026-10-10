// Package agentcontrol defines the narrow private protocol for explicitly
// authorized Agents to manage other Agents in the same Fleet.
package agentcontrol

import (
	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/instructionpolicy"
)

type Source struct {
	ActorID         string `json:"actor_id"`
	TenantID        string `json:"tenant_id"`
	UserID          string `json:"user_id"`
	FleetID         string `json:"fleet_id"`
	AgentID         string `json:"agent_id"`
	ThreadID        string `json:"thread_id"`
	ActionID        string `json:"action_id"`
	ActorEpoch      int64  `json:"actor_epoch"`
	MembershipEpoch int64  `json:"membership_epoch"`
	AgentEpoch      int64  `json:"agent_epoch"`
}

func (s Source) Valid() bool {
	for _, id := range []string{s.ActorID, s.TenantID, s.UserID, s.FleetID, s.AgentID, s.ThreadID, s.ActionID} {
		if u, err := uuid.Parse(id); err != nil || u == uuid.Nil || u.String() != id {
			return false
		}
	}
	return s.ActorEpoch > 0 && s.MembershipEpoch > 0 && s.AgentEpoch > 0
}

// Patch cannot change grants, credentials, devices, Hooks or extension sources.
// Missing fields preserve existing values; null module values remove overrides.
type Patch struct {
	Name                *string                                `json:"name,omitempty"`
	Instructions        *string                                `json:"instructions,omitempty"`
	WorkerDepth         *int                                   `json:"worker_depth,omitempty"`
	Models              *[]string                              `json:"models,omitempty"`
	ModulePreset        *string                                `json:"module_preset,omitempty"`
	Modules             map[agentpolicy.Capability]*bool       `json:"modules,omitempty"`
	DynamicInstructions *instructionpolicy.DynamicInstructions `json:"dynamic_instructions,omitempty"`
}

type Action struct {
	Kind    string `json:"kind"`
	AgentID string `json:"agent_id,omitempty"`
	Version int64  `json:"version,omitempty"`
	Patch   *Patch `json:"patch,omitempty"`
}

func (a Action) Valid() bool {
	if a.Patch == nil {
		return false
	}
	if a.Patch.WorkerDepth != nil && (*a.Patch.WorkerDepth < 1 || *a.Patch.WorkerDepth > 2) {
		return false
	}
	if a.Kind == "create" {
		return a.AgentID == "" && a.Version == 0 && a.Patch.Name != nil
	}
	if a.Kind != "configure" || a.Version < 1 {
		return false
	}
	id, err := uuid.Parse(a.AgentID)
	return err == nil && id != uuid.Nil && id.String() == a.AgentID
}

// Receipt is safe to recover after revocation: it contains no private settings.
type Receipt struct {
	ActionID string `json:"action_id"`
	AgentID  string `json:"agent_id,omitempty"`
	Version  int64  `json:"version,omitempty"`
	Outcome  string `json:"outcome"`
	Error    string `json:"error,omitempty"`
}

type Agent struct {
	ID                  string                                `json:"id"`
	Name                string                                `json:"name"`
	Status              string                                `json:"status"`
	Version             int64                                 `json:"version"`
	Instructions        string                                `json:"instructions"`
	WorkerDepth         int                                   `json:"worker_depth"`
	Models              []string                              `json:"models"`
	ModulePreset        string                                `json:"module_preset"`
	Modules             map[agentpolicy.Capability]bool       `json:"modules"`
	DynamicInstructions instructionpolicy.DynamicInstructions `json:"dynamic_instructions"`
}

type Page struct {
	Agents []Agent `json:"agents"`
	Next   string  `json:"next,omitempty"`
}
