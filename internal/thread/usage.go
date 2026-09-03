package thread

import (
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
)

func (u *UsageAggregate) Add(modelRef string, usage llm.Usage) {
	if u == nil {
		return
	}
	if u.ByModel == nil {
		u.ByModel = make(map[string]llm.Usage)
	}
	total := u.ByModel[modelRef]
	total.Add(usage)
	u.ByModel[modelRef] = total
	u.Total.Add(usage)
}

func (u UsageAggregate) Clone() UsageAggregate {
	clone := UsageAggregate{Total: u.Total, ByModel: make(map[string]llm.Usage, len(u.ByModel))}
	for modelRef, usage := range u.ByModel {
		clone.ByModel[modelRef] = usage
	}
	return clone
}

func (u UsageAggregate) IsZero() bool {
	return u.Total.IsZero() && len(u.ByModel) == 0
}

func validateUsageAggregate(aggregate UsageAggregate) error {
	if err := validateUsage(aggregate.Total); err != nil {
		return fmt.Errorf("invalid total Usage: %w", err)
	}
	sum := llm.Usage{}
	for modelRef, usage := range aggregate.ByModel {
		if err := validateModelRef(modelRef); err != nil {
			return err
		}
		if err := validateUsage(usage); err != nil {
			return fmt.Errorf("invalid Usage for model %q: %w", modelRef, err)
		}
		sum.Add(usage)
	}
	if sum != aggregate.Total {
		return fmt.Errorf("total Usage %+v does not equal by-model sum %+v", aggregate.Total, sum)
	}
	return nil
}

func validateUsage(usage llm.Usage) error {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CachedInputTokens < 0 {
		return fmt.Errorf("negative token count")
	}
	if usage.CachedInputTokens > usage.InputTokens {
		return fmt.Errorf("cached input %d exceeds input %d", usage.CachedInputTokens, usage.InputTokens)
	}
	return nil
}

func validateModelRef(modelRef string) error {
	providerID, modelID, ok := strings.Cut(modelRef, ":")
	if !ok || providerID == "" || modelID == "" || strings.TrimSpace(providerID) != providerID || strings.TrimSpace(modelID) != modelID {
		return fmt.Errorf("%w: model_ref %q must be canonical provider:model", ErrInvalidFact, modelRef)
	}
	return nil
}
