// Package managedruntime owns durable Agent activations, inputs and Threads.
package managedruntime

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

var (
	ErrDenied           = errors.New("runtime resource unavailable or access denied")
	ErrInvalid          = errors.New("invalid runtime request")
	ErrConflict         = errors.New("runtime state changed or request identity reused")
	ErrFence            = errors.New("activation lease is no longer current")
	ErrNoWork           = errors.New("no runnable work")
	ErrModelUnavailable = errors.New("selected model unavailable")
)

// Scope comes from current Management authority, never a browser request body.
type Scope struct {
	TenantID                 string `json:"tenant_id"`
	UserID                   string `json:"user_id"`
	FleetID                  string `json:"fleet_id"`
	AgentID                  string `json:"agent_id"`
	ActorID                  string `json:"actor_id"`
	ActorAuthorizationEpoch  int64  `json:"actor_authorization_epoch"`
	MembershipVersion        int64  `json:"membership_version"`
	MembershipExecutionEpoch int64  `json:"membership_execution_epoch"`
	AgentExecutionEpoch      int64  `json:"agent_execution_epoch"`
}

type Thread struct {
	ID            string    `json:"id"`
	AgentID       string    `json:"agent_id"`
	ParentID      string    `json:"parent_id"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name"`
	Retention     string    `json:"retention"`
	State         string    `json:"state"`
	Generation    int64     `json:"generation"`
	Sequence      int64     `json:"sequence"`
	PendingInputs int64     `json:"pending_inputs"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type InputRequest struct {
	RequestID string `json:"request_id"`
	ThreadID  string `json:"thread_id"`
	Text      string `json:"text"`
}

type InputReceipt struct {
	ID         string    `json:"id"`
	RequestID  string    `json:"request_id"`
	ThreadID   string    `json:"thread_id"`
	State      string    `json:"state"`
	AcceptedAt time.Time `json:"accepted_at"`
}

type Event struct {
	ID         string          `json:"id"`
	ThreadID   string          `json:"thread_id"`
	Sequence   int64           `json:"sequence"`
	Generation int64           `json:"generation"`
	Kind       string          `json:"kind"`
	Data       json.RawMessage `json:"data"`
	CreatedAt  time.Time       `json:"created_at"`
}

type Timeline struct {
	Thread       Thread  `json:"thread"`
	Events       []Event `json:"events"`
	NextSequence int64   `json:"next_sequence"`
	HasMore      bool    `json:"has_more"`
}

type Lease struct {
	AgentID   string
	Holder    string
	Epoch     int64
	ExpiresAt time.Time
}

type TurnConfig struct {
	AgentVersion  int64        `json:"agent_version"`
	Instructions  string       `json:"instructions"`
	ModelID       string       `json:"model_id"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	Protocol      llm.Protocol `json:"protocol"`
	Endpoint      string       `json:"endpoint"`
	ContextWindow int          `json:"context_window"`
	MaxOutput     int          `json:"max_output"`
}

type Work struct {
	Scope      Scope
	ThreadID   string
	InputID    string
	TurnID     string
	Generation int64
	Config     TurnConfig
	History    []llm.Message
}

type PendingWork struct {
	Scope    Scope
	InputID  string
	ThreadID string
	State    string
}

// SameAuthority ignores descriptive configuration versions. Execution epochs
// also detect a revoke-and-restore that happened between two observations.
func (s Scope) SameAuthority(other Scope) bool {
	return s.TenantID == other.TenantID && s.UserID == other.UserID && s.FleetID == other.FleetID && s.AgentID == other.AgentID && s.ActorID == other.ActorID && s.ActorAuthorizationEpoch == other.ActorAuthorizationEpoch && s.MembershipExecutionEpoch == other.MembershipExecutionEpoch && s.AgentExecutionEpoch == other.AgentExecutionEpoch
}

type Attempt struct {
	ID      string
	TurnID  string
	Ordinal int
}

type ModelRequest struct {
	System   string         `json:"system"`
	Messages []llm.Message  `json:"messages"`
	Tools    []llm.ToolSpec `json:"tools"`
	Purpose  string         `json:"purpose"`
}
