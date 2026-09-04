package contextbudget

import runtimepolicy "github.com/juex-ai/juex/internal/runtime/policy"

type CompactionPolicy = runtimepolicy.CompactionPolicy

type Policy struct {
	Enabled                   bool
	Instructions              string
	ContextWindow             int
	ReserveTokens             int
	KeepRecentTokens          int
	SummaryModel              string
	SummaryRequestTokens      int
	SummaryMaxTokens          int
	ToolResultMaxTokens       int
	ToolResultMaxChars        int
	UserInputInlineMaxBytes   int
	UserInputPreviewHeadBytes int
	UserInputPreviewTailBytes int
	MaxAutoFailures           int
	TriggerTokens             int
}

func DefaultCompactionPolicy() CompactionPolicy {
	return runtimepolicy.DefaultCompactionPolicy()
}

func EffectivePolicy(policy CompactionPolicy, contextWindow int, defaultContextWindow int) Policy {
	if contextWindow <= 0 {
		contextWindow = defaultContextWindow
	}
	defaults := DefaultCompactionPolicy()
	trigger := scaledContextBudget(contextWindow, 80, 100)
	if policy.ReserveTokens > 0 {
		configuredTrigger := contextWindow - policy.ReserveTokens
		if configuredTrigger < 1 {
			configuredTrigger = 1
		}
		trigger = minPositiveBudget(trigger, configuredTrigger)
	}
	reserve := contextWindow - trigger
	keep := cappedContextBudget(scaledContextBudget(contextWindow, 5, 64), policy.KeepRecentTokens)
	summaryRequest := scaledContextBudget(contextWindow, 90, 100)
	summaryMax := cappedContextBudget(summaryOutputTokenBudget(contextWindow), policy.SummaryMaxTokens)
	toolMaxTokens, toolMaxChars := toolResultContentBudgets(contextWindow, policy.ToolResultMaxChars)
	userInlineMax := policy.UserInputInlineMaxBytes
	if userInlineMax <= 0 {
		userInlineMax = defaults.UserInputInlineMaxBytes
	}
	userHead := policy.UserInputPreviewHeadBytes
	if userHead <= 0 {
		userHead = defaults.UserInputPreviewHeadBytes
	}
	userTail := policy.UserInputPreviewTailBytes
	if userTail <= 0 {
		userTail = defaults.UserInputPreviewTailBytes
	}
	maxFailures := policy.MaxAutoFailures
	if maxFailures <= 0 {
		maxFailures = defaults.MaxAutoFailures
	}
	return Policy{
		Enabled:                   policy.Enabled,
		Instructions:              policy.Instructions,
		ContextWindow:             contextWindow,
		ReserveTokens:             reserve,
		KeepRecentTokens:          keep,
		SummaryModel:              policy.SummaryModel,
		SummaryRequestTokens:      summaryRequest,
		SummaryMaxTokens:          summaryMax,
		ToolResultMaxTokens:       toolMaxTokens,
		ToolResultMaxChars:        toolMaxChars,
		UserInputInlineMaxBytes:   userInlineMax,
		UserInputPreviewHeadBytes: userHead,
		UserInputPreviewTailBytes: userTail,
		MaxAutoFailures:           maxFailures,
		TriggerTokens:             trigger,
	}
}

const (
	smallContextWindow  = 100_000
	largeContextWindow  = 1_000_000
	smallSummaryTokens  = 1_000
	mediumSummaryTokens = 2_000
	smallToolTokens     = 500
	mediumToolTokens    = 2_000
)

func summaryOutputTokenBudget(contextWindow int) int {
	switch {
	case contextWindow < smallContextWindow:
		return smallSummaryTokens
	case contextWindow < largeContextWindow:
		return mediumSummaryTokens
	default:
		return scaledContextBudget(contextWindow, 1, 200)
	}
}

func toolResultContentBudgets(contextWindow, configuredMaxChars int) (maxTokens, maxChars int) {
	switch {
	case contextWindow < smallContextWindow:
		maxTokens = smallToolTokens
	case contextWindow < largeContextWindow:
		maxTokens = mediumToolTokens
	default:
		maxChars = scaledContextBudget(contextWindow, 1, 200)
	}
	if configuredMaxChars > 0 {
		if maxChars > 0 {
			maxChars = minPositiveBudget(maxChars, configuredMaxChars)
		} else {
			maxChars = configuredMaxChars
		}
	}
	return maxTokens, maxChars
}

// ToolResultTokenBudget returns the default provider-visible content budget
// for context windows covered by the stepped token policy. A zero result means
// the legacy unit-specific 0.5% policy applies instead.
func ToolResultTokenBudget(contextWindow int) int {
	maxTokens, _ := toolResultContentBudgets(contextWindow, 0)
	return maxTokens
}

func scaledContextBudget(contextWindow, numerator, denominator int) int {
	if contextWindow <= 0 || numerator <= 0 || denominator <= 0 {
		return 1
	}
	budget := int(int64(contextWindow) * int64(numerator) / int64(denominator))
	if budget < 1 {
		return 1
	}
	return budget
}

func cappedContextBudget(derived, configured int) int {
	if configured > 0 {
		return minPositiveBudget(derived, configured)
	}
	return derived
}

func minPositiveBudget(a, b int) int {
	if a < b {
		return a
	}
	return b
}
