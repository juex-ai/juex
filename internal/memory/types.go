// Package memory owns shared Fleet knowledge and bounded review assignments.
package memory

import (
	"context"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

// Repository runs a mutation under the Fleet's transaction lock. An error rolls
// back all changes, including receipts, fences, suppression and knowledge.
type Repository interface {
	View(context.Context, application.Scope, func(*State) error) error
	Update(context.Context, application.Scope, func(*State) error) error
}

type Service struct {
	Repository Repository
	Authority  application.Authority
	Workers    WorkerGateway
}

type Review struct {
	SourceThrough  uint64            `json:"source_through,omitempty"`
	ID             string            `json:"id"`
	Scope          application.Scope `json:"scope"`
	ThreadID       string            `json:"source_thread_id"`
	Proposal       mc.Proposal       `json:"proposal"`
	Epoch          int64             `json:"epoch"`
	Fence          uint64            `json:"fence"`
	Receipt        mc.Receipt        `json:"receipt"`
	Fingerprint    string            `json:"fingerprint"`
	DecisionHash   string            `json:"decision_hash,omitempty"`
	Automatic      bool              `json:"automatic"`
	WorkerID       string            `json:"worker_id,omitempty"`
	WorkerFinished bool              `json:"worker_finished"`
	AttemptedAt    time.Time         `json:"attempted_at"`
}

// Binding is injected by Runtime from the persisted application Worker purpose.
// It is never accepted as part of a model tool's arguments.
type Binding struct {
	ReviewID string `json:"review_id"`
	Epoch    int64  `json:"epoch"`
	Fence    uint64 `json:"fence"`
}

type AdminReceipt struct {
	Hash    string     `json:"hash"`
	Receipt mc.Receipt `json:"receipt"`
}

type State struct {
	AdvancedSince time.Time                 `json:"advanced_since"`
	Participation map[string]*Participation `json:"participation"`
	Commands      map[string]CommandReceipt `json:"commands"`
	Control       application.Control       `json:"control"`
	Fence         uint64                    `json:"fence"`
	Strategy      string                    `json:"strategy"`
	Entries       map[string]mc.Entry       `json:"entries"`
	Reviews       map[string]*Review        `json:"reviews"`
	Keys          map[string]string         `json:"keys"`
	Admin         map[string]AdminReceipt   `json:"admin"`
	Deleted       map[string]bool           `json:"deleted"`
	Suppressed    []mc.Source               `json:"suppressed"`
}

func NewState() *State {
	return &State{Participation: map[string]*Participation{}, Control: application.Control{Enabled: true, Epoch: 1, Version: 1}, Fence: 1, Strategy: mc.Basic,
		Entries: map[string]mc.Entry{}, Reviews: map[string]*Review{}, Keys: map[string]string{}, Admin: map[string]AdminReceipt{}, Deleted: map[string]bool{}, Commands: map[string]CommandReceipt{}}
}

type Status struct {
	application.Control
	Strategy string `json:"strategy"`
	Fence    uint64 `json:"fence"`
	Entries  int    `json:"entries"`
	Pending  int    `json:"pending"`
}

func (s *State) Status() Status {
	r := Status{Control: s.Control, Strategy: s.Strategy, Fence: s.Fence, Entries: len(s.Entries)}
	for _, w := range s.Reviews {
		if !terminal(w.Receipt.State) {
			r.Pending++
		}
	}
	return r
}

func terminal(state string) bool {
	return state == "applied" || state == "no_change" || state == "rejected" || state == "failed"
}

func (s *State) Configure(version int64, enabled bool, strategy string, now time.Time) error {
	if version != s.Control.Version {
		return application.ErrConflict
	}
	if strategy != mc.Basic && strategy != mc.Advanced {
		return application.ErrInvalid
	}
	if enabled != s.Control.Enabled || strategy != s.Strategy {
		s.AdvancedSince = now
		s.Participation = map[string]*Participation{}
		s.Control.Epoch++
		s.revoke("application configuration changed", now)
	}
	s.Control.Enabled = enabled
	s.Control.Version++
	s.Strategy = strategy
	return nil
}

func (s *State) revoke(reason string, now time.Time) {
	for _, w := range s.Reviews {
		if !terminal(w.Receipt.State) {
			w.Receipt.State = "rejected"
			w.Receipt.Reason = reason
			w.Receipt.UpdatedAt = now
		}
	}
}
