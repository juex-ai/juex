package memory

import (
	"context"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/memory/knowledge"
)

func (s *Service) transact(ctx context.Context, access application.Access, write bool, action func(*State, application.Scope) error) error {
	scope, err := s.Authority.AuthorizeApplication(ctx, access, write || access.AgentID != "")
	if err != nil {
		return err
	}
	fn := func(state *State) error {
		if !state.Control.Enabled && access.AgentID != "" {
			return application.ErrDisabled
		}
		return action(state, scope)
	}
	if write {
		return s.Repository.Update(ctx, scope, fn)
	}
	return s.Repository.View(ctx, scope, fn)
}

func (s *Service) Status(ctx context.Context, access application.Access) (Status, error) {
	var value Status
	err := s.transact(ctx, access, false, func(state *State, _ application.Scope) error { value = state.Status(); return nil })
	return value, err
}

func (s *Service) Configure(ctx context.Context, access application.Access, version int64, enabled bool, strategy string) (Status, error) {
	if access.AgentID != "" {
		return Status{}, application.ErrDenied
	}
	var value Status
	err := s.transact(ctx, access, true, func(state *State, _ application.Scope) error {
		if err := state.Configure(version, enabled, strategy, time.Now()); err != nil {
			return err
		}
		value = state.Status()
		return nil
	})
	return value, err
}

func (s *Service) Search(ctx context.Context, access application.Access, query mc.Query) (mc.Page, error) {
	var value mc.Page
	err := s.transact(ctx, access, false, func(state *State, _ application.Scope) (err error) {
		value, err = state.Search(query, time.Now())
		return
	})
	return value, err
}

func (s *Service) Read(ctx context.Context, access application.Access, request mc.ReadRequest) (mc.Entry, error) {
	var value mc.Entry
	err := s.transact(ctx, access, false, func(state *State, _ application.Scope) (err error) {
		value, err = state.Read(request, time.Now())
		return
	})
	return value, err
}

func (s *Service) Facts(ctx context.Context, access application.Access, query mc.Query) (mc.FactPage, error) {
	var value mc.FactPage
	err := s.transact(ctx, access, false, func(state *State, _ application.Scope) (err error) {
		value, err = state.Facts(query, time.Now())
		return
	})
	return value, err
}

func (s *Service) Domains(ctx context.Context, access application.Access, request mc.DomainRequest) ([]mc.Domain, error) {
	var value []mc.Domain
	err := s.transact(ctx, access, false, func(_ *State, _ application.Scope) (err error) { value, err = knowledge.Domains(request.ID); return })
	return value, err
}

// Frozen scope and original evidence arrive only from the Runtime service.
func (s *Service) Propose(ctx context.Context, frozen application.Scope, thread string, proposal mc.Proposal, automatic bool) (mc.Receipt, error) {
	var value mc.Receipt
	err := s.transact(ctx, frozen.Access, true, func(state *State, scope application.Scope) (err error) {
		if scope != frozen {
			return application.ErrDenied
		}
		value, err = state.Propose(scope, thread, proposal, automatic, time.Now())
		return
	})
	return value, err
}

func (s *Service) Review(ctx context.Context, frozen application.Scope, binding Binding) (Review, error) {
	var value Review
	err := s.transact(ctx, frozen.Access, false, func(state *State, scope application.Scope) error {
		if scope != frozen {
			return application.ErrDenied
		}
		work, err := state.Review(scope, binding)
		if err != nil {
			return err
		}
		value = *work
		return nil
	})
	return value, err
}

func (s *Service) Decide(ctx context.Context, frozen application.Scope, binding Binding, decision mc.Decision) (mc.Receipt, error) {
	// Receipt retries must remain readable after app disable. State.Decide still
	// refuses any new mutation; the fresh Agent authorization is mandatory.
	scope, err := s.Authority.AuthorizeApplication(ctx, frozen.Access, true)
	if err != nil {
		return mc.Receipt{}, err
	}
	if scope != frozen {
		return mc.Receipt{}, application.ErrDenied
	}
	var value mc.Receipt
	err = s.Repository.Update(ctx, scope, func(state *State) (err error) {
		value, err = state.Decide(scope, binding, decision, time.Now())
		return
	})
	return value, err
}

func (s *Service) Result(ctx context.Context, access application.Access, thread, id string) (mc.Receipt, error) {
	var value mc.Receipt
	err := s.transact(ctx, access, false, func(state *State, scope application.Scope) (err error) {
		value, err = state.Result(scope, thread, id)
		return
	})
	return value, err
}

func (s *Service) Administer(ctx context.Context, access application.Access, request mc.AdminRequest) (mc.Receipt, error) {
	if access.AgentID != "" {
		return mc.Receipt{}, application.ErrDenied
	}
	var value mc.Receipt
	err := s.transact(ctx, access, true, func(state *State, scope application.Scope) (err error) {
		value, err = state.Administer(scope, request, time.Now())
		return
	})
	return value, err
}
