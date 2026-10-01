// Package memoryclient defines the typed, Fleet-scoped Memory business contract.
package memoryclient

import (
	"time"
)

const (
	Basic          = "basic"
	Advanced       = "advanced"
	MaxEntries     = 200
	MaxBatchEvents = 100
	MaxBatchBytes  = 32 * 1024
	MaxChanges     = 20
)

// Scope describes knowledge applicability or proposal context, never read/write authority.
type Scope struct {
	Workspace string `json:"workspace,omitempty"`
	Project   string `json:"project,omitempty"`
}

type Source struct {
	FleetID      string `json:"fleet_id"`
	AgentID      string `json:"agent_id"`
	ThreadID     string `json:"thread_id"`
	GenerationID string `json:"generation_id"`
	From         uint64 `json:"from"`
	Through      uint64 `json:"through"`
}

type Evidence struct {
	Source     Source    `json:"source"`
	Kind       string    `json:"kind"`
	Text       string    `json:"text"`
	RecordedAt time.Time `json:"recorded_at"`
}

type Entity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type Fact struct {
	ID         string            `json:"id"`
	Domain     string            `json:"domain"`
	Qualifiers map[string]string `json:"qualifiers,omitempty"`
	Reason     string            `json:"reason"`
	Replaces   []string          `json:"replaces,omitempty"`
	DueAt      *time.Time        `json:"due_at,omitempty"`
	TimeNote   string            `json:"time_note,omitempty"`
	Subject    string            `json:"subject"`
	Predicate  string            `json:"predicate"`
	Value      string            `json:"value,omitempty"`
	Object     string            `json:"object,omitempty"`
	Status     string            `json:"status"`
	SourceType string            `json:"source_type"`
	Sources    []Source          `json:"sources"`
	RecordedAt time.Time         `json:"recorded_at"`
	ValidFrom  *time.Time        `json:"valid_from,omitempty"`
	ValidUntil *time.Time        `json:"valid_until,omitempty"`
}

type Entry struct {
	ID        string    `json:"id"`
	Revision  uint64    `json:"revision"`
	Name      string    `json:"name"`
	Summary   string    `json:"summary"`
	Type      string    `json:"type"`
	Scope     Scope     `json:"scope"`
	Body      string    `json:"body"`
	Sources   []Source  `json:"sources"`
	Entities  []Entity  `json:"entities,omitempty"`
	Facts     []Fact    `json:"facts,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Query filters are explicit, exact metadata matches combined with AND.
// SourceAgentID matches any entry source; empty filters include all Fleet knowledge.
type Query struct {
	Domain        string     `json:"domain,omitempty"`
	Entity        string     `json:"entity,omitempty"`
	View          string     `json:"view,omitempty"`
	Status        string     `json:"status,omitempty"`
	SourceAgentID string     `json:"source_agent_id,omitempty"`
	Workspace     string     `json:"workspace,omitempty"`
	Project       string     `json:"project,omitempty"`
	Text          string     `json:"text"`
	Offset        int        `json:"offset,omitempty"`
	Limit         int        `json:"limit,omitempty"`
	Subject       string     `json:"subject,omitempty"`
	Predicate     string     `json:"predicate,omitempty"`
	At            *time.Time `json:"at,omitempty"`
}

type Page struct {
	Entries []Entry `json:"entries"`
	Next    int     `json:"next"`
	Fence   uint64  `json:"fence"`
}

type ReadRequest struct {
	ID   string     `json:"id"`
	View string     `json:"view,omitempty"`
	At   *time.Time `json:"at,omitempty"`
}

type Proposal struct {
	Key      string     `json:"key"`
	Text     string     `json:"text"`
	Reason   string     `json:"reason"`
	Sources  []Source   `json:"sources"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

type Receipt struct {
	ID         string    `json:"id"`
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	Committed  bool      `json:"committed"`
	IndexReady bool      `json:"index_ready"`
	EntryIDs   []string  `json:"entry_ids,omitempty"`
	Attempts   int       `json:"attempts"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Change struct {
	Entry            Entry  `json:"entry"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Delete           bool   `json:"delete,omitempty"`
}

type Decision struct {
	Outcome string   `json:"outcome"`
	Reason  string   `json:"reason"`
	Changes []Change `json:"changes,omitempty"`
}

type AdminRequest struct {
	Key      string   `json:"key"`
	Action   string   `json:"action"`
	Changes  []Change `json:"changes,omitempty"`
	Sources  []Source `json:"sources,omitempty"`
	EntryIDs []string `json:"entry_ids,omitempty"`
}
