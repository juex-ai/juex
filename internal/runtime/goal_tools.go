package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/events"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	GoalToolGet    = "get_goal"
	GoalToolCreate = "create_goal"
	GoalToolUpdate = "update_goal"
	goalGuide      = `Guide available via skill_load("juex-thread-state").`
)

const GoalModuleID runtimemodule.ID = "goal"

const goalCompletionGateName = "goal-completion-gate"

type GoalContinuationDeferrer interface {
	ShouldDeferGoalContinuation() bool
}

type GoalModuleOptions struct {
	EnableContinuation   bool
	ContinuationDeferrer GoalContinuationDeferrer
	EventSink            func(events.Event) error
	CurrentTurnID        func() string
}

type GoalModule struct {
	store                *workmem.GoalStateStore
	enableContinuation   bool
	continuationDeferrer GoalContinuationDeferrer
	eventSink            func(events.Event) error
	currentTurnID        func() string
}

func NewGoalModule(store *workmem.GoalStateStore) *GoalModule {
	return NewGoalModuleWithOptions(store, GoalModuleOptions{EnableContinuation: true})
}

func NewGoalModuleWithOptions(store *workmem.GoalStateStore, opts GoalModuleOptions) *GoalModule {
	return &GoalModule{
		store:                store,
		enableContinuation:   opts.EnableContinuation,
		continuationDeferrer: opts.ContinuationDeferrer,
		eventSink:            opts.EventSink,
		currentTurnID:        opts.CurrentTurnID,
	}
}

func (*GoalModule) ID() runtimemodule.ID { return GoalModuleID }

func (m *GoalModule) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return GoalTools(m), nil
}

func (m *GoalModule) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	text, ok := goalStateContextFromStore(m.store)
	if !ok {
		return nil, nil
	}
	return []runtimemodule.ContextSection{{
		Key:        "thread_goal",
		Source:     "runtime",
		Text:       text,
		Projection: runtimemodule.ContextProjectionRuntimeMessage,
		MessageID:  "runtime-goal-contract",
		Budget:     runtimemodule.UnboundedContextBudget(),
	}}, nil
}

func (m *GoalModule) EvaluateFinish(_ context.Context, request runtimemodule.FinishRequest) (runtimemodule.FinishDecision, error) {
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
		Name:   goalCompletionGateName,
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
		err := fmt.Errorf("goal state: completion gate requested block_stop without continue_prompt")
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

func (m *GoalModule) CommitFinishDecision(_ context.Context, request runtimemodule.FinishRequest, selected runtimemodule.FinishDecision) (bool, error) {
	if m == nil || !m.enableContinuation || m.shouldDeferContinuation() {
		return false, nil
	}
	decision, ok := selected.OwnerData.(workmem.GoalGateDecision)
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
				Point: runtimemodule.PolicyPointFinish, Name: goalCompletionGateName, Source: "builtin",
			}, runtimemodule.PolicyResult{}, err)
		}
		return false, err
	}
	if recorded {
		m.emitGoalUpdated(request.TurnID)
	}
	return recorded, nil
}

func (m *GoalModule) FinishContinuationCommitted(_ context.Context, request runtimemodule.FinishRequest, selected runtimemodule.FinishDecision) {
	if m == nil {
		return
	}
	decision, ok := selected.OwnerData.(workmem.GoalGateDecision)
	if !ok {
		return
	}
	store := m.store
	if store == nil {
		return
	}
	snapshot, _ := store.StatusSnapshot()
	_ = m.emit(events.Event{
		Type:    "goal.continued",
		TurnID:  request.TurnID,
		Payload: goalContinuedPayload(decision, snapshot),
	})
}

func (m *GoalModule) shouldDeferContinuation() bool {
	return m.continuationDeferrer != nil && m.continuationDeferrer.ShouldDeferGoalContinuation()
}

func GoalToolDefinitions() []tools.ToolDefinition {
	return []tools.ToolDefinition{
		{
			Name:        GoalToolGet,
			Group:       tools.ToolGroupThreadState,
			Description: "Read the current thread goal before changing it. " + goalGuide,
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        GoalToolCreate,
			Group:       tools.ToolGroupThreadState,
			Description: "Create or replace this thread's in-progress goal contract. " + goalGuide,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description":   map[string]any{"type": "string"},
					"acceptance":    map[string]any{"type": "string"},
					"status_reason": map[string]any{"type": "string"},
				},
				"required": []string{"description"},
			},
		},
		{
			Name:        GoalToolUpdate,
			Group:       tools.ToolGroupThreadState,
			Description: "Update goal fields or status (in_progress, wait_for_user, success, or failure). Use wait_for_user only when progress requires new external input; success requires acceptance. " + goalGuide,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description":   map[string]any{"type": "string"},
					"acceptance":    map[string]any{"type": "string"},
					"status":        map[string]any{"type": "string"},
					"status_reason": map[string]any{"type": "string"},
				},
			},
		},
	}
}

func GoalTools(module *GoalModule) []tools.Tool {
	definitions := GoalToolDefinitions()
	unavailable := func(context.Context, map[string]any) (string, error) {
		return "", fmt.Errorf("goal state is not configured")
	}
	if module == nil || module.store == nil {
		return []tools.Tool{definitions[0].Bind(unavailable), definitions[1].Bind(unavailable), definitions[2].Bind(unavailable)}
	}
	return []tools.Tool{
		definitions[0].Bind(func(context.Context, map[string]any) (string, error) { return module.handleGetGoal() }),
		definitions[1].Bind(func(_ context.Context, in map[string]any) (string, error) { return module.handleCreateGoal(in) }),
		definitions[2].Bind(func(_ context.Context, in map[string]any) (string, error) { return module.handleUpdateGoal(in) }),
	}
}

func (m *GoalModule) handleGetGoal() (string, error) {
	store := m.store
	if store == nil {
		return "", fmt.Errorf("goal state is not configured")
	}
	snapshot, err := store.StatusSnapshot()
	if err != nil {
		return "", err
	}
	if snapshot == nil {
		return marshalGoalToolResponse(map[string]any{"present": false})
	}
	return marshalGoalToolResponse(map[string]any{"present": true, "goal": snapshot})
}

func (m *GoalModule) handleCreateGoal(in map[string]any) (string, error) {
	store := m.store
	if store == nil {
		return "", fmt.Errorf("goal state is not configured")
	}
	description := goalToolString(in, "description")
	state, err := store.CreateWithContract(workmem.GoalStateCreate{
		Description:  description,
		Acceptance:   goalToolString(in, "acceptance"),
		StatusReason: goalToolString(in, "status_reason"),
	})
	if err != nil {
		return "", err
	}
	m.emitGoalUpdated(m.activeTurnID())
	return marshalGoalToolResponse(map[string]any{"present": true, "goal": state.StatusSnapshot()})
}

func (m *GoalModule) handleUpdateGoal(in map[string]any) (string, error) {
	store := m.store
	if store == nil {
		return "", fmt.Errorf("goal state is not configured")
	}
	var update workmem.GoalStateUpdate
	changed := false
	if _, ok := in["description"]; ok {
		value := goalToolString(in, "description")
		update.Description = &value
		changed = true
	}
	if _, ok := in["acceptance"]; ok {
		value := goalToolString(in, "acceptance")
		update.Acceptance = &value
		changed = true
	}
	if raw := goalToolString(in, "status"); raw != "" {
		update.Status = workmem.GoalStatus(raw)
		changed = true
	}
	if _, ok := in["status_reason"]; ok {
		value := goalToolString(in, "status_reason")
		update.StatusReason = &value
		changed = true
	}
	if !changed {
		return "", fmt.Errorf("update_goal requires at least one goal contract or status field")
	}
	state, err := store.Update(update)
	if err != nil {
		return "", err
	}
	m.emitGoalUpdated(m.activeTurnID())
	return marshalGoalToolResponse(map[string]any{"present": true, "goal": state.StatusSnapshot()})
}

func marshalGoalToolResponse(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func goalToolString(in map[string]any, key string) string {
	if in == nil {
		return ""
	}
	value, _ := in[key].(string)
	return strings.TrimSpace(value)
}
