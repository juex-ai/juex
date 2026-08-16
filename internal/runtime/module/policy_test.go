package module

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

type policyTestModule struct {
	id ID

	log           *[]string
	inputDecision TurnInputDecision
	inputErr      error
	toolDecision  ToolPolicyDecision
	toolErr       error
	finish        FinishDecision
	finishErr     error
	observeFinish bool
	commitApplied bool
	commitErr     error
	admitted      *[]string
	observedInput *[]map[string]any
	mutateInput   bool
	mutateMessage bool
	mutateIDs     bool

	finishEntered chan struct{}
	releaseFinish chan struct{}
}

func (m *policyTestModule) ID() ID { return m.id }

func (m *policyTestModule) ApplyTurnInput(_ context.Context, request TurnInputRequest) (TurnInputDecision, error) {
	if m.log != nil {
		*m.log = append(*m.log, "input:"+string(m.id)+":"+request.Message.FirstText())
	}
	if m.mutateMessage {
		request.Message.Blocks[0].Input["nested"].(map[string]any)["value"] = "mutated"
	}
	return m.inputDecision, m.inputErr
}

func (m *policyTestModule) ApplyTool(_ context.Context, request ToolPolicyRequest) (ToolPolicyDecision, error) {
	if m.log != nil {
		*m.log = append(*m.log, "tool:"+string(m.id)+":"+string(request.Stage))
	}
	if m.observedInput != nil {
		*m.observedInput = append(*m.observedInput, cloneAnyMap(request.Input))
	}
	if m.mutateInput {
		request.Input["nested"].(map[string]any)["value"] = "mutated"
		request.Input["items"].([]any)[0].(map[string]any)["value"] = "mutated"
	}
	return m.toolDecision, m.toolErr
}

func (m *policyTestModule) EvaluateFinish(_ context.Context, request FinishRequest) (FinishDecision, error) {
	if m.log != nil {
		*m.log = append(*m.log, "finish:"+string(m.id))
	}
	if m.observeFinish && request.Observer != nil {
		request.Observer.Started(PolicyExecution{Point: PolicyPointFinish})
	}
	if m.finishEntered != nil {
		close(m.finishEntered)
		<-m.releaseFinish
	}
	return m.finish, m.finishErr
}

func (m *policyTestModule) CommitFinishDecision(context.Context, FinishRequest, FinishDecision) (bool, error) {
	if m.log != nil {
		*m.log = append(*m.log, "commit:"+string(m.id))
	}
	return m.commitApplied, m.commitErr
}

func (m *policyTestModule) FinishContinuationCommitted(context.Context, FinishRequest, FinishDecision) {
	if m.log != nil {
		*m.log = append(*m.log, "observed:"+string(m.id))
	}
}

func (m *policyTestModule) PendingInputsAdmitted(_ context.Context, admission PendingInputAdmission) {
	if m.mutateIDs {
		admission.RecordIDs[0] = "mutated"
	}
	if m.admitted != nil {
		*m.admitted = append(*m.admitted, admission.RecordIDs...)
	}
}

func TestPolicyRequestsCannotMutateInputsWithoutTypedDecision(t *testing.T) {
	message := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{
		Type:  llm.BlockToolUse,
		Input: map[string]any{"nested": map[string]any{"value": "original"}},
	}}}
	messageSet := mustPolicySet(t, &policyTestModule{
		id:            "message-mutator",
		mutateMessage: true,
		inputDecision: TurnInputDecision{Action: TurnInputAllow},
	})
	result, err := ApplyTurnInputPolicies(context.Background(), TurnInputRequest{Message: message}, messageSet)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Blocks[0].Input["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("turn policy mutation leaked into result: %v", got)
	}
	if got := message.Blocks[0].Input["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("turn policy mutation leaked into caller: %v", got)
	}

	input := map[string]any{
		"nested": map[string]any{"value": "original"},
		"items":  []any{map[string]any{"value": "original"}},
	}
	toolSet := mustPolicySet(t, &policyTestModule{
		id:           "tool-mutator",
		mutateInput:  true,
		toolDecision: ToolPolicyDecision{Action: ToolPolicyAllow},
	})
	evaluation, err := ApplyToolPolicies(context.Background(), ToolPolicyRequest{Input: input}, toolSet)
	if err != nil {
		t.Fatal(err)
	}
	if got := evaluation.Input["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("tool policy map mutation leaked into result: %v", got)
	}
	if got := evaluation.Input["items"].([]any)[0].(map[string]any)["value"]; got != "original" {
		t.Fatalf("tool policy slice mutation leaked into result: %v", got)
	}
	if got := input["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("tool policy mutation leaked into caller: %v", got)
	}
}

func TestFinishContinuationAndPolicyContextAreBounded(t *testing.T) {
	empty := mustPolicySet(t, &policyTestModule{
		id:     "empty-finish",
		finish: FinishDecision{Action: FinishContinue, Continuation: " \n\t"},
	})
	if _, err := EvaluateFinishPolicies(context.Background(), FinishRequest{}, empty); err == nil || !strings.Contains(err.Error(), "empty continuation") {
		t.Fatalf("empty continuation error = %v", err)
	}

	maximum := mustPolicySet(t, &policyTestModule{
		id:     "maximum-finish",
		finish: FinishDecision{Action: FinishContinue, Continuation: strings.Repeat("x", maxPolicyContinuationChars)},
	})
	if evaluation, err := EvaluateFinishPolicies(context.Background(), FinishRequest{}, maximum); err != nil || len(evaluation.Candidates) != 1 {
		t.Fatalf("maximum continuation evaluation = %+v, %v", evaluation, err)
	}

	oversized := mustPolicySet(t, &policyTestModule{
		id:     "oversized-finish",
		finish: FinishDecision{Action: FinishContinue, Continuation: strings.Repeat("x", maxPolicyContinuationChars+1)},
	})
	if _, err := EvaluateFinishPolicies(context.Background(), FinishRequest{}, oversized); err == nil || !strings.Contains(err.Error(), "continuation length") {
		t.Fatalf("oversized continuation error = %v", err)
	}

	invalidLabel := mustPolicySet(t, &policyTestModule{
		id: "invalid-label",
		toolDecision: ToolPolicyDecision{
			Action:  ToolPolicyAllow,
			Context: []PolicyContext{{Label: string([]byte{0xff}), Text: "context"}},
		},
	})
	if _, err := ApplyToolPolicies(context.Background(), ToolPolicyRequest{}, invalidLabel); err == nil || !strings.Contains(err.Error(), "label is not valid UTF-8") {
		t.Fatalf("invalid label error = %v", err)
	}

	largeLabel := mustPolicySet(t, &policyTestModule{
		id: "large-label",
		toolDecision: ToolPolicyDecision{
			Action:  ToolPolicyAllow,
			Context: []PolicyContext{{Label: strings.Repeat("x", maxPolicyContextLabelChars+1), Text: "context"}},
		},
	})
	if _, err := ApplyToolPolicies(context.Background(), ToolPolicyRequest{}, largeLabel); err == nil || !strings.Contains(err.Error(), "label length") {
		t.Fatalf("large label error = %v", err)
	}
}

func TestTypedPoliciesPreserveModuleOrderAndEvaluateEveryFinishPolicy(t *testing.T) {
	var log []string
	set := mustPolicySet(t,
		&policyTestModule{id: "first", log: &log, finish: FinishDecision{Action: FinishContinue, Continuation: "one"}, commitApplied: true},
		&policyTestModule{id: "second", log: &log, finish: FinishDecision{Action: FinishContinue, Continuation: "two"}, commitApplied: true},
		&policyTestModule{id: "third", log: &log, finish: FinishDecision{Action: FinishComplete}, commitApplied: true},
	)

	evaluation, err := EvaluateFinishPolicies(context.Background(), FinishRequest{}, set)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"finish:first", "finish:second", "finish:third"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("evaluation order = %#v, want %#v", log, want)
	}
	if len(evaluation.Candidates) != 2 || evaluation.Candidates[0].ModuleID != "first" || evaluation.Candidates[1].ModuleID != "second" {
		t.Fatalf("finish candidates = %#v", evaluation.Candidates)
	}
	applied, err := CommitFinishCandidate(context.Background(), FinishRequest{}, evaluation.Candidates[0])
	if err != nil || !applied {
		t.Fatalf("commit first = %t, %v", applied, err)
	}
	ObserveFinishContinuation(context.Background(), FinishRequest{}, evaluation.Candidates[0])
	if want := []string{"finish:first", "finish:second", "finish:third", "commit:first", "observed:first"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("finish lifecycle = %#v, want %#v", log, want)
	}
}

func TestFinishCandidateCanBecomeStaleWithoutCommittingFlow(t *testing.T) {
	var log []string
	set := mustPolicySet(t,
		&policyTestModule{id: "stale", log: &log, finish: FinishDecision{Action: FinishContinue, Continuation: "old"}},
		&policyTestModule{id: "next", log: &log, finish: FinishDecision{Action: FinishContinue, Continuation: "next"}, commitApplied: true},
	)
	evaluation, err := EvaluateFinishPolicies(context.Background(), FinishRequest{}, set)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := CommitFinishCandidate(context.Background(), FinishRequest{}, evaluation.Candidates[0])
	if err != nil || applied {
		t.Fatalf("stale commit = %t, %v", applied, err)
	}
	applied, err = CommitFinishCandidate(context.Background(), FinishRequest{}, evaluation.Candidates[1])
	if err != nil || !applied {
		t.Fatalf("next commit = %t, %v", applied, err)
	}
}

func TestTurnInputPoliciesTransformSequentiallyAndFailClosed(t *testing.T) {
	var log []string
	firstMessage := llm.TextMessage(llm.RoleUser, "transformed")
	set := mustPolicySet(t,
		&policyTestModule{id: "first", log: &log, inputDecision: TurnInputDecision{Action: TurnInputReplace, Message: firstMessage}},
		&policyTestModule{id: "second", log: &log, inputDecision: TurnInputDecision{Action: TurnInputReject, Reason: "blocked"}},
	)
	_, err := ApplyTurnInputPolicies(context.Background(), TurnInputRequest{Message: llm.TextMessage(llm.RoleUser, "original")}, set)
	if err == nil || !strings.Contains(err.Error(), `module "second"`) || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("turn input error = %v", err)
	}
	if want := []string{"input:first:original", "input:second:transformed"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("input order = %#v, want %#v", log, want)
	}
}

func TestTurnInputPolicyReplacementPreservesFrameworkMessageMetadata(t *testing.T) {
	compaction := &llm.CompactionMetadata{Auto: true, Reason: "pending-input"}
	original := llm.Message{
		ID:         "pending-message-1",
		Role:       llm.RoleUser,
		Kind:       llm.MessageKindMCPEvent,
		Model:      "framework:model",
		Compaction: compaction,
		Blocks:     []llm.Block{{Type: llm.BlockText, Text: "original"}},
	}
	replacement := llm.Message{
		ID:         "policy-message",
		Role:       llm.RoleAssistant,
		Kind:       llm.MessageKindContinuation,
		Model:      "policy:model",
		Compaction: &llm.CompactionMetadata{Reason: "policy"},
		Blocks:     []llm.Block{{Type: llm.BlockText, Text: "replacement"}},
	}
	set := mustPolicySet(t, &policyTestModule{
		id: "transformer",
		inputDecision: TurnInputDecision{
			Action:  TurnInputReplace,
			Message: replacement,
		},
	})

	result, err := ApplyTurnInputPolicies(context.Background(), TurnInputRequest{Message: original}, set)
	if err != nil {
		t.Fatal(err)
	}
	if result.FirstText() != "replacement" {
		t.Fatalf("replacement content = %q", result.FirstText())
	}
	if result.ID != original.ID || result.Role != original.Role || result.Kind != original.Kind || result.Model != original.Model {
		t.Fatalf("framework metadata changed: got %+v, want ID=%q role=%q kind=%q model=%q", result, original.ID, original.Role, original.Kind, original.Model)
	}
	if result.Compaction == nil || result.Compaction.Auto != original.Compaction.Auto || result.Compaction.Reason != original.Compaction.Reason {
		t.Fatalf("compaction metadata = %+v, want %+v", result.Compaction, original.Compaction)
	}
}

func TestToolPolicyErrorIsAttributedAndDenyStopsLaterPolicies(t *testing.T) {
	var log []string
	set := mustPolicySet(t,
		&policyTestModule{id: "guard", log: &log, toolDecision: ToolPolicyDecision{Action: ToolPolicyDeny, Reason: "unsafe"}},
		&policyTestModule{id: "later", log: &log, toolErr: errors.New("must not run")},
	)
	evaluation, err := ApplyToolPolicies(context.Background(), ToolPolicyRequest{Stage: ToolPolicyBeforeExecution, ToolName: "exec"}, set)
	if err != nil {
		t.Fatal(err)
	}
	if !evaluation.Denied || evaluation.Reason != "unsafe" {
		t.Fatalf("tool evaluation = %#v", evaluation)
	}
	if want := []string{"tool:guard:before_execution"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("tool policy order = %#v, want %#v", log, want)
	}

	failing := mustPolicySet(t, &policyTestModule{id: "safety", toolErr: errors.New("offline")})
	_, err = ApplyToolPolicies(context.Background(), ToolPolicyRequest{Stage: ToolPolicyBeforeExecution}, failing)
	if err == nil || !strings.Contains(err.Error(), `module "safety"`) || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("tool policy error = %v", err)
	}
}

func TestToolPoliciesPreserveValidContextOnPolicyError(t *testing.T) {
	first := mustPolicySet(t, &policyTestModule{
		id: "first-context",
		toolDecision: ToolPolicyDecision{
			Action:  ToolPolicyAllow,
			Context: []PolicyContext{{Label: "First context:\n", Text: "keep first"}},
		},
	})
	failing := mustPolicySet(t, &policyTestModule{
		id: "partial-context",
		toolDecision: ToolPolicyDecision{
			Action:  ToolPolicyAllow,
			Context: []PolicyContext{{Label: "Second context:\n", Text: "keep second"}},
		},
		toolErr: errors.New("later required check failed"),
	})
	evaluation, err := ApplyToolPolicies(context.Background(), ToolPolicyRequest{Stage: ToolPolicyAfterExecution}, first, failing)
	if err == nil || !strings.Contains(err.Error(), "later required check failed") {
		t.Fatalf("tool policy error = %v", err)
	}
	want := []PolicyContext{
		{Label: "First context:\n", Text: "keep first"},
		{Label: "Second context:\n", Text: "keep second"},
	}
	if !reflect.DeepEqual(evaluation.Context, want) {
		t.Fatalf("tool policy context = %#v, want %#v", evaluation.Context, want)
	}
}

func TestToolPoliciesCheckpointTransformedInputBeforeNextPolicy(t *testing.T) {
	var log []string
	set := mustPolicySet(t,
		&policyTestModule{id: "first", log: &log, toolDecision: ToolPolicyDecision{
			Action: ToolPolicyTransform,
			Input:  map[string]any{"path": "effective.txt"},
		}},
		&policyTestModule{id: "second", log: &log, toolDecision: ToolPolicyDecision{Action: ToolPolicyAllow}},
	)
	evaluation, err := ApplyToolPoliciesWithInputCheckpoint(context.Background(), ToolPolicyRequest{
		Stage: ToolPolicyBeforeExecution,
		Input: map[string]any{"path": "provider.txt"},
	}, func(input map[string]any) error {
		log = append(log, "checkpoint:"+input["path"].(string))
		return nil
	}, set)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"tool:first:before_execution", "checkpoint:effective.txt", "tool:second:before_execution"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("policy/checkpoint order = %#v, want %#v", log, want)
	}
	if got := evaluation.Input["path"]; got != "effective.txt" {
		t.Fatalf("effective input = %v", got)
	}
}

func TestAfterExecutionTransformPreservesInputForLaterPolicies(t *testing.T) {
	var observed []map[string]any
	set := mustPolicySet(t,
		&policyTestModule{id: "transform-result", toolDecision: ToolPolicyDecision{
			Action: ToolPolicyTransform,
			Result: ToolPolicyResult{Content: "filtered"},
		}},
		&policyTestModule{
			id:            "audit-input",
			observedInput: &observed,
			toolDecision:  ToolPolicyDecision{Action: ToolPolicyAllow},
		},
	)

	evaluation, err := ApplyToolPolicies(context.Background(), ToolPolicyRequest{
		Stage:  ToolPolicyAfterExecution,
		Input:  map[string]any{"path": "handled.txt"},
		Result: ToolPolicyResult{Content: "raw"},
	}, set)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 || observed[0]["path"] != "handled.txt" {
		t.Fatalf("later policy input = %#v, want handled input", observed)
	}
	if evaluation.Input["path"] != "handled.txt" {
		t.Fatalf("evaluated input = %#v, want handled input", evaluation.Input)
	}
	if evaluation.Result.Content != "filtered" {
		t.Fatalf("evaluated result = %#v, want filtered content", evaluation.Result)
	}
	if !evaluation.ResultTransformed {
		t.Fatal("after-execution transform was not recorded in the evaluation")
	}
}

func TestToolPolicyInputCheckpointFailureStopsLaterPolicy(t *testing.T) {
	var log []string
	set := mustPolicySet(t,
		&policyTestModule{id: "first", log: &log, toolDecision: ToolPolicyDecision{
			Action: ToolPolicyTransform,
			Input:  map[string]any{"path": "effective.txt"},
		}},
		&policyTestModule{id: "second", log: &log, toolDecision: ToolPolicyDecision{Action: ToolPolicyAllow}},
	)
	want := errors.New("checkpoint failed")
	_, err := ApplyToolPoliciesWithInputCheckpoint(context.Background(), ToolPolicyRequest{
		Stage: ToolPolicyBeforeExecution,
		Input: map[string]any{"path": "provider.txt"},
	}, func(map[string]any) error {
		return want
	}, set)
	if !errors.Is(err, want) || !IsPolicyCheckpointError(err) {
		t.Fatalf("checkpoint error = %v, want %v", err, want)
	}
	if wantLog := []string{"tool:first:before_execution"}; !reflect.DeepEqual(log, wantLog) {
		t.Fatalf("policies after checkpoint failure = %#v, want %#v", log, wantLog)
	}
}

func TestPolicyObserverAttributionDoesNotLeakAcrossModules(t *testing.T) {
	observer := &recordingPolicyObserver{}
	set := mustPolicySet(t,
		&policyTestModule{id: "first", observeFinish: true, finish: FinishDecision{Action: FinishComplete}},
		&policyTestModule{id: "second", observeFinish: true, finish: FinishDecision{Action: FinishComplete}},
	)
	if _, err := EvaluateFinishPolicies(context.Background(), FinishRequest{Observer: observer}, set); err != nil {
		t.Fatal(err)
	}
	if want := []ID{"first", "second"}; !reflect.DeepEqual(observer.started, want) {
		t.Fatalf("observer owners = %#v, want %#v", observer.started, want)
	}
}

func TestInvalidPolicyDecisionsAreAttributedToOwningModule(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Set) error
	}{
		{
			name: "turn input",
			run: func(set *Set) error {
				_, err := ApplyTurnInputPolicies(context.Background(), TurnInputRequest{Message: llm.TextMessage(llm.RoleUser, "input")}, set)
				return err
			},
		},
		{
			name: "tool",
			run: func(set *Set) error {
				_, err := ApplyToolPolicies(context.Background(), ToolPolicyRequest{Stage: ToolPolicyBeforeExecution}, set)
				return err
			},
		},
		{
			name: "finish",
			run: func(set *Set) error {
				_, err := EvaluateFinishPolicies(context.Background(), FinishRequest{}, set)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mod := &policyTestModule{
				id:            "invalid-owner",
				inputDecision: TurnInputDecision{Action: TurnInputAction("invalid")},
				toolDecision:  ToolPolicyDecision{Action: ToolPolicyAction("invalid")},
				finish:        FinishDecision{Action: FinishAction("invalid")},
			}
			err := test.run(mustPolicySet(t, mod))
			if err == nil || !strings.Contains(err.Error(), `module "invalid-owner"`) || !strings.Contains(err.Error(), "invalid action") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPolicyEvaluationLeasesSetUntilCallReturns(t *testing.T) {
	mod := &policyTestModule{
		id:            "leased",
		finish:        FinishDecision{Action: FinishComplete},
		finishEntered: make(chan struct{}),
		releaseFinish: make(chan struct{}),
	}
	set := mustPolicySet(t, mod)
	if err := set.StartSession(context.Background(), SessionContext{}); err != nil {
		t.Fatal(err)
	}

	evaluationDone := make(chan error, 1)
	go func() {
		_, err := EvaluateFinishPolicies(context.Background(), FinishRequest{}, set)
		evaluationDone <- err
	}()
	<-mod.finishEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- set.CloseSession(context.Background()) }()
	select {
	case err := <-closeDone:
		t.Fatalf("set closed during policy call: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(mod.releaseFinish)
	if err := <-evaluationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestPendingInputObserverReceivesCopyWithoutFlowResult(t *testing.T) {
	var admitted []string
	set := mustPolicySet(t,
		&policyTestModule{id: "mutator", mutateIDs: true},
		&policyTestModule{id: "observer", admitted: &admitted},
	)
	ids := []string{"one", "two"}
	NotifyPendingInputsAdmitted(context.Background(), PendingInputAdmission{RecordIDs: ids}, set)
	ids[0] = "mutated"
	if want := []string{"one", "two"}; !reflect.DeepEqual(admitted, want) {
		t.Fatalf("admitted ids = %#v, want %#v", admitted, want)
	}
}

func mustPolicySet(t *testing.T, modules ...Module) *Set {
	t.Helper()
	registry := NewRegistry()
	for _, mod := range modules {
		if err := registry.Register(mod); err != nil {
			t.Fatal(err)
		}
	}
	set, err := registry.Seal(context.Background(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	set.scope = ScopeSession
	return set
}

type recordingPolicyObserver struct {
	started []ID
}

func (*recordingPolicyObserver) Requested(PolicyExecution) error { return nil }

func (o *recordingPolicyObserver) Started(execution PolicyExecution) {
	o.started = append(o.started, execution.ModuleID)
}

func (*recordingPolicyObserver) Completed(PolicyExecution, PolicyResult) {}

func (*recordingPolicyObserver) Errored(PolicyExecution, PolicyResult, error) {}
