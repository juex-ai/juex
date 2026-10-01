package memory

import (
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func (s *State) Administer(scope application.Scope, request mc.AdminRequest, now time.Time) (mc.Receipt, error) {
	if scope.AgentID != "" {
		return mc.Receipt{}, application.ErrDenied
	}
	if !s.Control.Enabled {
		return mc.Receipt{}, application.ErrDisabled
	}
	if request.Key == "" || len(request.Key) > 128 || len(request.EntryIDs) > mc.MaxChanges || len(request.Sources) > 100 {
		return mc.Receipt{}, application.ErrInvalid
	}
	key := scope.ActorID + "/" + request.Key
	hash := digest(request)
	if prior, ok := s.Admin[key]; ok {
		if prior.Hash != hash {
			return mc.Receipt{}, application.ErrConflict
		}
		return prior.Receipt, nil
	}
	for _, id := range request.EntryIDs {
		if err := mc.ValidateEntryID(id); err != nil {
			return mc.Receipt{}, invalid(err.Error())
		}
	}
	for _, ref := range request.Sources {
		if err := mc.ValidateSource(ref, scope.FleetID); err != nil {
			return mc.Receipt{}, invalid(err.Error())
		}
	}
	var ids []string
	var removedSources []mc.Source
	switch request.Action {
	case "correct":
		candidate, changed, err := s.changes(scope.FleetID, request.Changes, nil, true, now)
		if err != nil {
			return mc.Receipt{}, err
		}
		for _, ch := range request.Changes {
			if ch.Delete {
				removedSources = append(removedSources, s.Entries[ch.Entry.ID].Sources...)
				s.Deleted[ch.Entry.ID] = true
			}
		}
		s.Entries, ids = candidate, changed
	case "delete":
		if len(request.EntryIDs) == 0 {
			return mc.Receipt{}, application.ErrInvalid
		}
		ids = request.EntryIDs
		for _, id := range ids {
			removedSources = append(removedSources, s.Entries[id].Sources...)
			s.Deleted[id] = true
			delete(s.Entries, id)
		}
	case "no_store":
		if len(request.Sources) == 0 {
			return mc.Receipt{}, application.ErrInvalid
		}
		removedSources = request.Sources
		for id, entry := range s.Entries {
			for _, ref := range entry.Sources {
				if intersects(ref, removedSources) {
					ids = append(ids, id)
					delete(s.Entries, id)
					s.Deleted[id] = true
					break
				}
			}
		}
	case "allow_store":
		if len(request.EntryIDs)+len(request.Sources) == 0 {
			return mc.Receipt{}, application.ErrInvalid
		}
		for _, id := range request.EntryIDs {
			delete(s.Deleted, id)
		}
		s.Suppressed = slices.DeleteFunc(s.Suppressed, func(ref mc.Source) bool { return covered(ref, request.Sources) })
	default:
		return mc.Receipt{}, application.ErrInvalid
	}
	s.Fence++
	s.AdvancedSince = now
	s.Participation = map[string]*Participation{}
	s.revoke("superseded by explicit user control", now)
	s.Suppressed = append(s.Suppressed, removedSources...)
	s.scrub(removedSources)
	slices.Sort(ids)
	receipt := mc.Receipt{ID: uuid.NewString(), State: "applied", Committed: true, IndexReady: true, EntryIDs: ids, UpdatedAt: now, Reason: "Memory change committed"}
	s.Admin[key] = AdminReceipt{Hash: hash, Receipt: receipt}
	return receipt, nil
}
