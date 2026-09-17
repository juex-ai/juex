package tasks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/events"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

const (
	ToolList   = "list_tasks"
	ToolCreate = "create_task"
	ToolUpdate = "update_task"
	ToolDelete = "delete_task"
)

const ModuleID = "tasks"

const tasksCompletionGateName = "tasks-completion-gate"

type ContinuationDeferrer interface {
	ShouldDeferContinuation() bool
}

type ModuleOptions struct {
	EnableContinuation   bool
	ContinuationDeferrer ContinuationDeferrer
	EventSink            func(events.Event) error
	CurrentTurnID        func() string
}

type Module struct {
	store                *Store
	enableContinuation   bool
	continuationDeferrer ContinuationDeferrer
	eventSink            func(events.Event) error
	currentTurnID        func() string
}

func New(store *Store) *Module {
	return NewWithOptions(store, ModuleOptions{EnableContinuation: true})
}

func NewWithOptions(store *Store, opts ModuleOptions) *Module {
	return &Module{
		store:                store,
		enableContinuation:   opts.EnableContinuation,
		continuationDeferrer: opts.ContinuationDeferrer,
		eventSink:            opts.EventSink,
		currentTurnID:        opts.CurrentTurnID,
	}
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]toolcore.Tool, error) {
	return BoundTools(m), nil
}

func (m *Module) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	text, ok := tasksStateContextFromStore(m.store)
	if !ok {
		return nil, nil
	}
	return []runtimemodule.ContextSection{{
		Key:        "thread_tasks",
		Source:     "runtime",
		Text:       text,
		Projection: runtimemodule.ContextProjectionRuntimeMessage,
		MessageID:  "runtime-tasks-contract",
		Budget:     runtimemodule.UnboundedContextBudget(),
	}}, nil
}

func (m *Module) EvaluateFinish(_ context.Context, request runtimemodule.FinishRequest) (runtimemodule.FinishDecision, error) {
	if m == nil || !m.enableContinuation {
		return runtimemodule.FinishDecision{Action: runtimemodule.FinishComplete}, nil
	}
	store := m.store
	if store == nil {
		return runtimemodule.FinishDecision{Action: runtimemodule.FinishComplete}, nil
	}
	started := time.Now()
	execution := runtimemodule.PolicyExecution{
		Point:  runtimemodule.PolicyPointFinish,
		Name:   tasksCompletionGateName,
		Source: "builtin",
	}
	if request.Observer != nil {
		request.Observer.Started(execution)
	}
	decision, err := store.CompletionGateDecision()
	if err != nil {
		if request.Observer != nil {
			request.Observer.Errored(execution, runtimemodule.PolicyResult{Duration: time.Since(started)}, err)
		}
		return runtimemodule.FinishDecision{}, err
	}
	if decision.BlockStop && strings.TrimSpace(decision.ContinuePrompt) == "" {
		err := fmt.Errorf("tasks state: completion gate requested block_stop without continue_prompt")
		if request.Observer != nil {
			request.Observer.Errored(execution, runtimemodule.PolicyResult{Duration: time.Since(started)}, err)
		}
		return runtimemodule.FinishDecision{}, err
	}
	if decision.BlockStop && !m.shouldDeferContinuation() {
		if request.Observer != nil {
			request.Observer.Completed(execution, runtimemodule.PolicyResult{
				ExitCode: 2,
				Stdout:   decision.ContinuePrompt,
				Duration: time.Since(started),
			})
		}
		return runtimemodule.FinishDecision{
			Action:       runtimemodule.FinishContinue,
			Continuation: decision.ContinuePrompt,
			OwnerData:    decision,
		}, nil
	}
	if request.Observer != nil {
		request.Observer.Completed(execution, runtimemodule.PolicyResult{Duration: time.Since(started)})
	}
	return runtimemodule.FinishDecision{Action: runtimemodule.FinishComplete}, nil
}

func (m *Module) CommitFinishDecision(_ context.Context, request runtimemodule.FinishRequest, selected runtimemodule.FinishDecision) (bool, error) {
	if m == nil || !m.enableContinuation || m.shouldDeferContinuation() {
		return false, nil
	}
	decision, ok := selected.OwnerData.(GateDecision)
	if !ok || !decision.BlockStop {
		return false, nil
	}
	store := m.store
	if store == nil {
		return false, nil
	}
	recorded, err := store.RecordContinuation(decision)
	if err != nil {
		if request.Observer != nil {
			request.Observer.Errored(runtimemodule.PolicyExecution{
				Point: runtimemodule.PolicyPointFinish, Name: tasksCompletionGateName, Source: "builtin",
			}, runtimemodule.PolicyResult{}, err)
		}
		return false, err
	}
	if recorded {
		m.emitTasksUpdated(request.TurnID)
	}
	return recorded, nil
}

func (m *Module) FinishContinuationCommitted(_ context.Context, request runtimemodule.FinishRequest, selected runtimemodule.FinishDecision) {
	if m == nil {
		return
	}
	decision, ok := selected.OwnerData.(GateDecision)
	if !ok {
		return
	}
	store := m.store
	if store == nil {
		return
	}
	snapshot, _ := store.StatusSnapshot()
	_ = m.emit(events.Event{
		Type:    "tasks.continued",
		TurnID:  request.TurnID,
		Payload: tasksContinuedPayload(decision, snapshot),
	})
}

func (m *Module) shouldDeferContinuation() bool {
	return m.continuationDeferrer != nil && m.continuationDeferrer.ShouldDeferContinuation()
}
