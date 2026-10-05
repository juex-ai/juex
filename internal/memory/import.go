package memory

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/memory/knowledge"
)

// ReviewHistory is provenance, not a current-protocol decision or Worker binding.
type ReviewHistory struct {
	SourceSHA256 string `json:"source_sha256"`
	Fingerprint  string `json:"fingerprint,omitempty"`
	DecisionHash string `json:"decision_hash,omitempty"`
}

// HistoricalSource retains evidence never accepted by this platform's
// participation protocol. Configuration changes cannot silently erase or run it.
type HistoricalSource struct {
	Scope            application.Scope `json:"scope"`
	ThreadID         string            `json:"thread_id"`
	SourceSHA256     string            `json:"source_sha256"`
	Epoch            string            `json:"epoch"`
	Enabled          bool              `json:"enabled"`
	AcceptedThrough  uint64            `json:"accepted_through"`
	ProcessedThrough uint64            `json:"processed_through"`
	EndedGenerations []string          `json:"ended_generations,omitempty"`
	Evidence         []mc.Evidence     `json:"evidence,omitempty"`
}

type ImportedReview struct {
	Scope     application.Scope `json:"scope"`
	ThreadID  string            `json:"thread_id"`
	Proposal  mc.Proposal       `json:"proposal"`
	Receipt   mc.Receipt        `json:"receipt"`
	Automatic bool              `json:"automatic"`
	History   ReviewHistory     `json:"history"`
}

// FleetImport is an offline snapshot. It cannot supply live jobs, commands,
// delivery acknowledgments or runnable participation under new authority.
type FleetImport struct {
	Source        string              `json:"source"`
	SourceSHA256  string              `json:"source_sha256"`
	Control       application.Control `json:"control"`
	Fence         uint64              `json:"fence"`
	Strategy      string              `json:"strategy"`
	AdvancedSince time.Time           `json:"advanced_since"`
	Entries       []mc.Entry          `json:"entries"`
	Reviews       []ImportedReview    `json:"reviews"`
	Sources       []HistoricalSource  `json:"sources"`
	Deleted       []string            `json:"deleted"`
	Suppressed    []mc.Source         `json:"suppressed"`
}

func importHash(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 32 && strings.ToLower(value) == value
}
func importScope(owner, scope application.Scope, thread string) bool {
	return scope.Valid() && scope.AgentID != "" && thread != "" && len(thread) <= 128 && scope.TenantID == owner.TenantID && scope.UserID == owner.UserID && scope.FleetID == owner.FleetID
}
func importEvidence(fleet string, evidence []mc.Evidence) error {
	bytes := 0
	for _, e := range evidence {
		bytes += len(e.Text)
		if err := mc.ValidateSource(e.Source, fleet); err != nil || e.Kind != "user" && e.Kind != "assistant" || e.RecordedAt.IsZero() || strings.TrimSpace(e.Text) == "" {
			return application.ErrInvalid
		}
	}
	if len(evidence) > mc.MaxBatchEvents || bytes > mc.MaxBatchBytes {
		return application.ErrInvalid
	}
	return nil
}

// BuildState validates the complete owner snapshot and returns an independent
// copy. No live mutation method is called, and no historical authority is rebound.
func (value FleetImport) BuildState(owner application.Scope) (*State, error) {
	if !owner.Valid() || owner.AgentID != "" || value.Source == "" || len(value.Source) > 512 || !importHash(value.SourceSHA256) || value.Control.Epoch < 1 || value.Control.Version < 1 || value.Fence < 1 || value.AdvancedSince.IsZero() || value.Strategy != mc.Basic && value.Strategy != mc.Advanced || len(value.Entries) > mc.MaxEntries {
		return nil, application.ErrInvalid
	}
	// Detach nested maps and slices from caller-owned memory before validation.
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64<<20 {
		return nil, application.ErrInvalid
	}
	var detached FleetImport
	if err := json.Unmarshal(encoded, &detached); err != nil {
		return nil, err
	}
	value = detached
	s := NewState()
	s.Control, s.Fence, s.Strategy, s.AdvancedSince = value.Control, value.Fence, value.Strategy, value.AdvancedSince
	for _, id := range value.Deleted {
		if mc.ValidateEntryID(id) != nil || s.Deleted[id] {
			return nil, application.ErrInvalid
		}
		s.Deleted[id] = true
	}
	for _, ref := range value.Suppressed {
		// Suppression can span Generations and has no executable authority.
		if ref.FleetID != owner.FleetID || ref.AgentID == "" || ref.ThreadID == "" || ref.From == 0 || ref.Through < ref.From {
			return nil, application.ErrInvalid
		}
	}
	s.Suppressed = value.Suppressed
	seen := map[string]bool{}
	for _, entry := range value.Entries {
		key := strings.ToLower(entry.ID)
		if seen[key] || entry.Revision == 0 || entry.CreatedAt.IsZero() || entry.UpdatedAt.Before(entry.CreatedAt) {
			return nil, application.ErrInvalid
		}
		if err := s.validateEntry(owner.FleetID, entry, nil, true); err != nil {
			return nil, err
		}
		seen[key], s.Entries[entry.ID] = true, entry
	}
	if err := knowledge.Validate(s.Entries); err != nil {
		return nil, invalid(err.Error())
	}
	for _, r := range value.Reviews {
		id := r.Receipt.ID
		key := r.Scope.AgentID + "/" + r.ThreadID + "/" + r.Proposal.Key
		if !importScope(owner, r.Scope, r.ThreadID) || id == "" || len(id) > 128 || s.Reviews[id] != nil || !terminal(r.Receipt.State) || r.Receipt.UpdatedAt.IsZero() || r.Receipt.Attempts < 0 || r.Proposal.Key == "" || len(r.Proposal.Key) > 128 || s.Keys[key] != "" || !importHash(r.History.SourceSHA256) || len(r.History.Fingerprint) > 128 || len(r.History.DecisionHash) > 128 || len(r.Proposal.Text)+len(r.Proposal.Reason) > mc.MaxBatchBytes || len(r.Proposal.Sources) > 100 {
			return nil, application.ErrInvalid
		}
		for _, ref := range r.Proposal.Sources {
			if err := s.validateSource(ref, owner.FleetID); err != nil {
				return nil, err
			}
			if ref.AgentID != r.Scope.AgentID || ref.ThreadID != r.ThreadID {
				return nil, application.ErrInvalid
			}
		}
		if err := importEvidence(owner.FleetID, r.Proposal.Evidence); err != nil {
			return nil, err
		}
		for _, e := range r.Proposal.Evidence {
			if !covered(e.Source, r.Proposal.Sources) {
				return nil, application.ErrInvalid
			}
		}
		for _, entry := range r.Receipt.EntryIDs {
			if mc.ValidateEntryID(entry) != nil {
				return nil, application.ErrInvalid
			}
		}
		s.Reviews[id] = &Review{ID: id, Scope: r.Scope, ThreadID: r.ThreadID, Proposal: r.Proposal, Receipt: r.Receipt, Automatic: r.Automatic, WorkerFinished: true, Imported: &r.History, Fingerprint: digest(struct {
			Proposal  mc.Proposal
			Automatic bool
		}{r.Proposal, r.Automatic})}
		s.Keys[key] = id
	}
	for _, source := range value.Sources {
		key := source.Scope.AgentID + "/" + source.ThreadID
		if !importScope(owner, source.Scope, source.ThreadID) || !importHash(source.SourceSHA256) || source.ProcessedThrough > source.AcceptedThrough || len(source.Epoch) > 128 || len(source.EndedGenerations) > 10000 {
			return nil, application.ErrInvalid
		}
		if _, exists := s.ImportedSources[key]; exists {
			return nil, application.ErrInvalid
		}
		if err := importEvidence(owner.FleetID, source.Evidence); err != nil {
			return nil, err
		}
		for _, e := range source.Evidence {
			if e.Source.AgentID != source.Scope.AgentID || e.Source.ThreadID != source.ThreadID || e.Source.Through > source.AcceptedThrough || e.Source.From <= source.ProcessedThrough || intersects(e.Source, s.Suppressed) {
				return nil, application.ErrInvalid
			}
		}
		s.ImportedSources[key] = source
		if !source.Enabled {
			s.ParticipationExcluded[key] = true
		}
	}
	return s, nil
}
