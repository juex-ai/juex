package managedruntime

import (
	"context"
	"errors"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

var ErrApplicationBudget = errors.New("application Worker model budget exhausted")
var ErrSourceBusy = errors.New("source Thread is not safely idle")

// ApplicationJob is a frozen task from a trusted built-in service. The app owns
// its business result; Runtime owns this ordinary Worker's execution and usage.
type ApplicationJob struct {
	ModelBudget      *ApplicationModelBudget `json:"model_budget,omitempty"`
	IdleSourceThread string                  `json:"idle_source_thread,omitempty"`
	Application      string                  `json:"application"`
	ID               string                  `json:"id"`
	Epoch            int64                   `json:"epoch"`
	Fence            uint64                  `json:"fence"`
	Name             string                  `json:"name"`
	Instruction      string                  `json:"instruction"`
	MaxCalls         int                     `json:"max_calls"`
}

func (j ApplicationJob) Valid() bool {
	return j.ModelBudget.valid() && (j.Application == "memory" || j.Application == "calendar") && j.ID != "" && len(j.ID) <= 128 && j.Epoch > 0 && j.MaxCalls >= 1 && j.MaxCalls <= 32 && strings.TrimSpace(j.Name) != "" && len([]rune(j.Name)) <= 100 && strings.TrimSpace(j.Instruction) != "" && len(j.Instruction) <= 128<<10
}

func (j ApplicationJob) AllowsTool(name string) bool {
	if name == "thread_create" {
		return false
	}
	if j.Application != "memory" {
		return true
	}
	switch name {
	case "read_context", "memory_search", "memory_read", "memory_facts", "memory_domains", "memory_decide":
		return true
	default:
		return false
	}
}

type ApplicationReceipt struct {
	Application string   `json:"application"`
	ID          string   `json:"id"`
	ThreadID    string   `json:"thread_id,omitempty"`
	InputID     string   `json:"input_id,omitempty"`
	State       string   `json:"state"`
	Operations  []string `json:"operations,omitempty"`
}

type ApplicationStore interface {
	AdmitApplication(context.Context, Scope, ApplicationJob) (ApplicationReceipt, error)
	CancelApplication(context.Context, Scope, string, string) error
	ApplicationReceipt(context.Context, Scope, string, string) (ApplicationReceipt, error)
	ThreadApplication(context.Context, Scope, string) (*ApplicationJob, error)
}

type ApplicationGateway interface {
	Check(context.Context, Scope, ApplicationJob) error
	Tools(context.Context, Scope, *ApplicationJob) (ApplicationTools, error)
	Call(context.Context, ToolWork, *ApplicationJob) (any, error)
	Cancel(context.Context, ToolWork) error
}

type ApplicationTools struct {
	Tools        []llm.ToolSpec
	Instructions string
}

func (s *Service) AdmitApplication(ctx context.Context, original Scope, job ApplicationJob) (ApplicationReceipt, error) {
	if !job.Valid() || s.Applications == nil {
		return ApplicationReceipt{}, ErrInvalid
	}
	store, ok := s.Store.(ApplicationStore)
	if !ok {
		return ApplicationReceipt{}, ErrInvalid
	}
	if _, err := store.ApplicationReceipt(ctx, original, job.Application, job.ID); err == nil {
		return store.AdmitApplication(ctx, original, job)
	} else if !errors.Is(err, ErrDenied) {
		return ApplicationReceipt{}, err
	}
	scope, err := s.scope(ctx, original.ActorID, original.TenantID, original.AgentID, true)
	if err != nil {
		return ApplicationReceipt{}, err
	}
	if !scope.SameAuthority(original) || !scope.Capabilities.Allows(agentpolicy.Capability(job.Application)) {
		return ApplicationReceipt{}, ErrDenied
	}
	if err := s.Applications.Check(ctx, scope, job); err != nil {
		return ApplicationReceipt{}, err
	}
	if _, err := s.Authority.Snapshot(ctx, scope); err != nil {
		return ApplicationReceipt{}, err
	}
	if _, err := s.Store.EnsureAgent(ctx, scope); err != nil {
		return ApplicationReceipt{}, err
	}
	return store.AdmitApplication(ctx, scope, job)
}

// A service can revoke only its own frozen job identity, even after the member
// loses execution access. The authenticated RPC adapter fixes the application.
func (s *Service) CancelApplication(ctx context.Context, scope Scope, app, id string) error {
	store, ok := s.Store.(ApplicationStore)
	if !ok {
		return ErrInvalid
	}
	if _, err := s.Store.EnsureAgent(ctx, scope); err != nil {
		return err
	}
	return store.CancelApplication(ctx, scope, app, id)
}

func (s *Service) ApplicationReceipt(ctx context.Context, scope Scope, app, id string) (ApplicationReceipt, error) {
	store, ok := s.Store.(ApplicationStore)
	if !ok {
		return ApplicationReceipt{}, ErrInvalid
	}
	return store.ApplicationReceipt(ctx, scope, app, id)
}

func (r *Runner) application(ctx context.Context, scope Scope, thread string) (*ApplicationJob, error) {
	store, ok := r.store.(ApplicationStore)
	if !ok {
		return nil, nil
	}
	job, err := store.ThreadApplication(ctx, scope, thread)
	if err != nil || job == nil {
		return job, err
	}
	if r.config.Applications == nil {
		return job, ErrDenied
	}
	return job, r.config.Applications.Check(ctx, scope, *job)
}
