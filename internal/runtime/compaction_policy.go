package runtime

import (
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
	runtimepolicy "github.com/juex-ai/juex/internal/runtime/policy"
)

type CompactionPolicy = runtimepolicy.CompactionPolicy
type compactionPolicy = contextbudget.Policy

func DefaultCompactionPolicy() CompactionPolicy {
	return runtimepolicy.DefaultCompactionPolicy()
}

func effectiveCompactionPolicy(policy CompactionPolicy, contextWindow int) compactionPolicy {
	return contextbudget.EffectivePolicy(policy, contextWindow, DefaultContextWindowTokens)
}
