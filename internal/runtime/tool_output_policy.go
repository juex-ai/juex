package runtime

import runtimepolicy "github.com/juex-ai/juex/internal/runtime/policy"

import "github.com/juex-ai/juex/internal/runtime/contextbudget"

type ToolOutputPolicy = runtimepolicy.ToolOutputPolicy

func DefaultToolOutputPolicy() ToolOutputPolicy {
	return runtimepolicy.DefaultToolOutputPolicy()
}

type effectiveToolOutput struct {
	ContentMaxTokens int
	InlineMaxBytes   int
	PreviewHeadBytes int
	PreviewTailBytes int
}

func effectiveToolOutputPolicy(policy ToolOutputPolicy, contextWindow int) effectiveToolOutput {
	effective := effectiveToolOutput{
		ContentMaxTokens: contextbudget.ToolResultTokenBudget(contextWindow),
		InlineMaxBytes:   policy.InlineMaxBytes,
		PreviewHeadBytes: policy.PreviewHeadBytes,
		PreviewTailBytes: policy.PreviewTailBytes,
	}
	if effective.ContentMaxTokens == 0 {
		limit := contextWindow / 200
		if limit < 1 {
			limit = 1
		}
		if effective.InlineMaxBytes <= 0 || effective.InlineMaxBytes > limit {
			effective.InlineMaxBytes = limit
		}
	}
	if effective.InlineMaxBytes <= 0 {
		return effective
	}
	previewLimit := effective.InlineMaxBytes
	headConfigured := effective.PreviewHeadBytes > 0
	tailConfigured := effective.PreviewTailBytes > 0
	switch {
	case !headConfigured && !tailConfigured:
		effective.PreviewHeadBytes = previewLimit / 2
		effective.PreviewTailBytes = previewLimit - effective.PreviewHeadBytes
	case headConfigured && !tailConfigured:
		if effective.PreviewHeadBytes > previewLimit {
			effective.PreviewHeadBytes = previewLimit
		}
		effective.PreviewTailBytes = previewLimit - effective.PreviewHeadBytes
	case !headConfigured && tailConfigured:
		if effective.PreviewTailBytes > previewLimit {
			effective.PreviewTailBytes = previewLimit
		}
		effective.PreviewHeadBytes = previewLimit - effective.PreviewTailBytes
	case headConfigured && tailConfigured:
		if effective.PreviewHeadBytes > previewLimit {
			effective.PreviewHeadBytes = previewLimit
		}
		remaining := previewLimit - effective.PreviewHeadBytes
		if effective.PreviewTailBytes > remaining {
			effective.PreviewTailBytes = remaining
		}
	}
	return effective
}
