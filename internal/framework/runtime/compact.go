package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/cancellation"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	runtimepolicy "github.com/juex-ai/juex/internal/framework/runtime/policy"
	"github.com/juex-ai/juex/internal/framework/thread"
)

const DefaultContextWindowTokens = runtimepolicy.DefaultContextWindowTokens

const compactionCanceledMessage = "Compaction canceled"

type compactionError struct {
	Err error
}

func (e *compactionError) Error() string {
	if e == nil || e.Err == nil {
		return "compact context failed"
	}
	if cancellation.IsUserCancelled(e.Err) {
		return compactionCanceledMessage
	}
	return "compact context: " + e.Err.Error()
}

func (e *compactionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newCompactionError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var compactErr *compactionError
	if errors.As(err, &compactErr) {
		return err
	}
	return &compactionError{Err: cancellation.NormalizeErrorWithContext(ctx, err)}
}

func isCompactionCancellation(err error) bool {
	var compactErr *compactionError
	return errors.As(err, &compactErr) && cancellation.IsUserCancelled(compactErr)
}

type CompactionResult struct {
	MessageID          string `json:"message_id,omitempty"`
	Reason             string `json:"reason,omitempty"`
	Auto               bool   `json:"auto"`
	TokensBefore       int    `json:"tokens_before,omitempty"`
	TokensAfter        int    `json:"tokens_after,omitempty"`
	SummaryChars       int    `json:"summary_chars,omitempty"`
	SummaryModel       string `json:"summary_model,omitempty"`
	TailStartMessageID string `json:"tail_start_message_id,omitempty"`
	FirstKeptMessageID string `json:"first_kept_message_id,omitempty"`
}

func (e *Engine) maybeCompact(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, incoming llm.Message) error {
	policy := effectiveCompactionPolicy(e.Compaction, e.ContextWindow)
	if !policy.Enabled {
		return nil
	}

	active, err := e.activeContextLockedWithPolicyContextError(ctx, e.pendingPolicyRuntimeContextSnapshot(), incoming)
	if err != nil {
		return fmt.Errorf("runtime: build compaction context: %w", err)
	}
	projected := active.Messages
	estimated := e.estimateContextTokens(systemPrompt, tools, projected)
	if estimated < policy.TriggerTokens {
		return nil
	}
	if e.autoCompactFailures >= policy.MaxAutoFailures {
		err := fmt.Errorf("auto compaction paused after %d consecutive failures; run /compact with focus instructions or start a new thread", policy.MaxAutoFailures)
		if emitErr := e.emit(events.Event{Type: "context.compact.skipped", TurnID: turnID, Payload: ContextCompactSkippedPayload{
			Reason:              "failure_circuit_breaker",
			Auto:                true,
			ConsecutiveFailures: e.autoCompactFailures,
			MaxAutoFailures:     policy.MaxAutoFailures,
			Error:               err.Error(),
		}}); emitErr != nil {
			return errors.Join(err, fmt.Errorf("commit compaction skip: %w", emitErr))
		}
		return err
	}

	_, err = e.compactLocked(ctx, turnID, systemPrompt, tools, "auto", true, "", 0, incoming)
	if err != nil {
		e.autoCompactFailures++
		return err
	}
	e.autoCompactFailures = 0
	return err
}

func (e *Engine) Compact(ctx context.Context, turnID, systemPrompt, reason string, auto bool) (CompactionResult, error) {
	return e.CompactWithInstructions(ctx, turnID, systemPrompt, reason, auto, "")
}

func (e *Engine) CompactWithInstructions(ctx context.Context, turnID, systemPrompt, reason string, auto bool, instructions string) (CompactionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ctx, operationGeneration, finishOperation := e.beginActiveOperation(ctx)
	defer finishOperation()
	return e.compactLocked(ctx, turnID, systemPrompt, e.compactionToolsLocked(), reason, auto, instructions, operationGeneration)
}

func (e *Engine) compactLocked(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, reason string, auto bool, instructions string, operationGeneration uint64, incoming ...llm.Message) (CompactionResult, error) {
	return e.compactLockedForContextWindow(ctx, turnID, systemPrompt, tools, reason, auto, instructions, e.ContextWindow, operationGeneration, incoming...)
}

func (e *Engine) compactLockedForContextWindow(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, reason string, auto bool, instructions string, contextWindow int, operationGeneration uint64, incoming ...llm.Message) (CompactionResult, error) {
	return e.compactLockedForContextWindowWithHealthReservation(ctx, turnID, systemPrompt, tools, reason, auto, instructions, contextWindow, operationGeneration, "", incoming...)
}

func (e *Engine) compactLockedForContextWindowWithHealthReservation(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, reason string, auto bool, instructions string, contextWindow int, operationGeneration uint64, reservedModelRef string, incoming ...llm.Message) (CompactionResult, error) {
	policy := effectiveCompactionPolicy(e.Compaction, contextWindow)
	if !policy.Enabled {
		return CompactionResult{}, nil
	}
	threadState := e.currentThread()
	if threadState == nil {
		return CompactionResult{}, fmt.Errorf("compact context: missing thread runtime")
	}
	_, threadHistory := threadState.Snapshot()
	selection := selectCompactionInputWithEstimator(providerVisibleMessages(threadHistory), policy, e.estimateMessageTokens)
	if len(selection.SummaryInput) == 0 && !selection.HasPreviousSummary {
		return CompactionResult{}, nil
	}
	summaryState, err := e.compactionSummaryStateLocked(ctx, policy)
	if err != nil {
		compactErr := newCompactionError(ctx, err)
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, compactErr)
	}
	prePolicy, err := runtimemodule.ApplyCompactionPolicies(ctx, runtimemodule.CompactionPolicyRequest{
		Runtime:  e.policyRuntimeContext(),
		Thread:   e.policyThreadContext(),
		TurnID:   turnID,
		Stage:    runtimemodule.CompactionPolicyBefore,
		Reason:   reason,
		Auto:     auto,
		Observer: e.policyObserver(turnID),
	}, e.policySets()...)
	if err != nil {
		compactErr := newCompactionError(ctx, err)
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, compactErr)
	}
	instructions = mergeCompactInstructions(policy.Instructions, instructions)
	instructions = mergeCompactInstructions(append([]string{instructions}, prePolicy.Instructions...)...)
	summaryInput, retainedInputReferences, projection, err := e.projectOversizedCompactionInputsLocked(selection.SummaryInput, selection.OversizedInputIDs, policy)
	if err != nil {
		compactErr := newCompactionError(ctx, err)
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, compactErr)
	}
	if err := e.emitProjectionApplied(turnID, projection); err != nil {
		compactErr := newCompactionError(ctx, fmt.Errorf("commit compaction input projection: %w", err))
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, compactErr)
	}
	retainedInputReferences, err = e.carryCompactionInputReferencesLocked(selection.PreviousSummary, retainedInputReferences, policy)
	if err != nil {
		compactErr := newCompactionError(ctx, err)
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, compactErr)
	}

	if contextWindow <= 0 {
		contextWindow = DefaultContextWindowTokens
	}
	sections, err := e.moduleRuntimeContextSections(ctx, e.ThreadRuntimeSnapshot())
	if err != nil {
		compactErr := newCompactionError(ctx, fmt.Errorf("runtime: build compaction context: %w", err))
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, compactErr)
	}
	policyContext := e.pendingPolicyRuntimeContextSnapshot()
	active := appendRuntimeContextMessages(assembleActiveContext(threadHistory, incoming), runtimeContextMessages(sections)...)
	active = appendRuntimeContextMessages(active, policyContext...)
	tokensBefore := e.estimateContextTokens(systemPrompt, tools, active.Messages)
	if err := e.emit(events.Event{Type: "context.compact.started", TurnID: turnID, Payload: ContextCompactStartedPayload{
		Reason:           reason,
		Auto:             auto,
		EstimatedTokens:  tokensBefore,
		TokensBefore:     tokensBefore,
		ContextWindow:    contextWindow,
		ReserveTokens:    policy.ReserveTokens,
		KeepRecentTokens: policy.KeepRecentTokens,
	}}); err != nil {
		return CompactionResult{}, fmt.Errorf("commit compaction start: %w", err)
	}

	// Reserve summary space against exactly the frozen context that will be
	// committed, including tools and module state outside the model's output.
	sections, err = runtimemodule.ProjectCompactionContext(sections, summaryState.Contributions)
	if err != nil {
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, newCompactionError(ctx, err))
	}
	newSummaryMessage := func(summary string) llm.Message {
		msg := llm.TextMessage(llm.RoleUser, compactMessageText(appendCompactionInputReferences(summary, retainedInputReferences)))
		msg.Kind = llm.MessageKindCompact
		msg.Compaction = &llm.CompactionMetadata{
			Auto: auto, Reason: reason,
			FirstKeptMessageID:      selection.FirstKeptMessageID,
			TailStartMessageID:      selection.TailStartMessageID,
			RetainedMessageIDs:      append([]string(nil), selection.RetainedMessageIDs...),
			RetainedInputReferences: append([]llm.Message(nil), retainedInputReferences...),
			TokensBefore:            tokensBefore, SummaryChars: len(summary),
		}
		if selection.HasPreviousSummary {
			msg.Compaction.PreviousSummaryID = selection.PreviousSummary.ID
		}
		return msg
	}
	projectSummary := func(msg llm.Message) ([]llm.Message, error) {
		simulated := append(append([]llm.Message(nil), threadHistory...), msg)
		compacted := assembleActiveContext(simulated, incoming)
		compacted.Messages = append(compacted.Messages, runtimeContextMessages(sections)...)
		compacted.Messages = append(compacted.Messages, policyContext...)
		projected, _, err := e.projectMessagesForProviderLocked(ctx, compacted.Messages, policy)
		return projected, err
	}
	// A nonempty placeholder also exercises provider URI expansion for retained references.
	minimum, err := projectSummary(newSummaryMessage("."))
	if err != nil {
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, newCompactionError(ctx, err))
	}
	baseTokens := estimateContextTokens(systemPrompt, tools, minimum)
	// Calibration applies to the whole request. Binary search preserves its
	// rounding behavior instead of subtracting independently rounded counts.
	low, high := 0, policy.SummaryMaxTokens
	for low < high {
		mid := low + (high-low+1)/2
		if e.applyTokenEstimateCalibration(baseTokens+mid) <= policy.TriggerTokens {
			low = mid
		} else {
			high = mid - 1
		}
	}
	if low == 0 {
		err := fmt.Errorf("compacted context exceeds budget before summary: %d tokens, limit %d; system, tools, retained input and module state leave no summary room", e.applyTokenEstimateCalibration(baseTokens), policy.TriggerTokens)
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, newCompactionError(ctx, err))
	}
	policy.SummaryMaxTokens = low
	previousModelSummary := compactionModelSummary(selection.PreviousSummary)
	generation, err := e.generateCompactionSummaryLocked(ctx, turnID, systemPrompt, previousModelSummary, summaryInput, summaryState, policy, instructions, contextWindow, reservedModelRef)
	if err != nil {
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, newCompactionError(ctx, err))
	}
	if contextErr := cancellation.ContextError(ctx); contextErr != nil {
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, newCompactionError(ctx, contextErr))
	}
	model := generation.Response.Message.Model
	if model == "" && generation.Provider != nil {
		model = generation.Provider.Name()
	}
	summaryChars := len(generation.Summary)
	msg := newSummaryMessage(generation.Summary)
	msg.Compaction.SummaryModel = model
	projectedAfter, err := projectSummary(msg)
	if err != nil {
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, newCompactionError(ctx, fmt.Errorf("project compacted context: %w", err)))
	}
	tokensAfter := e.estimateContextTokens(systemPrompt, tools, projectedAfter)
	if tokensAfter > policy.TriggerTokens {
		err := fmt.Errorf("compacted context exceeds budget: %d tokens, limit %d", tokensAfter, policy.TriggerTokens)
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, newCompactionError(ctx, err))
	}
	msg.Compaction.TokensAfter = tokensAfter
	contextUsage := llm.ContextUsage{
		Model:         model,
		ContextWindow: contextWindow,
		InputTokens:   tokensAfter,
		TotalTokens:   tokensAfter,
		Breakdown: []llm.ContextUsagePart{
			{Key: "active_context", Label: "active context after compaction", Tokens: tokensAfter},
		},
	}
	if err := e.commitCompactionMarker(ctx, operationGeneration, func() error {
		change, err := runtimemodule.StageContextTransition(ctx, e.ThreadRuntimeSnapshot().Modules, runtimemodule.ContextTransitionCompact, threadState.Projection().CurrentGeneration.ID)
		if err != nil {
			return err
		}
		if _, err := threadState.BeginCompactedGeneration(msg, auto, &contextUsage); err != nil {
			var persistErr *thread.ProjectionPersistError
			if errors.As(err, &persistErr) {
				return errors.Join(err, change.Finalize())
			}
			return errors.Join(fmt.Errorf("thread begin compacted generation: %w", err), change.Rollback())
		}
		return change.Finalize()
	}); err != nil {
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, err)
	}
	e.autoCompactFailures = 0
	replay := threadState.ReplaySnapshot()
	if len(replay.Activities) > 0 {
		activity := replay.Activities[len(replay.Activities)-1]
		if activity.Summary != nil {
			msg = *activity.Summary
		}
	}
	result := CompactionResult{
		MessageID:          msg.ID,
		Reason:             reason,
		Auto:               auto,
		TokensBefore:       tokensBefore,
		TokensAfter:        tokensAfter,
		SummaryChars:       summaryChars,
		SummaryModel:       model,
		TailStartMessageID: selection.TailStartMessageID,
		FirstKeptMessageID: selection.FirstKeptMessageID,
	}
	if err := e.emit(events.Event{Type: "context.compact.completed", TurnID: turnID, Payload: ContextCompactCompletedPayload{
		MessageID:          result.MessageID,
		Reason:             result.Reason,
		Auto:               result.Auto,
		EstimatedTokens:    result.TokensBefore,
		TokensBefore:       result.TokensBefore,
		TokensAfter:        result.TokensAfter,
		SummaryChars:       result.SummaryChars,
		SummaryModel:       result.SummaryModel,
		TailStartMessageID: result.TailStartMessageID,
		ContextWindow:      contextWindow,
		ReserveTokens:      policy.ReserveTokens,
		KeepRecentTokens:   policy.KeepRecentTokens,
		ContextUsage:       &contextUsage,
	}}); err != nil {
		return result, fmt.Errorf("commit compaction completion: %w", err)
	}
	postPolicy, postErr := runtimemodule.ApplyCompactionPolicies(ctx, runtimemodule.CompactionPolicyRequest{
		Runtime:  e.policyRuntimeContext(),
		Thread:   e.policyThreadContext(),
		TurnID:   turnID,
		Stage:    runtimemodule.CompactionPolicyAfter,
		Reason:   reason,
		Auto:     auto,
		Observer: e.policyObserver(turnID),
	}, e.policySets()...)
	// Ordinary policy failures are observational after commit. Cancellation still
	// reaches the caller, while the committed Generation and earlier context remain.
	if err := e.queuePolicyRuntimeContext(postPolicy.Context); err != nil {
		return result, err
	}
	if runtimemodule.IsPolicyCheckpointError(postErr) || runtimemodule.IsPolicyContextValidationError(postErr) ||
		errors.Is(postErr, context.Canceled) || errors.Is(postErr, context.DeadlineExceeded) {
		return result, postErr
	}
	return result, nil
}

func (e *Engine) reportCompactionError(turnID, reason string, auto bool, compactErr error) error {
	emitErr := e.emit(events.Event{Type: "context.compact.errored", TurnID: turnID, Payload: ContextCompactErroredPayload{
		Reason: reason,
		Auto:   auto,
		Error:  compactErr.Error(),
	}})
	if emitErr != nil {
		return errors.Join(compactErr, fmt.Errorf("commit compaction error: %w", emitErr))
	}
	return compactErr
}

func (e *Engine) commitCompactionMarker(ctx context.Context, operationGeneration uint64, commit func() error) error {
	e.activeOperationMu.Lock()
	defer e.activeOperationMu.Unlock()
	if contextErr := cancellation.ContextError(ctx); contextErr != nil {
		return newCompactionError(ctx, contextErr)
	}
	if err := commit(); err != nil {
		return err
	}
	if operationGeneration != 0 && e.activeOperationGeneration == operationGeneration {
		e.activeOperationCancel = nil
	}
	return nil
}

func mergeCompactInstructions(parts ...string) string {
	merged := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			merged = append(merged, part)
		}
	}
	return strings.Join(merged, "\n\n")
}

const compactMessagePrefix = "Context compacted automatically because the provider context window is nearing its limit.\n\nSummary of earlier conversation:\n"

func compactMessageText(summary string) string {
	return compactMessagePrefix + summary
}

func compactionModelSummary(msg llm.Message) llm.Message {
	if msg.Compaction == nil || msg.Compaction.SummaryChars <= 0 {
		return msg
	}
	text := strings.TrimPrefix(msg.FirstText(), compactMessagePrefix)
	if msg.Compaction.SummaryChars > len(text) {
		return msg
	}
	text = text[:msg.Compaction.SummaryChars]
	out := msg
	out.Blocks = append([]llm.Block(nil), msg.Blocks...)
	for i := range out.Blocks {
		if out.Blocks[i].Type == llm.BlockText {
			out.Blocks[i].Text = text
			break
		}
	}
	return out
}

func (e *Engine) compactionToolsLocked() []llm.ToolSpec {
	if e == nil || e.Tools == nil {
		return nil
	}
	return e.Tools.Specs()
}

func (e *Engine) compactionSummaryCandidatesLocked(policy compactionPolicy) []ModelCandidate {
	if e == nil {
		return nil
	}
	candidates := make([]ModelCandidate, 0, len(e.ModelCandidates)+1)
	if e.SummaryProvider != nil {
		ref := strings.TrimSpace(policy.SummaryModel)
		if ref == "" {
			ref = strings.TrimSpace(e.SummaryProvenance.Model)
		}
		if ref == "" {
			ref = e.SummaryProvider.Name()
		}
		candidates = append(candidates, ModelCandidate{
			Ref:           ref,
			Provider:      e.SummaryProvider,
			Provenance:    e.SummaryProvenance,
			ContextWindow: e.SummaryContextWindow,
		})
	}
	seen := make(map[string]struct{}, len(candidates)+len(e.ModelCandidates))
	for _, candidate := range candidates {
		seen[compactionSummaryCandidateRef(candidate)] = struct{}{}
	}
	for _, candidate := range e.effectiveModelCandidatesLocked() {
		ref := compactionSummaryCandidateRef(candidate)
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return candidates
}

func compactionSummaryCandidateRef(candidate ModelCandidate) string {
	if ref := strings.TrimSpace(candidate.Ref); ref != "" {
		return ref
	}
	if candidate.Provider != nil {
		return candidate.Provider.Name()
	}
	return ""
}
