package memory

import "github.com/juex-ai/juex/internal/foundation/lifecycle"

// Purge removes private review material, not the knowledge already shared with
// the Fleet. Source references in shared facts remain historical provenance.
func (s *State) Purge(target lifecycle.Target) {
	for key, p := range s.Participation {
		if target.Contains(p.Scope.AgentID) {
			delete(s.Participation, key)
		}
	}
	for id, r := range s.Reviews {
		if target.Contains(r.Scope.AgentID) {
			delete(s.Reviews, id)
		}
	}
	for key, id := range s.Keys {
		if s.Reviews[id] == nil {
			delete(s.Keys, key)
		}
	}
	for id, c := range s.Commands {
		if target.Contains(c.Scope.AgentID) {
			delete(s.Commands, id)
		}
	}
}
