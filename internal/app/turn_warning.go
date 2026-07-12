package app

import (
	"fmt"
	"strings"
)

const TurnWarningAttachmentVisionUnavailable = "attachment_vision_unavailable"

// TurnWarning is a non-blocking application warning rendered by transports.
type TurnWarning struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// AttachmentWarnings reports capability mismatches for an accepted image turn.
func (a *App) AttachmentWarnings(attachmentCount int) []TurnWarning {
	if a == nil || attachmentCount <= 0 {
		return nil
	}
	profile, err := a.cfg.ProviderSelection().ProviderProfile()
	if err != nil || profile.Capabilities.Vision {
		return nil
	}
	model := strings.Trim(strings.TrimSpace(profile.ID)+":"+strings.TrimSpace(profile.Model), ":")
	if model == "" {
		model = "the selected model"
	}
	return []TurnWarning{{
		Code: TurnWarningAttachmentVisionUnavailable,
		Message: fmt.Sprintf(
			"model %q cannot view attached image content because vision is disabled",
			model,
		),
		Suggestion: "use a vision-capable model or enable providers[].models[].capabilities.vision only when the model supports image input",
	}}
}
