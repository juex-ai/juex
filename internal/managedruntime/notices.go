package managedruntime

import (
	"context"
	"errors"
	"time"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type NoticeStore interface {
	AcceptNotice(context.Context, Scope, application.Event) error
	PendingNotices(context.Context, Scope, string) ([]application.Event, error)
	ConsumeNotice(context.Context, Lease, Work, application.Event, bool) (*llm.Message, int64, error)
}
type NoticeGateway interface {
	NoticeValid(context.Context, application.Event) error
}

func noticeScope(event application.Event) Scope {
	s := event.Scope
	return Scope{Capabilities: s.Capabilities, ActorID: s.ActorID, TenantID: s.TenantID, UserID: s.UserID, AgentID: s.AgentID, FleetID: s.FleetID, ActorAuthorizationEpoch: s.ActorEpoch, MembershipExecutionEpoch: s.MemberEpoch, AgentExecutionEpoch: s.AgentEpoch, MembershipVersion: s.MemberVersion}
}

func (s *Service) RecordApplicationNotice(ctx context.Context, event application.Event) error {
	store, ok := s.Store.(NoticeStore)
	gate, configured := s.Applications.(NoticeGateway)
	if !event.Valid() || event.Scope.AgentID == "" || event.Application == "runtime" {
		return ErrInvalid
	}
	if !ok || !configured {
		return ErrDenied
	}
	original := noticeScope(event)
	current, err := s.Authority.Authorize(ctx, original.ActorID, original.TenantID, original.AgentID, true)
	if err != nil {
		return err
	}
	if !current.SameAuthority(original) || !current.Capabilities.Allows(agentpolicy.Capability(event.Application)) {
		return ErrDenied
	}
	if err := gate.NoticeValid(ctx, event); err != nil {
		return err
	}
	return store.AcceptNotice(ctx, current, event)
}

// Delivery never queues an input. A Main already processing authorized work
// consumes bounded application facts after rechecking their authority.
func (r *Runner) applicationNotices(ctx context.Context, lease Lease, work *Work) error {
	store, ok := r.store.(NoticeStore)
	if !ok {
		return nil
	}
	gate, ok := r.config.Applications.(NoticeGateway)
	if !ok {
		return nil
	}
	if work.Compaction != nil || work.Source.Kind == "compaction" {
		return nil
	}
	events, err := store.PendingNotices(ctx, work.Scope, work.ThreadID)
	if err != nil {
		return err
	}
	batch, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, event := range events {
		if !work.Config.Capabilities.Allows(agentpolicy.Capability(event.Application)) {
			continue
		}
		call, stop := context.WithTimeout(batch, 500*time.Millisecond)
		_, err := r.currentScope(call, noticeScope(event))
		if err == nil {
			err = gate.NoticeValid(call, event)
		}
		stop()
		if err != nil && !errors.Is(err, ErrDenied) {
			continue
		}
		message, sequence, err := store.ConsumeNotice(ctx, lease, *work, event, err == nil)
		if err != nil {
			return err
		}
		if message != nil {
			work.History = append(work.History, *message)
			work.ContextSequence = sequence
		}
		if batch.Err() != nil {
			break
		}
	}
	return nil
}
