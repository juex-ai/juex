package runtime

import runtimepolicy "github.com/juex-ai/juex/internal/runtime/policy"

type ToolOutputPolicy = runtimepolicy.ToolOutputPolicy

func DefaultToolOutputPolicy() ToolOutputPolicy {
	return runtimepolicy.DefaultToolOutputPolicy()
}

func effectiveToolOutputPolicy(policy ToolOutputPolicy, contextWindow int) ToolOutputPolicy {
	limit := contextWindow / 200
	if limit < 1 {
		limit = 1
	}
	if policy.InlineMaxBytes <= 0 {
		policy.InlineMaxBytes = limit
	} else if policy.InlineMaxBytes > limit {
		policy.InlineMaxBytes = limit
	}
	previewLimit := policy.InlineMaxBytes
	if policy.PreviewHeadBytes <= 0 {
		policy.PreviewHeadBytes = previewLimit / 2
	}
	if policy.PreviewTailBytes <= 0 {
		policy.PreviewTailBytes = previewLimit - policy.PreviewHeadBytes
	}
	if total := policy.PreviewHeadBytes + policy.PreviewTailBytes; total > previewLimit {
		policy.PreviewHeadBytes = previewLimit / 2
		policy.PreviewTailBytes = previewLimit - policy.PreviewHeadBytes
	}
	return policy
}
