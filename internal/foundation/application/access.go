// Package application defines authenticated ownership for independent Fleet apps.
package application

import (
	"context"
	"errors"
)

var (
	ErrDenied   = errors.New("application access denied or unavailable")
	ErrDisabled = errors.New("application is disabled")
	ErrInvalid  = errors.New("invalid application request")
	ErrConflict = errors.New("application state changed or request identity reused")
)

// Access is resolved by Management; AgentID is empty for human Fleet operations.
type Access struct {
	ActorID  string `json:"actor_id"`
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id"`
	AgentID  string `json:"agent_id,omitempty"`
}

type Scope struct {
	Access
	FleetID       string `json:"fleet_id"`
	ActorEpoch    int64  `json:"actor_epoch"`
	MemberEpoch   int64  `json:"member_epoch"`
	MemberVersion int64  `json:"member_version"`
	AgentEpoch    int64  `json:"agent_epoch,omitempty"`
}

// Membership versions describe profile edits; only execution epochs revoke work.
func (s Scope) SameAuthority(other Scope) bool {
	s.MemberVersion, other.MemberVersion = 0, 0
	return s == other
}

func (s Scope) Valid() bool {
	return s.ActorID != "" && s.TenantID != "" && s.UserID != "" && s.FleetID != "" && s.ActorEpoch > 0 && s.MemberEpoch > 0 && (s.AgentID == "" || s.AgentEpoch > 0)
}

type Authority interface {
	AuthorizeApplication(context.Context, Access, bool) (Scope, error)
}

// Control is owned and serialized by the application, including knowledge commits
// or Calendar occurrences. Re-enabling advances Epoch instead of reviving jobs.
type Control struct {
	Enabled bool  `json:"enabled"`
	Epoch   int64 `json:"epoch"`
	Version int64 `json:"version"`
}
