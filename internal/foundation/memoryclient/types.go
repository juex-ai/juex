// Package memoryclient defines the typed, Fleet-scoped Memory business contract.
package memoryclient

import (
	"context"
	"time"
)

const (
	ProfileAgent      = "agent"
	ProfileSupervisor = "supervisor"
	ProfileUser       = "user"
	Basic             = "basic"
	Advanced          = "advanced"
	MaxEntries        = 200
	MaxBatchEvents    = 100
	MaxBatchBytes     = 32 * 1024
	MaxChanges        = 20
)

// Scope describes knowledge applicability or proposal context, never read/write authority.
type Scope struct {
	Workspace string `json:"workspace,omitempty"`
	Project   string `json:"project,omitempty"`
}

// Caller is frozen by trusted composition, never taken from model arguments.
// Profiles are business capabilities, not identity authentication.
type Caller struct {
	FleetID      string `json:"fleet_id"`
	AgentID      string `json:"agent_id"`
	ThreadID     string `json:"thread_id"`
	Profile      string `json:"profile"`
	Scope        Scope  `json:"scope"`
	Purpose      string `json:"purpose,omitempty"`
	AssignmentID string `json:"assignment_id,omitempty"`
	Token        string `json:"token,omitempty"`
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

type Assignment struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Proposal  Proposal  `json:"proposal"`
	Scope     Scope     `json:"scope"`
	Automatic bool      `json:"automatic"`
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

type Failure struct {
	Reason string `json:"reason"`
}

type Settlement struct {
	Confirmed bool `json:"confirmed"`
	Released  int  `json:"released"`
}

// SourceBatch advances a continuous committed cursor, including events which
// carry no usable evidence. Evidence itself is restricted to original dialogue.
type SourceBatch struct {
	ThreadID         string     `json:"thread_id"`
	Epoch            string     `json:"epoch"`
	After            uint64     `json:"after"`
	Through          uint64     `json:"through"`
	EndedGenerations []string   `json:"ended_generations"`
	Evidence         []Evidence `json:"evidence"`
	IdleSince        time.Time  `json:"idle_since"`
	Pending          int        `json:"pending"`
	Maintenance      bool       `json:"maintenance"`
}

type Boundary struct {
	ThreadID string `json:"thread_id"`
	Epoch    string `json:"epoch"`
	Cursor   uint64 `json:"cursor"`
	Enabled  bool   `json:"enabled"`
}

type SourceState struct {
	AcceptedThrough  uint64 `json:"accepted_through"`
	ProcessedThrough uint64 `json:"processed_through"`
	Epoch            string `json:"epoch"`
	Enabled          bool   `json:"enabled"`
}

type Recall struct {
	Strategy string  `json:"strategy"`
	Fence    uint64  `json:"fence"`
	Entries  []Entry `json:"entries"`
}

type AdminRequest struct {
	Key      string   `json:"key"`
	Action   string   `json:"action"`
	Changes  []Change `json:"changes,omitempty"`
	Sources  []Source `json:"sources,omitempty"`
	EntryIDs []string `json:"entry_ids,omitempty"`
}

type Status struct {
	Strategy   string `json:"strategy"`
	Fence      uint64 `json:"fence"`
	Entries    int    `json:"entries"`
	Pending    int    `json:"pending"`
	Running    int    `json:"running"`
	IndexReady bool   `json:"index_ready"`
}

// API contains Memory operations only; it is not a Fleet forwarding interface.
type API interface {
	Domains(context.Context, Caller, DomainRequest) ([]Domain, error)
	Facts(context.Context, Caller, Query) (FactPage, error)
	Status(context.Context, Caller) (Status, error)
	Search(context.Context, Caller, Query) (Page, error)
	Read(context.Context, Caller, ReadRequest) (Entry, error)
	Propose(context.Context, Caller, Proposal) (Receipt, error)
	Result(context.Context, Caller, string) (Receipt, error)
	Claim(context.Context, Caller) (*Assignment, error)
	Decide(context.Context, Caller, Decision) (Receipt, error)
	Fail(context.Context, Caller, Failure) (Receipt, error)
	Revoke(context.Context, Caller, string) (Settlement, error)
	Participation(context.Context, Caller, Boundary) (SourceState, error)
	Contribute(context.Context, Caller, SourceBatch) (SourceState, error)
	Maintain(context.Context, Caller, string) (Receipt, error)
	History(context.Context, Caller, Source) ([]Evidence, error)
	Recall(context.Context, Caller, Query) (Recall, error)
	Admin(context.Context, Caller, AdminRequest) (Receipt, error)
}
