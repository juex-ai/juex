package profile

import (
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type Config struct {
	ID             string
	Protocol       string
	BaseURL        string
	APIKey         string
	Model          string
	ThinkingEffort string // "low", "medium", "high", "xhigh", "max", or "" (provider default)
	Headers        map[string]string
	Query          map[string]string
	Capabilities   llm.CapabilityOverrides
	Compat         llm.CompatOptions
	MediaDir       string
}
