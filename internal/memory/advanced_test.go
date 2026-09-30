package memory

import (
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func TestAdvancedParticipationBoundariesAndRetry(t *testing.T) {
	now := time.Now().UTC()
	s := NewState()
	scope := application.Scope{Access: application.Access{ActorID: "user", TenantID: "tenant", UserID: "user", AgentID: "agent"}, FleetID: "fleet", ActorEpoch: 1, MemberEpoch: 1, AgentEpoch: 1, MemberVersion: 1}
	if err := s.Configure(1, true, mc.Advanced, now); err != nil {
		t.Fatal(err)
	}
	ref := mc.Source{FleetID: "fleet", AgentID: "agent", ThreadID: "thread", GenerationID: "1", From: 2, Through: 2}
	batch := Contribution{Epoch: s.Control.Epoch, Evidence: mc.Evidence{Source: ref, Kind: "user", Text: "I prefer concise replies", RecordedAt: now.Add(time.Second)}}
	if err := s.Contribute(scope, batch, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.Contribute(scope, batch, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(s.Participation["agent/thread"].Evidence) != 1 {
		t.Fatal("retry duplicated original evidence")
	}
	if _, err := s.Advance("agent/thread", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(s.Reviews) != 0 {
		t.Fatal("low volume dispatched before 24 hours")
	}
	if _, err := s.Advance("agent/thread", now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(s.Reviews) != 1 {
		t.Fatal("low volume did not become eligible")
	}
	if err := s.Configure(s.Control.Version, false, mc.Advanced, now.Add(26*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Configure(s.Control.Version, true, mc.Advanced, now.Add(27*time.Hour)); err != nil {
		t.Fatal(err)
	}
	batch.Epoch = s.Control.Epoch
	if err := s.Contribute(scope, batch, now.Add(28*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(s.Participation) != 0 {
		t.Fatal("reenable learned evidence from before the new boundary")
	}
	for _, review := range s.Reviews {
		if review.Receipt.State != "rejected" {
			t.Fatal("old review revived")
		}
	}
}

func TestTerminalEvidencePruningPreservesReceiptsAndActiveWork(t *testing.T) {
	now := time.Now()
	s := NewState()
	s.Reviews["done"] = &Review{WorkerFinished: true, Fingerprint: "identity", DecisionHash: "decision", Proposal: mc.Proposal{Key: "request", Text: "sensitive original text"}, Receipt: mc.Receipt{ID: "done", State: "applied", Committed: true, UpdatedAt: now.Add(-8 * 24 * time.Hour)}}
	s.Reviews["active"] = &Review{Proposal: mc.Proposal{Text: "still needed"}, Receipt: mc.Receipt{State: "pending", UpdatedAt: now.Add(-8 * 24 * time.Hour)}}
	s.PruneEvidence(now.Add(-7 * 24 * time.Hour))
	done := s.Reviews["done"]
	if done.Proposal.Text != "" || done.Fingerprint != "identity" || done.DecisionHash != "decision" || !done.Receipt.Committed {
		t.Fatal(done)
	}
	if s.Reviews["active"].Proposal.Text != "still needed" {
		t.Fatal("active assignment evidence pruned")
	}
}
