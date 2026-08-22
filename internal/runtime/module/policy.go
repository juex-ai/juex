package module

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/provenance"
)

const (
	maxPolicyContextChars      = 1 * 1024 * 1024
	maxPolicyContextLabelChars = 4 * 1024
	// Leave room for the largest valid first-party continuation: a Goal
	// contract can contain a 32 KiB acceptance plus other bounded fields and
	// the continuation template.
	maxPolicyContinuationChars = 64 * 1024
)

type PolicyPoint string

const (
	PolicyPointSessionStart     PolicyPoint = "session_start"
	PolicyPointTurnInput        PolicyPoint = "turn_input"
	PolicyPointToolBefore       PolicyPoint = "tool_before"
	PolicyPointToolAfter        PolicyPoint = "tool_after"
	PolicyPointFinish           PolicyPoint = "finish"
	PolicyPointCompactionBefore PolicyPoint = "compaction_before"
	PolicyPointCompactionAfter  PolicyPoint = "compaction_after"
)

// PolicyExecution is observer-only metadata. Framework assigns ModuleID and
// Point before forwarding it, so a policy cannot forge ownership or phase.
type PolicyExecution struct {
	ModuleID ID
	Point    PolicyPoint
	Name     string
	Source   string
	ToolName string
}

type PolicyResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// PolicyObserver records policy facts but cannot return a flow decision.
// Requested is the one exception that returns an error because the durable
// checkpoint must succeed before a policy implementation may run.
type PolicyObserver interface {
	Requested(PolicyExecution) error
	Started(PolicyExecution)
	Completed(PolicyExecution, PolicyResult)
	Errored(PolicyExecution, PolicyResult, error)
}

type policyCheckpointError struct {
	operation string
	err       error
}

func (e *policyCheckpointError) Error() string { return e.operation + ": " + e.err.Error() }
func (e *policyCheckpointError) Unwrap() error { return e.err }

func CheckpointPolicy(observer PolicyObserver, execution PolicyExecution) error {
	if observer == nil {
		return nil
	}
	if err := observer.Requested(execution); err != nil {
		return &policyCheckpointError{operation: "commit policy request", err: err}
	}
	return nil
}

func IsPolicyCheckpointError(err error) bool {
	var target *policyCheckpointError
	return errors.As(err, &target)
}

type policyContextValidationError struct {
	err error
}

func (e *policyContextValidationError) Error() string { return e.err.Error() }
func (e *policyContextValidationError) Unwrap() error { return e.err }

func IsPolicyContextValidationError(err error) bool {
	var target *policyContextValidationError
	return errors.As(err, &target)
}

type PolicyContext struct {
	Label string
	Text  string
}

type TurnInputAction string

const (
	TurnInputAllow   TurnInputAction = "allow"
	TurnInputReject  TurnInputAction = "reject"
	TurnInputReplace TurnInputAction = "replace"
)

type TurnInputRequest struct {
	Runtime  RuntimeContext
	Session  *SessionContext
	TurnID   string
	Message  llm.Message
	Observer PolicyObserver
}

type TurnInputDecision struct {
	Action  TurnInputAction
	Message llm.Message
	Reason  string
}

type TurnInputPolicy interface {
	ApplyTurnInput(context.Context, TurnInputRequest) (TurnInputDecision, error)
}

type ToolPolicyStage string

const (
	ToolPolicyBeforeExecution ToolPolicyStage = "before_execution"
	ToolPolicyAfterExecution  ToolPolicyStage = "after_execution"
)

type ToolPolicyResult struct {
	Content string
	IsError bool
}

type ToolPolicyRequest struct {
	Runtime  RuntimeContext
	Session  *SessionContext
	TurnID   string
	Stage    ToolPolicyStage
	ToolName string
	Input    map[string]any
	Result   ToolPolicyResult
	Observer PolicyObserver
}

type ToolPolicyAction string

const (
	ToolPolicyAllow     ToolPolicyAction = "allow"
	ToolPolicyDeny      ToolPolicyAction = "deny"
	ToolPolicyTransform ToolPolicyAction = "transform"
)

type ToolPolicyDecision struct {
	Action  ToolPolicyAction
	Reason  string
	Input   map[string]any
	Result  ToolPolicyResult
	Context []PolicyContext
}

type ToolPolicy interface {
	ApplyTool(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error)
}

// LiveToolOutputPolicy is an optional static promise that a ToolPolicy never
// transforms or withholds the raw result after execution. Unknown policies are
// treated conservatively because a live delta cannot be retracted later.
type LiveToolOutputPolicy interface {
	AllowsLiveToolOutput() bool
}

type FinishRequest struct {
	Runtime    RuntimeContext
	Session    *SessionContext
	TurnID     string
	UserInput  string
	StopReason string
	Output     string
	Observer   PolicyObserver
}

type FinishAction string

const (
	FinishComplete FinishAction = "complete"
	FinishContinue FinishAction = "continue"
)

type FinishDecision struct {
	Action       FinishAction
	Continuation string
	Context      []PolicyContext
	OwnerData    any
}

type FinishPolicy interface {
	EvaluateFinish(context.Context, FinishRequest) (FinishDecision, error)
}

// FinishPolicyCommitter owns state changes required by one selected
// continuation. Returning false means the decision became stale; Framework
// falls through to the next already-evaluated continuation.
type FinishPolicyCommitter interface {
	CommitFinishDecision(context.Context, FinishRequest, FinishDecision) (bool, error)
}

// FinishPolicyContinuationObserver runs only after Framework has admitted the
// selected continuation. It cannot reject or alter the transition.
type FinishPolicyContinuationObserver interface {
	FinishContinuationCommitted(context.Context, FinishRequest, FinishDecision)
}

type SessionStartRequest struct {
	Runtime  RuntimeContext
	Session  *SessionContext
	Observer PolicyObserver
}

type SessionStartDecision struct {
	Reject  bool
	Reason  string
	Context []PolicyContext
}

type SessionStartPolicy interface {
	ApplySessionStart(context.Context, SessionStartRequest) (SessionStartDecision, error)
}

type CompactionPolicyStage string

const (
	CompactionPolicyBefore CompactionPolicyStage = "before"
	CompactionPolicyAfter  CompactionPolicyStage = "after"
)

type CompactionPolicyRequest struct {
	Runtime  RuntimeContext
	Session  *SessionContext
	TurnID   string
	Stage    CompactionPolicyStage
	Reason   string
	Auto     bool
	Observer PolicyObserver
}

type CompactionPolicyDecision struct {
	Instructions []string
	Context      []PolicyContext
}

type CompactionPolicy interface {
	ApplyCompaction(context.Context, CompactionPolicyRequest) (CompactionPolicyDecision, error)
}

type PendingInputAdmission struct {
	Runtime   RuntimeContext
	Session   *SessionContext
	TurnID    string
	RecordIDs []string
}

type PendingInputObserver interface {
	PendingInputsAdmitted(context.Context, PendingInputAdmission)
}

type ToolPolicyEvaluation struct {
	Input             map[string]any
	Result            ToolPolicyResult
	Context           []PolicyContext
	Denied            bool
	Reason            string
	ResultTransformed bool
}

type FinishCandidate struct {
	ModuleID ID
	Decision FinishDecision
	set      *Set
	module   Module
}

type FinishEvaluation struct {
	Candidates []FinishCandidate
	Context    []PolicyContext
}

func ApplyTurnInputPolicies(ctx context.Context, request TurnInputRequest, sets ...*Set) (llm.Message, error) {
	message := cloneMessage(request.Message)
	for _, set := range sets {
		var err error
		request.Message = message
		message, err = set.applyTurnInputPolicies(ctx, request)
		if err != nil {
			return llm.Message{}, err
		}
	}
	return message, nil
}

func (s *Set) applyTurnInputPolicies(ctx context.Context, request TurnInputRequest) (llm.Message, error) {
	if s == nil {
		return request.Message, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.closed {
		return llm.Message{}, fmt.Errorf("runtime modules: %s set is closed", s.scope)
	}
	message := cloneMessage(request.Message)
	observer := request.Observer
	for _, registered := range s.turnInputPolicies {
		request.Message = cloneMessage(message)
		request.Observer = ownedPolicyObserver{owner: registered.id, point: PolicyPointTurnInput, next: observer}
		decision, err := registered.module.(TurnInputPolicy).ApplyTurnInput(nonNilContext(ctx), request)
		if err != nil {
			return llm.Message{}, fmt.Errorf("runtime module %q turn input policy: %w", registered.id, err)
		}
		switch decision.Action {
		case "", TurnInputAllow:
		case TurnInputReplace:
			message = replaceTurnInputContent(message, decision.Message)
		case TurnInputReject:
			return llm.Message{}, fmt.Errorf("runtime module %q turn input rejected%s", registered.id, reasonSuffix(decision.Reason))
		default:
			return llm.Message{}, fmt.Errorf("runtime module %q turn input policy returned invalid action %q", registered.id, decision.Action)
		}
	}
	return message, nil
}

func replaceTurnInputContent(current, replacement llm.Message) llm.Message {
	frameworkMetadata := cloneMessage(current)
	replacement = cloneMessage(replacement)
	replacement.ID = frameworkMetadata.ID
	replacement.Role = frameworkMetadata.Role
	replacement.Kind = frameworkMetadata.Kind
	replacement.Model = frameworkMetadata.Model
	replacement.PolicyBlocked = frameworkMetadata.PolicyBlocked
	replacement.Compaction = frameworkMetadata.Compaction
	return replacement
}

func ApplyToolPolicies(ctx context.Context, request ToolPolicyRequest, sets ...*Set) (ToolPolicyEvaluation, error) {
	return applyToolPolicies(ctx, request, nil, sets...)
}

// ApplyToolPoliciesWithInputCheckpoint durably records each effective input
// before a later before-execution policy can observe or act on it.
func ApplyToolPoliciesWithInputCheckpoint(
	ctx context.Context,
	request ToolPolicyRequest,
	checkpoint func(map[string]any) error,
	sets ...*Set,
) (ToolPolicyEvaluation, error) {
	return applyToolPolicies(ctx, request, checkpoint, sets...)
}

func applyToolPolicies(
	ctx context.Context,
	request ToolPolicyRequest,
	checkpoint func(map[string]any) error,
	sets ...*Set,
) (ToolPolicyEvaluation, error) {
	evaluation := ToolPolicyEvaluation{Input: cloneAnyMap(request.Input), Result: request.Result}
	for _, set := range sets {
		request.Input = evaluation.Input
		request.Result = evaluation.Result
		current, err := set.applyToolPolicies(ctx, request, checkpoint)
		evaluation.Input = current.Input
		evaluation.Result = current.Result
		combinedContext := append(append([]PolicyContext(nil), evaluation.Context...), current.Context...)
		if validationErr := validatePolicyContext(combinedContext); validationErr != nil {
			return evaluation, fmt.Errorf("runtime modules tool policy context: %w", validationErr)
		}
		evaluation.Context = combinedContext
		evaluation.ResultTransformed = evaluation.ResultTransformed || current.ResultTransformed
		if err != nil {
			return evaluation, err
		}
		if current.Denied {
			evaluation.Denied = true
			evaluation.Reason = current.Reason
			return evaluation, nil
		}
	}
	return evaluation, nil
}

func (s *Set) applyToolPolicies(
	ctx context.Context,
	request ToolPolicyRequest,
	checkpoint func(map[string]any) error,
) (ToolPolicyEvaluation, error) {
	evaluation := ToolPolicyEvaluation{Input: cloneAnyMap(request.Input), Result: request.Result}
	if s == nil {
		return evaluation, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.closed {
		return ToolPolicyEvaluation{}, fmt.Errorf("runtime modules: %s set is closed", s.scope)
	}
	observer := request.Observer
	point := PolicyPointToolBefore
	if request.Stage == ToolPolicyAfterExecution {
		point = PolicyPointToolAfter
	}
	for _, registered := range s.toolPolicies {
		request.Input = cloneAnyMap(evaluation.Input)
		request.Result = evaluation.Result
		request.Observer = ownedPolicyObserver{owner: registered.id, point: point, next: observer}
		decision, err := registered.module.(ToolPolicy).ApplyTool(nonNilContext(ctx), request)
		combinedContext := append(append([]PolicyContext(nil), evaluation.Context...), decision.Context...)
		if validationErr := validatePolicyContext(combinedContext); validationErr != nil {
			return evaluation, fmt.Errorf("runtime module %q tool policy: %w", registered.id, validationErr)
		}
		evaluation.Context = combinedContext
		if err != nil {
			return evaluation, fmt.Errorf("runtime module %q tool policy: %w", registered.id, err)
		}
		switch decision.Action {
		case "", ToolPolicyAllow:
		case ToolPolicyTransform:
			if request.Stage == ToolPolicyBeforeExecution {
				effectiveInput := cloneAnyMap(decision.Input)
				if checkpoint != nil && !reflect.DeepEqual(evaluation.Input, effectiveInput) {
					if err := checkpoint(cloneAnyMap(effectiveInput)); err != nil {
						return evaluation, &policyCheckpointError{operation: "commit resolved tool input", err: err}
					}
				}
				evaluation.Input = effectiveInput
			} else {
				evaluation.Result = decision.Result
				evaluation.ResultTransformed = true
			}
		case ToolPolicyDeny:
			evaluation.Denied = true
			evaluation.Reason = strings.TrimSpace(decision.Reason)
			return evaluation, nil
		default:
			return ToolPolicyEvaluation{}, fmt.Errorf("runtime module %q tool policy returned invalid action %q", registered.id, decision.Action)
		}
	}
	return evaluation, nil
}

func EvaluateFinishPolicies(ctx context.Context, request FinishRequest, sets ...*Set) (FinishEvaluation, error) {
	var evaluation FinishEvaluation
	for _, set := range sets {
		current, err := set.evaluateFinishPolicies(ctx, request)
		if err != nil {
			return FinishEvaluation{}, err
		}
		evaluation.Candidates = append(evaluation.Candidates, current.Candidates...)
		combinedContext := append(append([]PolicyContext(nil), evaluation.Context...), current.Context...)
		if err := validateDurablePolicyContext(combinedContext); err != nil {
			return FinishEvaluation{}, fmt.Errorf("runtime modules finish policy context: %w", err)
		}
		evaluation.Context = combinedContext
	}
	return evaluation, nil
}

func (s *Set) evaluateFinishPolicies(ctx context.Context, request FinishRequest) (FinishEvaluation, error) {
	if s == nil {
		return FinishEvaluation{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.closed {
		return FinishEvaluation{}, fmt.Errorf("runtime modules: %s set is closed", s.scope)
	}
	var evaluation FinishEvaluation
	observer := request.Observer
	for _, registered := range s.finishPolicies {
		request.Observer = ownedPolicyObserver{owner: registered.id, point: PolicyPointFinish, next: observer}
		decision, err := registered.module.(FinishPolicy).EvaluateFinish(nonNilContext(ctx), request)
		if err != nil {
			return FinishEvaluation{}, fmt.Errorf("runtime module %q finish policy: %w", registered.id, err)
		}
		combinedContext := append(append([]PolicyContext(nil), evaluation.Context...), decision.Context...)
		if err := validateDurablePolicyContext(combinedContext); err != nil {
			return FinishEvaluation{}, fmt.Errorf("runtime module %q finish policy: %w", registered.id, err)
		}
		evaluation.Context = combinedContext
		switch decision.Action {
		case "", FinishComplete:
		case FinishContinue:
			if strings.TrimSpace(decision.Continuation) == "" {
				return FinishEvaluation{}, fmt.Errorf("runtime module %q finish policy returned empty continuation", registered.id)
			}
			if count := utf8.RuneCountInString(decision.Continuation); count > maxPolicyContinuationChars {
				return FinishEvaluation{}, fmt.Errorf("runtime module %q finish continuation length %d exceeds %d", registered.id, count, maxPolicyContinuationChars)
			}
			evaluation.Candidates = append(evaluation.Candidates, FinishCandidate{
				ModuleID: registered.id,
				Decision: decision,
				set:      s,
				module:   registered.module,
			})
		default:
			return FinishEvaluation{}, fmt.Errorf("runtime module %q finish policy returned invalid action %q", registered.id, decision.Action)
		}
	}
	return evaluation, nil
}

func CommitFinishCandidate(ctx context.Context, request FinishRequest, candidate FinishCandidate) (bool, error) {
	if candidate.set == nil || candidate.module == nil {
		return false, fmt.Errorf("runtime modules: invalid finish candidate")
	}
	candidate.set.mu.RLock()
	defer candidate.set.mu.RUnlock()
	if candidate.set.state.closed {
		return false, fmt.Errorf("runtime modules: %s set is closed", candidate.set.scope)
	}
	committer, ok := candidate.module.(FinishPolicyCommitter)
	if !ok {
		return true, nil
	}
	request.Observer = ownedPolicyObserver{owner: candidate.ModuleID, point: PolicyPointFinish, next: request.Observer}
	applied, err := committer.CommitFinishDecision(nonNilContext(ctx), request, candidate.Decision)
	if err != nil {
		return false, fmt.Errorf("runtime module %q commit finish policy: %w", candidate.ModuleID, err)
	}
	return applied, nil
}

func ObserveFinishContinuation(ctx context.Context, request FinishRequest, candidate FinishCandidate) {
	if candidate.set == nil || candidate.module == nil {
		return
	}
	candidate.set.mu.RLock()
	defer candidate.set.mu.RUnlock()
	if candidate.set.state.closed {
		return
	}
	observer, ok := candidate.module.(FinishPolicyContinuationObserver)
	if !ok {
		return
	}
	request.Observer = ownedPolicyObserver{owner: candidate.ModuleID, point: PolicyPointFinish, next: request.Observer}
	observer.FinishContinuationCommitted(nonNilContext(ctx), request, candidate.Decision)
}

func ApplySessionStartPolicies(ctx context.Context, request SessionStartRequest, sets ...*Set) ([]PolicyContext, error) {
	var contexts []PolicyContext
	for _, set := range sets {
		current, err := set.applySessionStartPolicies(ctx, request)
		if err != nil {
			return nil, err
		}
		combinedContext := append(append([]PolicyContext(nil), contexts...), current...)
		if err := validateDurablePolicyContext(combinedContext); err != nil {
			return nil, fmt.Errorf("runtime modules session start policy context: %w", err)
		}
		contexts = combinedContext
	}
	return contexts, nil
}

func (s *Set) applySessionStartPolicies(ctx context.Context, request SessionStartRequest) ([]PolicyContext, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.closed {
		return nil, fmt.Errorf("runtime modules: %s set is closed", s.scope)
	}
	var contexts []PolicyContext
	observer := request.Observer
	for _, registered := range s.sessionStartPolicies {
		request.Observer = ownedPolicyObserver{owner: registered.id, point: PolicyPointSessionStart, next: observer}
		decision, err := registered.module.(SessionStartPolicy).ApplySessionStart(nonNilContext(ctx), request)
		if err != nil {
			return nil, fmt.Errorf("runtime module %q session start policy: %w", registered.id, err)
		}
		combinedContext := append(append([]PolicyContext(nil), contexts...), decision.Context...)
		if err := validateDurablePolicyContext(combinedContext); err != nil {
			return nil, fmt.Errorf("runtime module %q session start policy: %w", registered.id, err)
		}
		contexts = combinedContext
		if decision.Reject {
			return nil, fmt.Errorf("runtime module %q session start rejected%s", registered.id, reasonSuffix(decision.Reason))
		}
	}
	return contexts, nil
}

func ApplyCompactionPolicies(ctx context.Context, request CompactionPolicyRequest, sets ...*Set) (CompactionPolicyDecision, error) {
	var combined CompactionPolicyDecision
	for _, set := range sets {
		current, err := set.applyCompactionPolicies(ctx, request)
		combined.Instructions = append(combined.Instructions, current.Instructions...)
		combinedContext := append(append([]PolicyContext(nil), combined.Context...), current.Context...)
		if validationErr := validateDurablePolicyContext(combinedContext); validationErr != nil {
			return combined, fmt.Errorf("runtime modules compaction policy context: %w", validationErr)
		}
		combined.Context = combinedContext
		if err != nil {
			return combined, err
		}
	}
	return combined, nil
}

func (s *Set) applyCompactionPolicies(ctx context.Context, request CompactionPolicyRequest) (CompactionPolicyDecision, error) {
	if s == nil {
		return CompactionPolicyDecision{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.closed {
		return CompactionPolicyDecision{}, fmt.Errorf("runtime modules: %s set is closed", s.scope)
	}
	var combined CompactionPolicyDecision
	observer := request.Observer
	point := PolicyPointCompactionBefore
	if request.Stage == CompactionPolicyAfter {
		point = PolicyPointCompactionAfter
	}
	for _, registered := range s.compactionPolicies {
		request.Observer = ownedPolicyObserver{owner: registered.id, point: point, next: observer}
		decision, err := registered.module.(CompactionPolicy).ApplyCompaction(nonNilContext(ctx), request)
		combinedContext := append(append([]PolicyContext(nil), combined.Context...), decision.Context...)
		if err := validateDurablePolicyContext(combinedContext); err != nil {
			return combined, fmt.Errorf("runtime module %q compaction policy: %w", registered.id, err)
		}
		combined.Instructions = append(combined.Instructions, decision.Instructions...)
		combined.Context = combinedContext
		if err != nil {
			return combined, fmt.Errorf("runtime module %q compaction policy: %w", registered.id, err)
		}
	}
	return combined, nil
}

func NotifyPendingInputsAdmitted(ctx context.Context, admission PendingInputAdmission, sets ...*Set) {
	for _, set := range sets {
		set.notifyPendingInputsAdmitted(ctx, admission)
	}
}

func (s *Set) notifyPendingInputsAdmitted(ctx context.Context, admission PendingInputAdmission) {
	if s == nil || len(admission.RecordIDs) == 0 {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.closed {
		return
	}
	for _, registered := range s.pendingInputObservers {
		current := admission
		current.RecordIDs = append([]string(nil), admission.RecordIDs...)
		registered.module.(PendingInputObserver).PendingInputsAdmitted(nonNilContext(ctx), current)
	}
}

func validatePolicyContext(contexts []PolicyContext) error {
	for _, item := range contexts {
		if !utf8.ValidString(item.Label) {
			return fmt.Errorf("policy context label is not valid UTF-8")
		}
		if count := utf8.RuneCountInString(item.Label); count > maxPolicyContextLabelChars {
			return fmt.Errorf("policy context label length %d exceeds %d", count, maxPolicyContextLabelChars)
		}
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		if !utf8.ValidString(text) {
			return fmt.Errorf("policy context is not valid UTF-8")
		}
		if count := utf8.RuneCountInString(text); count > maxPolicyContextChars {
			return fmt.Errorf("policy context length %d exceeds %d", count, maxPolicyContextChars)
		}
	}
	return nil
}

func validateDurablePolicyContext(contexts []PolicyContext) error {
	if err := validatePolicyContext(contexts); err != nil {
		return &policyContextValidationError{err: err}
	}
	messages := make([]llm.Message, 0, len(contexts))
	for _, item := range contexts {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		message := llm.TextMessage(llm.RoleUser, item.Label+text)
		message.ID = fmt.Sprintf("policy-context-%024x", len(messages))
		message.Kind = llm.MessageKindRuntimeContext
		messages = append(messages, message)
	}
	if len(messages) == 0 {
		return nil
	}
	if err := provenance.ValidatePolicyContextQueued(provenance.PolicyContextQueuedPayload{Messages: messages}); err != nil {
		return &policyContextValidationError{err: fmt.Errorf("policy context durable batch: %w", err)}
	}
	return nil
}

func reasonSuffix(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	return ": " + reason
}

func cloneMessage(message llm.Message) llm.Message {
	message.Blocks = append([]llm.Block(nil), message.Blocks...)
	for i := range message.Blocks {
		block := &message.Blocks[i]
		block.Input = cloneAnyMap(block.Input)
		if block.Media != nil {
			media := *block.Media
			block.Media = &media
		}
		if block.Artifact != nil {
			artifact := *block.Artifact
			block.Artifact = &artifact
		}
		if block.ChunkedWrite != nil {
			chunkedWrite := *block.ChunkedWrite
			block.ChunkedWrite = &chunkedWrite
		}
	}
	if message.Compaction != nil {
		compaction := *message.Compaction
		compaction.RetainedMessageIDs = append([]string(nil), compaction.RetainedMessageIDs...)
		compaction.RetainedInputReferences = make([]llm.Message, len(message.Compaction.RetainedInputReferences))
		for i, reference := range message.Compaction.RetainedInputReferences {
			compaction.RetainedInputReferences[i] = cloneMessage(reference)
		}
		message.Compaction = &compaction
	}
	return message
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = cloneAny(value)
	}
	return cloned
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i, item := range typed {
			cloned[i] = cloneAny(item)
		}
		return cloned
	default:
		return value
	}
}

type ownedPolicyObserver struct {
	owner ID
	point PolicyPoint
	next  PolicyObserver
}

func (o ownedPolicyObserver) execution(execution PolicyExecution) PolicyExecution {
	execution.ModuleID = o.owner
	execution.Point = o.point
	return execution
}

func (o ownedPolicyObserver) Requested(execution PolicyExecution) error {
	if o.next == nil {
		return nil
	}
	return o.next.Requested(o.execution(execution))
}

func (o ownedPolicyObserver) Started(execution PolicyExecution) {
	if o.next != nil {
		o.next.Started(o.execution(execution))
	}
}

func (o ownedPolicyObserver) Completed(execution PolicyExecution, result PolicyResult) {
	if o.next != nil {
		o.next.Completed(o.execution(execution), result)
	}
}

func (o ownedPolicyObserver) Errored(execution PolicyExecution, result PolicyResult, err error) {
	if o.next != nil {
		o.next.Errored(o.execution(execution), result, err)
	}
}
