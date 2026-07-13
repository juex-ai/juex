package contextbudget

import runtimepolicy "github.com/juex-ai/juex/internal/runtime/policy"

type CompactionPolicy = runtimepolicy.CompactionPolicy

type Policy struct {
	Enabled                    bool
	Instructions               string
	ReserveTokens              int
	KeepRecentTokens           int
	TailTurns                  int
	SummaryModel               string
	SummaryMaxTokens           int
	ToolResultMaxChars         int
	UserInputInlineMaxBytes    int
	UserInputPreviewHeadBytes  int
	UserInputPreviewTailBytes  int
	ToolResultInlineMaxBytes   int
	ToolResultPreviewHeadBytes int
	ToolResultPreviewTailBytes int
	MaxAutoFailures            int
	TriggerTokens              int
}

func DefaultCompactionPolicy() CompactionPolicy {
	return runtimepolicy.DefaultCompactionPolicy()
}

func EffectivePolicy(policy CompactionPolicy, contextWindow int, defaultContextWindow int) Policy {
	if contextWindow <= 0 {
		contextWindow = defaultContextWindow
	}
	defaults := DefaultCompactionPolicy()
	if policy.ReserveTokens <= 0 && policy.KeepRecentTokens <= 0 && policy.TailTurns <= 0 && policy.SummaryMaxTokens <= 0 && policy.ToolResultMaxChars <= 0 {
		enabled := policy.Enabled
		instructions := policy.Instructions
		policy = defaults
		policy.Enabled = enabled
		policy.Instructions = instructions
	}
	reserve := policy.ReserveTokens
	if reserve <= 0 {
		reserve = defaults.ReserveTokens
	}
	maxReserve := maxInt(1024, contextWindow/4)
	if reserve > maxReserve {
		reserve = maxReserve
	}
	keep := policy.KeepRecentTokens
	if keep <= 0 {
		keep = defaults.KeepRecentTokens
	}
	maxKeep := maxInt(512, contextWindow/3)
	if keep > maxKeep {
		keep = maxKeep
	}
	tailTurns := policy.TailTurns
	if tailTurns <= 0 {
		tailTurns = defaults.TailTurns
	}
	summaryMax := policy.SummaryMaxTokens
	if summaryMax <= 0 {
		summaryMax = defaults.SummaryMaxTokens
	}
	toolMax := policy.ToolResultMaxChars
	if toolMax <= 0 {
		toolMax = defaults.ToolResultMaxChars
	}
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
	toolInlineMax := policy.ToolResultInlineMaxBytes
	if toolInlineMax <= 0 {
		toolInlineMax = defaults.ToolResultInlineMaxBytes
	}
	toolHead := policy.ToolResultPreviewHeadBytes
	if toolHead <= 0 {
		toolHead = defaults.ToolResultPreviewHeadBytes
	}
	toolTail := policy.ToolResultPreviewTailBytes
	if toolTail <= 0 {
		toolTail = defaults.ToolResultPreviewTailBytes
	}
	maxFailures := policy.MaxAutoFailures
	if maxFailures <= 0 {
		maxFailures = defaults.MaxAutoFailures
	}
	trigger := contextWindow - reserve
	if trigger <= 0 {
		trigger = maxInt(1, contextWindow/2)
	}
	return Policy{
		Enabled:                    policy.Enabled,
		Instructions:               policy.Instructions,
		ReserveTokens:              reserve,
		KeepRecentTokens:           keep,
		TailTurns:                  tailTurns,
		SummaryModel:               policy.SummaryModel,
		SummaryMaxTokens:           summaryMax,
		ToolResultMaxChars:         toolMax,
		UserInputInlineMaxBytes:    userInlineMax,
		UserInputPreviewHeadBytes:  userHead,
		UserInputPreviewTailBytes:  userTail,
		ToolResultInlineMaxBytes:   toolInlineMax,
		ToolResultPreviewHeadBytes: toolHead,
		ToolResultPreviewTailBytes: toolTail,
		MaxAutoFailures:            maxFailures,
		TriggerTokens:              trigger,
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
