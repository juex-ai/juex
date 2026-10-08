package managedruntime

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
)

// MainTrigger transfers a Calendar occurrence into the ordinary Main mailbox.
// Acceptance is durable delivery, not completion of the resulting work.
type MainTrigger struct {
	ID          string    `json:"id"`
	Epoch       int64     `json:"epoch"`
	Name        string    `json:"name"`
	Content     string    `json:"content"`
	ScheduledAt time.Time `json:"scheduled_at"`
}

func (q MainTrigger) Valid() bool {
	_, err := uuid.Parse(q.ID)
	return err == nil && q.Epoch > 0 && strings.TrimSpace(q.Name) != "" && len(q.Name) <= 200 && len([]rune(q.Name)) <= 100 && !strings.ContainsAny(q.Name, "\r\n") && strings.TrimSpace(q.Content) != "" && len(q.Content) <= 8192 && !q.ScheduledAt.IsZero()
}

type MainTriggerReceipt struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id,omitempty"`
	InputID  string `json:"input_id,omitempty"`
	State    string `json:"state"`
}

type MainTriggerStore interface {
	AdmitMainTrigger(context.Context, Scope, MainTrigger) (MainTriggerReceipt, error)
	MainTriggerReceipt(context.Context, Scope, string) (MainTriggerReceipt, error)
	CancelMainTrigger(context.Context, Scope, string) (MainTriggerReceipt, error)
}

type TriggerAuthority interface {
	CheckTrigger(context.Context, Scope, MainTrigger) error
}

func (s *Service) AdmitMainTrigger(ctx context.Context, original Scope, q MainTrigger) (MainTriggerReceipt, error) {
	store, ok := s.Store.(MainTriggerStore)
	if !ok || !q.Valid() || s.Triggers == nil {
		return MainTriggerReceipt{}, ErrInvalid
	}
	// Historical receipts are available after revocation, but the store still
	// compares their complete frozen request and authority on every retry.
	if _, err := store.MainTriggerReceipt(ctx, original, q.ID); err == nil {
		return store.AdmitMainTrigger(ctx, original, q)
	} else if !errors.Is(err, ErrDenied) {
		return MainTriggerReceipt{}, err
	}
	scope, err := s.Authority.Authorize(ctx, original.ActorID, original.TenantID, original.AgentID, true)
	if err != nil {
		return MainTriggerReceipt{}, err
	}
	if !scope.SameAuthority(original) || !scope.Capabilities.Allows(agentpolicy.Calendar) {
		return MainTriggerReceipt{}, ErrDenied
	}
	if err := s.Triggers.CheckTrigger(ctx, scope, q); err != nil {
		return MainTriggerReceipt{}, err
	}
	if _, err := s.Authority.Snapshot(ctx, scope); err != nil {
		return MainTriggerReceipt{}, err
	}
	if _, err := s.Store.EnsureAgent(ctx, scope); err != nil {
		return MainTriggerReceipt{}, err
	}
	return store.AdmitMainTrigger(ctx, scope, q)
}

func (s *Service) MainTriggerReceipt(ctx context.Context, scope Scope, id string) (MainTriggerReceipt, error) {
	store, ok := s.Store.(MainTriggerStore)
	if !ok {
		return MainTriggerReceipt{}, ErrInvalid
	}
	return store.MainTriggerReceipt(ctx, scope, id)
}

func (s *Service) CancelMainTrigger(ctx context.Context, scope Scope, id string) (MainTriggerReceipt, error) {
	store, ok := s.Store.(MainTriggerStore)
	if !ok {
		return MainTriggerReceipt{}, ErrInvalid
	}
	if _, err := s.Store.EnsureAgent(ctx, scope); err != nil {
		return MainTriggerReceipt{}, err
	}
	return store.CancelMainTrigger(ctx, scope, id)
}
