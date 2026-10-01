package managedruntime

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

// Authority is implemented by the composition root using Management's current
// directory. Runtime never reads another service's tables or stores API keys.
type Authority interface {
	Authorize(context.Context, string, string, string, bool) (Scope, error)
	Snapshot(context.Context, Scope) (TurnConfig, error)
	Provider(context.Context, Scope, ModelConfig) (llm.Provider, error)
}

type ConversationStore interface {
	SetThreadArchived(context.Context, Scope, string, bool) (Thread, error)
	EnsureAgent(context.Context, Scope) (Thread, error)
	AcceptCompaction(context.Context, Scope, string, CompactionRequest) (InputReceipt, error)
	AcceptInput(context.Context, Scope, InputRequest) (InputReceipt, error)
	Threads(context.Context, Scope) ([]Thread, error)
	Timeline(context.Context, Scope, string, int64, int) (Timeline, error)
	CancelThread(context.Context, Scope, string) error
	CreateWorker(context.Context, Scope, string, string, string) (Thread, error)
}

type Service struct {
	Store        ConversationStore
	Authority    Authority
	Applications ApplicationGateway
}

func (s *Service) scope(ctx context.Context, actor, tenant, agent string, execute bool) (Scope, error) {
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, execute)
	if err != nil {
		return scope, err
	}
	_, err = s.Store.EnsureAgent(ctx, scope)
	return scope, err
}

func (s *Service) Submit(ctx context.Context, actor, tenant, agent string, input InputRequest) (InputReceipt, error) {
	scope, err := s.scope(ctx, actor, tenant, agent, true)
	if err != nil {
		return InputReceipt{}, err
	}
	if _, err := s.Authority.Snapshot(ctx, scope); err != nil {
		return InputReceipt{}, err
	}
	return s.Store.AcceptInput(ctx, scope, input)
}

func (s *Service) Threads(ctx context.Context, actor, tenant, agent string) ([]Thread, error) {
	scope, err := s.scope(ctx, actor, tenant, agent, false)
	if err != nil {
		return nil, err
	}
	return s.Store.Threads(ctx, scope)
}

func (s *Service) Events(ctx context.Context, actor, tenant, agent, thread string, after int64, limit int) (Timeline, error) {
	scope, err := s.scope(ctx, actor, tenant, agent, false)
	if err != nil {
		return Timeline{}, err
	}
	return s.Store.Timeline(ctx, scope, thread, after, limit)
}

func (s *Service) Cancel(ctx context.Context, actor, tenant, agent, thread string) error {
	scope, err := s.scope(ctx, actor, tenant, agent, true)
	if err != nil {
		return err
	}
	return s.Store.CancelThread(ctx, scope, thread)
}

func (s *Service) Worker(ctx context.Context, actor, tenant, agent, parent, requestID, name string) (Thread, error) {
	scope, err := s.scope(ctx, actor, tenant, agent, true)
	if err != nil {
		return Thread{}, err
	}
	return s.Store.CreateWorker(ctx, scope, parent, requestID, name)
}

func (s *Service) Compact(ctx context.Context, actor, tenant, agent, thread string, request CompactionRequest) (InputReceipt, error) {
	scope, err := s.scope(ctx, actor, tenant, agent, true)
	if err != nil {
		return InputReceipt{}, err
	}
	if _, err := s.Authority.Snapshot(ctx, scope); err != nil {
		return InputReceipt{}, err
	}
	return s.Store.AcceptCompaction(ctx, scope, thread, request)
}

func (s *Service) Archive(ctx context.Context, actor, tenant, agent, thread string, archived bool) (Thread, error) {
	scope, err := s.scope(ctx, actor, tenant, agent, true)
	if err != nil {
		return Thread{}, err
	}
	return s.Store.SetThreadArchived(ctx, scope, thread, archived)
}
