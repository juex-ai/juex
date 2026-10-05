package managedruntime

import (
	"context"
	"errors"
	"slices"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

// Model histories are projections of immutable messages. Provider-specific
// reasoning IDs/signatures never cross a model or route boundary.
func modelHistory(work Work, target ModelConfig) []llm.Message {
	history := slices.Clone(work.History)
	for i, message := range history {
		origin, known := work.ModelOrigins[message.ID]
		compatible := known && origin.ModelID == target.ModelID && origin.Provider == target.Provider && origin.Model == target.Model && origin.Protocol == target.Protocol && origin.Endpoint == target.Endpoint && origin.ModelAuthorizationEpoch == target.ModelAuthorizationEpoch
		if compatible {
			continue
		}
		history[i].Blocks = slices.DeleteFunc(slices.Clone(message.Blocks), func(block llm.Block) bool { return block.Type == llm.BlockReasoning })
	}
	return history
}

func (r *Runner) selectModel(ctx context.Context, lease Lease, work Work, request ModelRequest) (llm.Provider, ModelRequest, error) {
	base := request
	for index := work.ModelIndex; index < len(work.Config.Models); index++ {
		model := work.Config.Models[index]
		request = base
		request.Model = model
		request.Generation = work.Generation
		request.MaxOutputTokens = model.MaxOutput
		request.Messages = projectModelHistory(work, model)
		if base.Recall != nil && work.Compaction == nil {
			withRecall := append([]llm.Message{*base.Recall}, request.Messages...)
			if llm.EstimateContextTokens(request.System, request.Tools, withRecall)+model.OutputReserve+contextSafety(model) <= model.ContextWindow {
				request.Messages = withRecall
			}
		}
		reason := "model_unavailable"
		var planErr error
		if work.Source.Kind == "compaction" || work.Compaction != nil || compactionNeeded(work, request, model) {
			request, planErr = planCompaction(work, base, model)
			if planErr == nil {
				// The summary body contains textual references. Validate the source
				// before that projection can summarize an unavailable image away.
				if _, err := hydrateMedia(ctx, r.config.Media, work.Scope, work.History); err != nil {
					return nil, request, err
				}
			}
			if errors.Is(planErr, ErrNoCompaction) && work.Source.Kind != "compaction" {
				planErr = ErrContextLimit
			}
			if work.Compaction == nil && (planErr == nil || errors.Is(planErr, ErrNoCompaction)) {
				reason := "automatic"
				if work.Source.Kind == "compaction" {
					reason = "manual"
				}
				job, err := r.store.PrepareCompaction(ctx, lease, work, reason, work.Source.Focus)
				if err != nil {
					return nil, request, err
				}
				work.Compaction = job
				if request.Compaction != nil {
					request.Compaction.JobID = job.ID
				}
			}
		}
		if planErr != nil && !errors.Is(planErr, ErrContextLimit) {
			return nil, request, planErr
		}
		if planErr == nil && llm.EstimateContextTokens(request.System, request.Tools, request.Messages)+outputBudget(request)+contextSafety(model) <= model.ContextWindow {
			messages, err := hydrateMedia(ctx, r.config.Media, work.Scope, request.Messages)
			if err != nil {
				return nil, request, err
			}
			request.Messages = messages
			provider, err := r.authority.Provider(ctx, work.Scope, model)
			if err == nil {
				return provider, request, nil
			}
			if !errors.Is(err, ErrModelUnavailable) {
				return nil, request, err
			}
		} else {
			reason = "context_limit"
		}

		if index+1 == len(work.Config.Models) && reason == "context_limit" {
			return nil, request, ErrContextLimit
		}
		if err := r.store.AdvanceModel(ctx, lease, work.TurnID, index, reason); err != nil {
			return nil, request, err
		}
	}
	return nil, request, ErrModelUnavailable
}

// Create references before removing provider-specific blocks so their offsets
// continue to address the immutable original message across model switches.
func projectModelHistory(work Work, model ModelConfig) []llm.Message {
	work.History = projectContext(work.History, model)
	return modelHistory(work, model)
}

// Normal requests may leave the provider cap unset while still reserving output
// capacity. A compaction draft instead has its own bounded summary request.
func outputBudget(request ModelRequest) int {
	if request.Compaction != nil {
		return request.MaxOutputTokens
	}
	return request.Model.OutputReserve
}
