package cli

import (
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/providerreadiness"
)

const initNoConfigSuggestion = providerreadiness.InitSuggestion

func ensureSelectedRuntimeConfig(cfg config.Config) error {
	result := providerreadiness.CheckSelection(cfg)
	if result.Status == providerreadiness.StatusOK {
		return nil
	}
	return &usageError{msg: readinessMessage(result)}
}

func readinessMessage(result providerreadiness.Result) string {
	if result.Err != nil {
		return result.Err.Error()
	}
	if result.Suggestion != "" {
		return result.Message + "; " + result.Suggestion
	}
	return result.Message
}

func doctorStatusFromReadiness(status providerreadiness.Status) doctorStatus {
	switch status {
	case providerreadiness.StatusWarn:
		return doctorStatusWarn
	case providerreadiness.StatusFail:
		return doctorStatusFail
	default:
		return doctorStatusOK
	}
}

func doctorCheckFromReadiness(name string, result providerreadiness.Result) doctorCheck {
	return doctorCheck{
		Name:       name,
		Status:     doctorStatusFromReadiness(result.Status),
		Message:    result.Message,
		Suggestion: result.Suggestion,
		Details:    result.Details,
	}
}
