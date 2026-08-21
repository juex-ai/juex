package app

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

type replacementGoalModule struct{}

func (*replacementGoalModule) ID() runtimemodule.ID { return runtime.GoalModuleID }

func (*replacementGoalModule) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return []tools.Tool{{
		Name:  runtime.GoalToolGet,
		Group: tools.ToolGroupSessionState,
		Handler: func(context.Context, map[string]any) (string, error) {
			return `{"owner":"replacement-goal"}`, nil
		},
	}}, nil
}

func (*replacementGoalModule) Context(context.Context, runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	return []runtimemodule.ContextSection{{
		Key:        "replacement_goal",
		Source:     "test",
		Text:       "replacement Goal context",
		Projection: runtimemodule.ContextProjectionRuntimeMessage,
		MessageID:  "replacement-goal-context",
		Budget:     runtimemodule.UnboundedContextBudget(),
	}}, nil
}

func (*replacementGoalModule) EvaluateFinish(context.Context, runtimemodule.FinishRequest) (runtimemodule.FinishDecision, error) {
	return runtimemodule.FinishDecision{
		Action:       runtimemodule.FinishContinue,
		Continuation: "replacement Goal continuation",
	}, nil
}

func TestAppCanReplaceGoalModuleWithoutRuntimeChanges(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
			Modules: config.ModulePolicy{
				string(runtime.GoalModuleID): {Enabled: false},
			},
		},
		Provider:           &stubProvider{},
		WorkDir:            dir,
		DisableMCP:         true,
		disableObservables: true,
		sessionModuleFactories: []runtimemodule.SessionFactorySpec{{
			ID:      runtime.GoalModuleID,
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return &replacementGoalModule{}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })

	toolOutput, err := a.Engine.Tools.Call(context.Background(), runtime.GoalToolGet, nil)
	if err != nil || toolOutput != `{"owner":"replacement-goal"}` {
		t.Fatalf("replacement Goal tool = %q, %v", toolOutput, err)
	}
	contextSnapshot := a.Engine.ActiveContext(llm.TextMessage(llm.RoleUser, "continue"))
	foundContext := false
	for _, message := range contextSnapshot.Messages {
		if message.ID == "replacement-goal-context" && message.FirstText() == "replacement Goal context" {
			foundContext = true
		}
	}
	if !foundContext {
		t.Fatalf("replacement Goal context missing: %+v", contextSnapshot.Messages)
	}
	evaluation, err := runtimemodule.EvaluateFinishPolicies(context.Background(), runtimemodule.FinishRequest{}, a.Engine.SessionRuntimeSnapshot().Modules)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Candidates) != 1 || evaluation.Candidates[0].ModuleID != runtime.GoalModuleID || evaluation.Candidates[0].Decision.Continuation != "replacement Goal continuation" {
		t.Fatalf("replacement Goal finish candidates = %+v", evaluation.Candidates)
	}
}

func TestHooksReadGoalStateFromActiveModuleProvider(t *testing.T) {
	a, _ := newStubApp(t)
	var observed []byte
	installSideHookRunner(t, a, sideHookRunnerFunc(func(_ context.Context, request hooks.Request) ([]hooks.Result, error) {
		if request.EventName == hooks.EventUserPromptSubmit {
			observed = append([]byte(nil), request.GoalState...)
		}
		return nil, nil
	}))
	if _, err := appGoalStateStore(t, a).Create("hook reads module state", "exact Goal JSON is projected"); err != nil {
		t.Fatal(err)
	}
	snapshot := a.Engine.SessionRuntimeSnapshot()
	sessionContext := sessionModuleContext(snapshot.Session)
	if _, err := runtimemodule.ApplyTurnInputPolicies(context.Background(), runtimemodule.TurnInputRequest{
		Runtime: a.runtimeModuleContext,
		Session: &sessionContext,
		TurnID:  "turn-hook-goal",
		Message: llm.TextMessage(llm.RoleUser, "continue"),
	}, snapshot.Modules); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(observed, []byte(`"description":"hook reads module state"`)) {
		t.Fatalf("Hook Goal state = %s", observed)
	}
}
