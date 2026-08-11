package runtime

import runtimepolicy "github.com/juex-ai/juex/internal/runtime/policy"

type ToolOutputPolicy = runtimepolicy.ToolOutputPolicy

func DefaultToolOutputPolicy() ToolOutputPolicy {
	return runtimepolicy.DefaultToolOutputPolicy()
}

func effectiveToolOutputPolicy(policy ToolOutputPolicy) ToolOutputPolicy {
	defaults := DefaultToolOutputPolicy()
	if policy.InlineMaxBytes <= 0 {
		policy.InlineMaxBytes = defaults.InlineMaxBytes
	}
	if policy.PreviewHeadBytes <= 0 {
		policy.PreviewHeadBytes = defaults.PreviewHeadBytes
	}
	if policy.PreviewTailBytes <= 0 {
		policy.PreviewTailBytes = defaults.PreviewTailBytes
	}
	return policy
}
