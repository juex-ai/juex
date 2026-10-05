// Package lifecycle defines the immutable, private service cleanup protocol.
package lifecycle

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
)

var ErrInvalid = errors.New("invalid cleanup request")

const (
	Fence = "fence"
	Erase = "erase"
)

// Target is frozen when the owner or administrator accepts permanent deletion.
// It never resolves a resource again by its display name or current membership.
type Target struct {
	ID         string   `json:"id"`
	TenantID   string   `json:"tenant_id"`
	UserID     string   `json:"user_id"`
	FleetID    string   `json:"fleet_id"`
	AgentIDs   []string `json:"agent_ids"`
	WholeFleet bool     `json:"whole_fleet"`
}

func (t Target) Valid() bool {
	for _, id := range append([]string{t.ID, t.TenantID, t.UserID, t.FleetID}, t.AgentIDs...) {
		v, err := uuid.Parse(id)
		if err != nil || v == uuid.Nil || v.String() != id {
			return false
		}
	}
	if !t.WholeFleet && len(t.AgentIDs) != 1 {
		return false
	}
	return slices.IsSorted(t.AgentIDs) && len(slices.Compact(slices.Clone(t.AgentIDs))) == len(t.AgentIDs)
}

func (t Target) Contains(agent string) bool {
	return t.WholeFleet || slices.Contains(t.AgentIDs, agent)
}

type Request struct {
	Target
	Phase string `json:"phase"`
}

func (r Request) Valid() bool { return r.Target.Valid() && (r.Phase == Fence || r.Phase == Erase) }

// Unconfirmed counts original operations whose stopping could not be proved.
// DataRemoved never implies that a user's disconnected computer has stopped.
type Receipt struct {
	Fenced        bool `json:"fenced"`
	DataRemoved   bool `json:"data_removed"`
	Unconfirmed   int  `json:"unconfirmed"`
	EnvironmentsPending int  `json:"environments_pending"`
}

type Participant interface {
	Purge(context.Context, Request) (Receipt, error)
}
