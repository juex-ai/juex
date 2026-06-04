package runtime

import "github.com/juex-ai/juex/internal/config"

type compactionPolicy struct {
	Enabled                    bool
	ReserveTokens              int
	KeepRecentTokens           int
	TailTurns                  int
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

func effectiveCompactionPolicy(cfg config.CompactionConfig, contextWindow int) compactionPolicy {
	if contextWindow <= 0 {
		contextWindow = DefaultContextWindowTokens
	}
	if cfg.ReserveTokens <= 0 && cfg.KeepRecentTokens <= 0 && cfg.TailTurns <= 0 && cfg.SummaryMaxTokens <= 0 && cfg.ToolResultMaxChars <= 0 {
		cfg = config.DefaultCompactionConfig()
	}
	reserve := cfg.ReserveTokens
	if reserve <= 0 {
		reserve = 16384
	}
	maxReserve := maxInt(1024, contextWindow/4)
	if reserve > maxReserve {
		reserve = maxReserve
	}
	keep := cfg.KeepRecentTokens
	if keep <= 0 {
		keep = 20000
	}
	maxKeep := maxInt(512, contextWindow/3)
	if keep > maxKeep {
		keep = maxKeep
	}
	tailTurns := cfg.TailTurns
	if tailTurns <= 0 {
		tailTurns = 2
	}
	summaryMax := cfg.SummaryMaxTokens
	if summaryMax <= 0 {
		summaryMax = 2048
	}
	toolMax := cfg.ToolResultMaxChars
	if toolMax <= 0 {
		toolMax = 2000
	}
	userInlineMax := cfg.UserInputInlineMaxBytes
	if userInlineMax <= 0 {
		userInlineMax = 65536
	}
	userHead := cfg.UserInputPreviewHeadBytes
	if userHead <= 0 {
		userHead = 8192
	}
	userTail := cfg.UserInputPreviewTailBytes
	if userTail <= 0 {
		userTail = 8192
	}
	toolInlineMax := cfg.ToolResultInlineMaxBytes
	if toolInlineMax <= 0 {
		toolInlineMax = 32768
	}
	toolHead := cfg.ToolResultPreviewHeadBytes
	if toolHead <= 0 {
		toolHead = 8192
	}
	toolTail := cfg.ToolResultPreviewTailBytes
	if toolTail <= 0 {
		toolTail = 8192
	}
	maxFailures := cfg.MaxAutoFailures
	if maxFailures <= 0 {
		maxFailures = 3
	}
	trigger := contextWindow - reserve
	if trigger <= 0 {
		trigger = maxInt(1, contextWindow/2)
	}
	return compactionPolicy{
		Enabled:                    cfg.Enabled,
		ReserveTokens:              reserve,
		KeepRecentTokens:           keep,
		TailTurns:                  tailTurns,
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
