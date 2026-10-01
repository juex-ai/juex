package calendar

import (
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"time"
)

func (s *State) Purge(target lifecycle.Target) {
	for _, j := range s.Jobs {
		if target.Contains(j.AgentID) && j.PauseReason != "agent_purged" {
			j.Status = "paused"
			j.PauseReason = "agent_purged"
			j.NextAt = time.Time{}
			j.Epoch++
			j.Version++
		}
	}
	for _, d := range s.Deliveries {
		if target.Contains(d.Scope.AgentID) {
			// Original occurrence identity is retained. Execution independently owns
			// stop verification after the private Runtime Worker has been removed.
			d.Purged = true
			if !d.Settled {
				d.State = "unknown"
				d.CancelRequested = true
				d.ExternalPending = true
				d.Finished = true
			}
			for _, n := range d.Notices {
				n.MainDone = true
				n.InboxDone = true
			}
		}
	}
	for id, c := range s.Commands {
		if target.Contains(c.Scope.AgentID) {
			delete(s.Commands, id)
		}
	}
}
