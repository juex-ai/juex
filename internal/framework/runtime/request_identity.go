package runtime

import "github.com/juex-ai/juex/internal/foundation/llm"

// Called after candidate preparation, which may commit a new Generation.
func (e *Engine) requestIdentityLocked() llm.RequestIdentity {
	current := e.currentThread()
	generation, scope := current.ContextIdentity()
	return llm.RequestIdentity{
		AgentID:        e.RuntimeContext.ID,
		ThreadID:       current.ID,
		GenerationID:   generation,
		ContextScopeID: scope,
	}
}
