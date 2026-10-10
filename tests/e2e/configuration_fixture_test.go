//go:build postgres

package e2e

import "github.com/juex-ai/juex/internal/foundation/agentpolicy"

func modulesForPolicy(policy *agentpolicy.Policy) map[agentpolicy.Capability]bool {
	if policy == nil {
		return nil
	}
	result := map[agentpolicy.Capability]bool{}
	for _, capability := range agentpolicy.Capabilities() {
		result[capability] = policy.Allows(capability)
	}
	return result
}
