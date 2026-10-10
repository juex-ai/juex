package managedruntime

import (
	"context"
	"errors"
)

// ErrPaused is temporary. Application outboxes retain their work for resume;
// callers must not translate it into revocation or permanent rejection.
var ErrPaused = errors.New("agent processing is paused")

type AgentRunState struct {
	Initialized     bool     `json:"initialized"`
	AgentID         string   `json:"agent_id"`
	Mode            string   `json:"mode"`
	Version         int64    `json:"version"`
	ActivationEpoch int64    `json:"activation_epoch"`
	Busy            []string `json:"busy"`
}

type AgentLifecycleChange struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	Version   int64  `json:"version"`
	Interrupt bool   `json:"interrupt"`
}

func (c AgentLifecycleChange) Validate() error {
	if c.RequestID == "" || len(c.RequestID) > 200 || c.Version < 1 || (c.Action != "pause" && c.Action != "resume" && c.Action != "restart") || c.Action == "resume" && c.Interrupt {
		return ErrInvalid
	}
	return nil
}

// State is the fact at acceptance, not a replacement for a fresh state read.
// Deferred commands never execute later: retry with a new request identity.
type AgentLifecycleReceipt struct {
	RequestID string        `json:"request_id"`
	Action    string        `json:"action"`
	Outcome   string        `json:"outcome"`
	State     AgentRunState `json:"state"`
}

type LifecycleStore interface {
	AgentRunState(context.Context, Scope) (AgentRunState, error)
	ChangeAgentLifecycle(context.Context, Scope, AgentLifecycleChange) (AgentLifecycleReceipt, error)
}

func (s *Service) AgentRunState(ctx context.Context, actor, tenant, agent string) (AgentRunState, error) {
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, false)
	if err != nil {
		return AgentRunState{}, err
	}
	store, ok := s.Store.(LifecycleStore)
	if !ok {
		return AgentRunState{}, ErrInvalid
	}
	return store.AgentRunState(ctx, scope)
}

func (s *Service) ChangeAgentLifecycle(ctx context.Context, actor, tenant, agent string, change AgentLifecycleChange) (AgentLifecycleReceipt, error) {
	if err := change.Validate(); err != nil {
		return AgentLifecycleReceipt{}, err
	}
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, true)
	if err != nil {
		return AgentLifecycleReceipt{}, err
	}
	store, ok := s.Store.(LifecycleStore)
	if !ok {
		return AgentLifecycleReceipt{}, ErrInvalid
	}
	if _, err = s.Store.EnsureAgent(ctx, scope); err != nil {
		return AgentLifecycleReceipt{}, err
	}
	return store.ChangeAgentLifecycle(ctx, scope, change)
}
