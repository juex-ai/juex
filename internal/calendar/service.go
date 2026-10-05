package calendar

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/application"
)

func invalid(reason string) error { return &application.ValidationError{Reason: reason} }

func (s *Service) transact(ctx context.Context, access application.Access, write bool, fn func(*State, application.Scope) error) error {
	scope, err := s.Authority.AuthorizeApplication(ctx, access, write || access.AgentID != "")
	if err != nil {
		return err
	}
	if scope.AgentID != "" && !scope.Capabilities.Allows(agentpolicy.Calendar) {
		return application.ErrDisabled
	}
	action := func(state *State) error {
		if !state.Control.Enabled && access.AgentID != "" {
			return application.ErrDisabled
		}
		return fn(state, scope)
	}
	if write {
		return s.Repository.Update(ctx, scope, action)
	}
	return s.Repository.View(ctx, scope, action)
}

func (s *Service) Status(ctx context.Context, access application.Access) (Status, error) {
	var value Status
	err := s.transact(ctx, access, false, func(state *State, _ application.Scope) error { value = state.Status(); return nil })
	return value, err
}

func (s *Service) Configure(ctx context.Context, access application.Access, version int64, enabled bool) (Status, error) {
	if access.AgentID != "" {
		return Status{}, application.ErrDenied
	}
	var value Status
	err := s.transact(ctx, access, true, func(state *State, _ application.Scope) error {
		if err := state.Configure(version, enabled, time.Now()); err != nil {
			return err
		}
		value = state.Status()
		return nil
	})
	return value, err
}

func (s *Service) Change(ctx context.Context, access application.Access, frozen *application.Scope, id string, q Change) (Receipt, error) {
	q, err := normalizeChange(q)
	if err != nil {
		return Receipt{}, err
	}
	scope, err := s.Authority.AuthorizeApplication(ctx, access, true)
	if err != nil {
		return Receipt{}, err
	}
	if frozen != nil && !scope.SameAuthority(*frozen) {
		return Receipt{}, application.ErrDenied
	}
	key, err := commandKey(scope, id)
	if err != nil {
		return Receipt{}, err
	}
	var repeated *Receipt
	if err := s.Repository.View(ctx, scope, func(state *State) error {
		prior, exists := state.Commands[key]
		if !exists {
			return nil
		}
		if !prior.Scope.SameAuthority(scope) || prior.Cancelled {
			return application.ErrDenied
		}
		if prior.Fingerprint != digest(q) {
			return application.ErrConflict
		}
		receipt := prior.Receipt
		repeated = &receipt
		return nil
	}); err != nil {
		return Receipt{}, err
	}
	if repeated != nil {
		return *repeated, nil
	}
	if scope.AgentID != "" && !scope.Capabilities.Allows(agentpolicy.Calendar) {
		return Receipt{}, application.ErrDisabled
	}
	if q.Action == "save" {
		var previous *Job
		if err := s.Repository.View(ctx, scope, func(state *State) error {
			if state.Control.Enabled {
				if j := state.Jobs[q.ID]; j != nil && j.Status == "active" {
					copy := *j
					previous = &copy
				}
			}
			return nil
		}); err != nil {
			return Receipt{}, err
		}
		if previous != nil {
			current, err := s.Authority.AuthorizeApplication(ctx, previous.Scope.Access, true)
			if err != nil && !errors.Is(err, application.ErrDenied) {
				return Receipt{}, err
			}
			if err != nil || !current.SameAuthority(previous.Scope) || !current.Capabilities.Allows(agentpolicy.Calendar) {
				if err := s.Repository.Update(ctx, scope, func(state *State) error {
					j := state.Jobs[q.ID]
					if j != nil && j.Version == previous.Version && j.Status == "active" {
						j.Status = "paused"
						j.PauseReason = "target_unavailable"
						j.NextAt = time.Time{}
						j.Version++
						j.UpdatedAt = time.Now()
					}
					return nil
				}); err != nil {
					return Receipt{}, err
				}
				return Receipt{}, application.ErrConflict
			}
		}
	}
	// Resolve external authority before the Calendar transaction. Scope epochs
	// are checked again at delivery and by Runtime before every model/tool call.
	target := scope
	if q.Action == "save" || q.Action == "resume" {
		var definition Definition
		if q.Action == "save" {
			if q.Definition == nil {
				return Receipt{}, application.ErrInvalid
			}
			definition = *q.Definition
		} else {
			if err := s.Repository.View(ctx, scope, func(state *State) error {
				j := state.Jobs[q.ID]
				if j == nil {
					return application.ErrDenied
				}
				definition = j.Definition
				return nil
			}); err != nil {
				return Receipt{}, err
			}
		}
		if err := definition.Validate(); err != nil {
			return Receipt{}, err
		}
		requested := access
		requested.AgentID = definition.AgentID
		target, err = s.Authority.AuthorizeApplication(ctx, requested, true)
		if err != nil {
			return Receipt{}, err
		}
		if target.FleetID != scope.FleetID || target.UserID != scope.UserID || target.TenantID != scope.TenantID || !target.Capabilities.Allows(agentpolicy.Calendar) {
			return Receipt{}, application.ErrDenied
		}
	}
	var receipt Receipt
	err = s.Repository.Update(ctx, scope, func(state *State) (err error) { receipt, err = state.Change(scope, target, id, q, time.Now()); return })
	return receipt, err
}

func (s *Service) CancelCommand(ctx context.Context, scope application.Scope, id string) error {
	return s.Repository.Update(ctx, scope, func(state *State) error { return state.CancelCommand(scope, id) })
}

type SchedulePage struct {
	Schedules []Schedule `json:"schedules"`
	Next      int        `json:"next"`
}
type OccurrencePage struct {
	Occurrences []Occurrence `json:"occurrences"`
	Next        int          `json:"next"`
}

func pageBounds(offset, limit int) bool {
	return offset >= 0 && offset <= 1<<30 && limit >= 1 && limit <= 50
}

func (s *Service) Schedules(ctx context.Context, access application.Access, offset, limit int) (SchedulePage, error) {
	page := SchedulePage{Schedules: []Schedule{}}
	if !pageBounds(offset, limit) {
		return page, application.ErrInvalid
	}
	err := s.transact(ctx, access, false, func(state *State, _ application.Scope) error {
		var all []Schedule
		for _, j := range state.Jobs {
			all = append(all, j.Schedule)
		}
		slices.SortFunc(all, func(a, b Schedule) int {
			if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
		end := min(offset+limit, len(all))
		if offset < len(all) {
			page.Schedules = all[offset:end]
		}
		if end < len(all) {
			page.Next = end
		}
		return nil
	})
	return page, err
}

func (s *Service) Occurrences(ctx context.Context, access application.Access, scheduleID string, offset, limit int) (OccurrencePage, error) {
	page := OccurrencePage{Occurrences: []Occurrence{}}
	if !pageBounds(offset, limit) {
		return page, application.ErrInvalid
	}
	err := s.transact(ctx, access, false, func(state *State, _ application.Scope) error {
		var all []Occurrence
		for _, d := range state.Deliveries {
			if scheduleID == "" || d.ScheduleID == scheduleID {
				all = append(all, d.Occurrence)
			}
		}
		slices.SortFunc(all, func(a, b Occurrence) int {
			if c := b.ScheduledAt.Compare(a.ScheduledAt); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
		end := min(offset+limit, len(all))
		if offset < len(all) {
			page.Occurrences = all[offset:end]
		}
		if end < len(all) {
			page.Next = end
		}
		return nil
	})
	return page, err
}

// Assignment is private to Runtime: it verifies the persisted Worker purpose.
func (s *Service) Assignment(ctx context.Context, frozen application.Scope, id string, epoch int64) (Delivery, error) {
	var value Delivery
	err := s.transact(ctx, frozen.Access, false, func(state *State, scope application.Scope) error {
		d := state.Deliveries[id]
		if d == nil || !scope.SameAuthority(frozen) || !scope.SameAuthority(d.Scope) || !state.Control.Enabled || epoch != state.Control.Epoch || d.Epoch != epoch || d.CancelRequested || d.Finished || d.Mode != "agent" {
			return application.ErrDenied
		}
		value = *d
		return nil
	})
	return value, err
}
