package managedruntime

import "github.com/juex-ai/juex/internal/foundation/llm"

// ApplicationModelBudget is frozen with the application job, independently of
// the authorized catalog. It can narrow a candidate but never authorize one.
type ApplicationModelBudget struct {
	ContextWindow int `json:"context_window"`
	MaxOutput     int `json:"max_output"`
}

func (b *ApplicationModelBudget) valid() bool {
	return b == nil || b.ContextWindow > 0 && b.MaxOutput > 0 && b.MaxOutput < b.ContextWindow
}

// ContextModel projects limits for budgeting only. Provider authorization must
// continue to use Model, including its original catalog limits and epochs.
func (r ModelRequest) ContextModel() ModelConfig {
	model := r.Model
	if r.ModelBudget != nil {
		model.ContextWindow = min(model.ContextWindow, r.ModelBudget.ContextWindow)
		if model.MaxOutput == 0 {
			model.MaxOutput = r.ModelBudget.MaxOutput
		} else {
			model.MaxOutput = min(model.MaxOutput, r.ModelBudget.MaxOutput)
		}
		// A known positive cap provides the reservation for this bounded job.
		model.OutputReserve = model.MaxOutput
	}
	return model
}

// ValidateModelBudget checks the persisted job policy at attempt admission.
// The request copy exists to retain the effective policy with historical calls.
func (r ModelRequest) ValidateModelBudget(budget *ApplicationModelBudget) error {
	if !budget.valid() || (r.ModelBudget == nil) != (budget == nil) || budget != nil && *r.ModelBudget != *budget {
		return ErrInvalid
	}
	model := r.ContextModel()
	if r.Purpose == "compaction" {
		if r.MaxOutputTokens <= 0 || r.MaxOutputTokens > model.OutputReserve {
			return ErrInvalid
		}
	} else if r.MaxOutputTokens != model.MaxOutput {
		return ErrInvalid
	}
	if budget != nil && llm.EstimateContextTokens(r.System, r.Tools, r.Messages)+outputBudget(r)+contextSafety(model) > model.ContextWindow {
		return ErrContextLimit
	}
	return nil
}
