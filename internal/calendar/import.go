package calendar

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/calendar/recurrence"
	"github.com/juex-ai/juex/internal/foundation/application"
)

// ImportedSchedule carries a definition and a proven scheduling cursor, never
// an old delivery, command, execution epoch or acceptance receipt.
type ImportedSchedule struct {
	ID string `json:"id"`
	Definition
	Scope   application.Scope `json:"scope"`
	Enabled bool              `json:"enabled"`
	Clock   recurrence.State  `json:"clock"`
	NextAt  time.Time         `json:"next_at,omitempty"`
}

// FleetImport is a complete offline snapshot. CapturedAt is fixed by the
// operator and makes the target's new metadata stable across import retries.
type FleetImport struct {
	Source       string             `json:"source"`
	SourceSHA256 string             `json:"source_sha256"`
	CapturedAt   time.Time          `json:"captured_at"`
	Schedules    []ImportedSchedule `json:"schedules"`
}

// BuildState establishes fresh Calendar epochs without scheduling or notifying.
// The caller supplies freshly authorized Agent scopes from the target Fleet.
func (value FleetImport) BuildState(owner application.Scope) (*State, error) {
	hash, err := hex.DecodeString(value.SourceSHA256)
	if !owner.Valid() || owner.AgentID != "" || strings.TrimSpace(value.Source) == "" || len(value.Source) > 512 || err != nil || len(hash) != 32 || strings.ToLower(value.SourceSHA256) != value.SourceSHA256 || value.CapturedAt.IsZero() || len(value.Schedules) > 1000 {
		return nil, application.ErrInvalid
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64<<20 {
		return nil, application.ErrInvalid
	}
	var detached FleetImport
	if err := json.Unmarshal(encoded, &detached); err != nil {
		return nil, err
	}
	value = detached
	state := NewState()
	for _, item := range value.Schedules {
		scope := item.Scope
		if !scope.Valid() || scope.AgentID == "" || scope.TenantID != owner.TenantID || scope.UserID != owner.UserID || scope.FleetID != owner.FleetID || item.Mode != "main" || item.AgentID != scope.AgentID || item.CatchUp == "" || state.Jobs[item.ID] != nil {
			return nil, application.ErrInvalid
		}
		if _, err := uuid.Parse(item.ID); err != nil {
			return nil, application.ErrInvalid
		}
		if err := item.Validate(); err != nil {
			return nil, err
		}
		clock := item.Clock
		if clock.LastEvaluatedAt.IsZero() || clock.LastEvaluatedAt.After(value.CapturedAt) || clock.LastEmittedScheduledAt.After(clock.LastEvaluatedAt) || clock.Anchor.After(clock.LastEvaluatedAt) {
			return nil, application.ErrInvalid
		}
		job := &Job{Schedule: Schedule{ID: item.ID, Definition: item.Definition, Version: 1, Status: "paused", PauseReason: "manual", UpdatedAt: value.CapturedAt}, Scope: scope, Clock: clock, Epoch: state.Control.Epoch}
		if item.Enabled {
			next, found, err := recurrence.Next(item.Rule, clock, clock.LastEvaluatedAt)
			if err != nil || found == item.NextAt.IsZero() || found && !next.Equal(item.NextAt) {
				return nil, application.ErrInvalid
			}
			job.Status, job.PauseReason, job.NextAt = "active", "", item.NextAt
			if !found {
				job.Status = "completed"
			}
		} else if !item.NextAt.IsZero() {
			return nil, application.ErrInvalid
		}
		state.Jobs[item.ID] = job
	}
	return state, nil
}
