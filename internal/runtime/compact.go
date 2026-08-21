package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	runtimepolicy "github.com/juex-ai/juex/internal/runtime/policy"
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
		err := fmt.Errorf("auto compaction paused after %d consecutive failures; run /compact with focus instructions or start a new session", policy.MaxAutoFailures)
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

	_, err = e.compactLocked(ctx, turnID, systemPrompt, tools, "auto", true, "", 0)
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

func (e *Engine) compactLocked(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, reason string, auto bool, instructions string, operationGeneration uint64) (CompactionResult, error) {
	return e.compactLockedForContextWindow(ctx, turnID, systemPrompt, tools, reason, auto, instructions, e.ContextWindow, operationGeneration)
}

func (e *Engine) compactLockedForContextWindow(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, reason string, auto bool, instructions string, contextWindow int, operationGeneration uint64) (CompactionResult, error) {
	policy := effectiveCompactionPolicy(e.Compaction, contextWindow)
	if !policy.Enabled {
		return CompactionResult{}, nil
	}
	sess := e.currentSession()
	if sess == nil {
		return CompactionResult{}, fmt.Errorf("compact context: missing session runtime")
	}
	selection := selectCompactionInputWithEstimator(providerVisibleMessages(sess.History), policy, e.estimateMessageTokens)
	if len(selection.SummaryInput) == 0 && !selection.HasPreviousSummary {
		return CompactionResult{}, nil
	}
	summaryState, err := e.compactionSummaryStateLocked()
	if err != nil {
		compactErr := newCompactionError(ctx, err)
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, compactErr)
	}
	prePolicy, err := runtimemodule.ApplyCompactionPolicies(ctx, runtimemodule.CompactionPolicyRequest{
		Runtime:  e.policyRuntimeContext(),
		Session:  e.policySessionContext(),
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
	active, err := e.activeContextLockedWithPolicyContextError(ctx, e.pendingPolicyRuntimeContextSnapshot())
	if err != nil {
		compactErr := newCompactionError(ctx, fmt.Errorf("runtime: build compaction context: %w", err))
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, compactErr)
	}
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

	previousModelSummary := compactionModelSummary(selection.PreviousSummary)
	generation, err := e.generateCompactionSummaryLocked(ctx, turnID, systemPrompt, previousModelSummary, summaryInput, summaryState, policy, instructions)
	if err != nil {
		sess.RecordResponseUsage(generation.Usage, nil)
		compactErr := newCompactionError(ctx, err)
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, compactErr)
	}
	resp := generation.Response
	summaryProvider := generation.Provider
	summaryChars := len(generation.Summary)
	summary := appendCompactionInputReferences(generation.Summary, retainedInputReferences)
	if contextErr := cancellation.ContextError(ctx); contextErr != nil {
		compactErr := newCompactionError(ctx, contextErr)
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, compactErr)
	}

	model := resp.Message.Model
	if model == "" && summaryProvider != nil {
		model = summaryProvider.Name()
	}
	msg := llm.TextMessage(llm.RoleUser, compactMessageText(summary))
	msg.Kind = llm.MessageKindCompact
	msg.Compaction = &llm.CompactionMetadata{
		Auto:                    auto,
		Reason:                  reason,
		FirstKeptMessageID:      selection.FirstKeptMessageID,
		TailStartMessageID:      selection.TailStartMessageID,
		RetainedMessageIDs:      append([]string(nil), selection.RetainedMessageIDs...),
		RetainedInputReferences: append([]llm.Message(nil), retainedInputReferences...),
		TokensBefore:            tokensBefore,
		SummaryChars:            summaryChars,
		SummaryModel:            model,
	}
	if selection.HasPreviousSummary {
		msg.Compaction.PreviousSummaryID = selection.PreviousSummary.ID
	}
	simulated := make([]llm.Message, 0, len(sess.History)+1)
	simulated = append(simulated, sess.History...)
	simulated = append(simulated, msg)
	tokensAfter := e.estimateContextTokens(systemPrompt, tools, assembleActiveContext(simulated, nil).Messages)
	msg.Compaction.TokensAfter = tokensAfter
	if err := e.commitCompactionMarker(ctx, operationGeneration, func() error {
		if err := sess.Append(msg); err != nil {
			return fmt.Errorf("session append compact: %w", err)
		}
		return nil
	}); err != nil {
		return CompactionResult{}, e.reportCompactionError(turnID, reason, auto, err)
	}
	e.autoCompactFailures = 0
	if len(sess.History) > 0 {
		msg = sess.History[len(sess.History)-1]
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
	contextUsage := llm.ContextUsage{
		Model:         model,
		ContextWindow: contextWindow,
		InputTokens:   tokensAfter,
		TotalTokens:   tokensAfter,
		Breakdown: []llm.ContextUsagePart{
			{Key: "active_context", Label: "active context after compaction", Tokens: tokensAfter},
		},
	}
	sess.RecordResponseUsage(generation.Usage, &contextUsage)
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
		Session:  e.policySessionContext(),
		TurnID:   turnID,
		Stage:    runtimemodule.CompactionPolicyAfter,
		Reason:   reason,
		Auto:     auto,
		Observer: e.policyObserver(turnID),
	}, e.policySets()...)
	// Policy failures are observational after commit; keep context produced by
	// earlier successful policies when a later policy fails.
	if err := e.queuePolicyRuntimeContext(postPolicy.Context); err != nil {
		return result, err
	}
	if runtimemodule.IsPolicyCheckpointError(postErr) || runtimemodule.IsPolicyContextValidationError(postErr) {
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

func (e *Engine) compactionSummaryProviderLocked() llm.Provider {
	if e != nil && e.SummaryProvider != nil {
		return e.SummaryProvider
	}
	if e != nil {
		return e.Provider
	}
	return nil
}
