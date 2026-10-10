package memory

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func TestMemoryImportValidatesOwnershipAndDetachesSource(t *testing.T) {
	now := time.Now().UTC()
	owner := application.Scope{Access: application.Access{ActorID: "user", TenantID: "tenant", UserID: "user"}, FleetID: "fleet", ActorEpoch: 1, MemberEpoch: 1}
	scope := owner
	scope.AgentID, scope.AgentEpoch = "agent", 1
	ref := mc.Source{FleetID: "fleet", AgentID: "agent", ThreadID: "thread", GenerationID: "1", From: 1, Through: 2}
	value := FleetImport{Source: "fixture", SourceSHA256: strings.Repeat("a", 64), Control: application.Control{Enabled: true, Epoch: 1, Version: 1}, Fence: 1, Strategy: mc.Basic, AdvancedSince: now,
		Entries: []mc.Entry{{ID: "entry", Revision: 4, Name: "Name", Summary: "Summary", Type: "user", Body: "Content", Sources: []mc.Source{ref}, CreatedAt: now, UpdatedAt: now}},
		Reviews: []ImportedReview{{Scope: scope, ThreadID: "thread", Proposal: mc.Proposal{Key: "key", Text: "Proposal", Sources: []mc.Source{ref}, Evidence: []mc.Evidence{{Source: ref, Kind: "user", Text: "Original", RecordedAt: now}}}, Receipt: mc.Receipt{ID: "review", State: "failed", Attempts: 3, UpdatedAt: now}, History: ReviewHistory{SourceSHA256: strings.Repeat("b", 64)}}}}
	state, err := value.BuildState(owner)
	if err != nil {
		t.Fatal(err)
	}
	state.Entries["entry"].Sources[0].AgentID = "changed"
	state.Reviews["review"].Proposal.Evidence[0].Text = "changed"
	if value.Entries[0].Sources[0].AgentID != "agent" || value.Reviews[0].Proposal.Evidence[0].Text != "Original" {
		t.Fatal("validated import aliases caller-owned state")
	}
	for _, scenario := range []string{"foreign-owner", "pending-review", "reset-revision", "case-conflict", "foreign-evidence", "uncovered-evidence", "invalid-hash", "suppressed-review"} {
		t.Run(scenario, func(t *testing.T) {
			// Every case receives its own deep copy through the validated builder.
			copyValue := value
			copyValue.Entries = append([]mc.Entry{}, value.Entries...)
			copyValue.Reviews = append([]ImportedReview{}, value.Reviews...)
			copyValue.Reviews[0].Proposal.Evidence = append([]mc.Evidence{}, value.Reviews[0].Proposal.Evidence...)
			want := application.ErrInvalid
			switch scenario {
			case "foreign-owner":
				copyValue.Reviews[0].Scope.UserID = "foreign"
			case "pending-review":
				copyValue.Reviews[0].Receipt.State = "pending"
			case "reset-revision":
				copyValue.Entries[0].Revision = 0
			case "case-conflict":
				entry := copyValue.Entries[0]
				entry.ID = "ENTRY"
				copyValue.Entries = append(copyValue.Entries, entry)
			case "foreign-evidence":
				copyValue.Reviews[0].Proposal.Evidence[0].Source.AgentID = "foreign"
			case "uncovered-evidence":
				copyValue.Reviews[0].Proposal.Evidence[0].Source.Through = 3
			case "invalid-hash":
				copyValue.SourceSHA256 = "not-a-hash"
			case "suppressed-review":
				copyValue.Entries = nil
				copyValue.Suppressed = []mc.Source{ref}
				want = application.ErrDenied
			}
			if _, err := copyValue.BuildState(owner); !errors.Is(err, want) {
				t.Fatal("invalid import accepted", err)
			}
			if scenario == "suppressed-review" {
				copyValue.Reviews[0].Proposal = mc.Proposal{Key: "key"}
				if _, err := copyValue.BuildState(owner); err != nil {
					t.Fatal("properly scrubbed receipt rejected", err)
				}
			}
		})
	}
}

func TestImportedMemoryHistoryCannotBecomeLiveWork(t *testing.T) {
	now := time.Now().UTC()
	scope := application.Scope{Access: application.Access{ActorID: "user", TenantID: "tenant", UserID: "user", AgentID: "agent"}, FleetID: "fleet", ActorEpoch: 1, MemberEpoch: 1, AgentEpoch: 1}
	ref := mc.Source{FleetID: "fleet", AgentID: "agent", ThreadID: "thread", GenerationID: "1", From: 2, Through: 4}
	s := NewState()
	s.Reviews["old"] = &Review{ID: "old", Scope: scope, ThreadID: "thread", Epoch: 1, Fence: 1, WorkerFinished: true, DecisionHash: digest(mc.Decision{}), Imported: &ReviewHistory{SourceSHA256: "source"}, Proposal: mc.Proposal{Text: "retained review evidence"}, Receipt: mc.Receipt{ID: "old", State: "applied", UpdatedAt: now.Add(-30 * 24 * time.Hour)}}
	s.ImportedSources["agent/thread"] = HistoricalSource{Scope: scope, ThreadID: "thread", Evidence: []mc.Evidence{{Source: ref, Kind: "assistant", Text: "retained assistant evidence", RecordedAt: now}}, AcceptedThrough: 4}
	s.ParticipationExcluded["agent/thread"] = true
	s.StageNotifications()
	if s.Reviews["old"].Notification != nil {
		t.Fatal("historical receipt generated a notification")
	}
	if _, err := s.Review(scope, Binding{ReviewID: "old", Epoch: 1, Fence: 1}); !errors.Is(err, application.ErrDenied) {
		t.Fatal("historical receipt regained Worker authority", err)
	}
	if _, err := s.Decide(scope, Binding{ReviewID: "old", Epoch: 1, Fence: 1}, mc.Decision{}, now); !errors.Is(err, application.ErrDenied) {
		t.Fatal("historical decision hash became live authority", err)
	}
	s.PruneEvidence(now)
	if s.Reviews["old"].Proposal.Text == "" {
		t.Fatal("imported review erased on first background retention pass")
	}
	if err := s.Configure(1, true, mc.Advanced, now); err != nil {
		t.Fatal(err)
	}
	ref.From, ref.Through = 5, 5
	if err := s.Contribute(scope, Contribution{Epoch: s.Control.Epoch, Evidence: mc.Evidence{Source: ref, Kind: "user", Text: "new input", RecordedAt: now}}, now); err != nil {
		t.Fatal(err)
	}
	if len(s.Participation) != 0 || len(s.ImportedSources["agent/thread"].Evidence) != 1 {
		t.Fatal("configuration discarded history or revived disabled participation")
	}
	// Even a previously queued candidate cannot bypass the retained opt-out.
	s.Participation["agent/thread"] = &Participation{Scope: scope, ThreadID: "thread", Manual: true, Through: 5, Evidence: []mc.Evidence{{Source: ref, Kind: "user", Text: "queued input", RecordedAt: now}}}
	if receipt, err := s.Advance("agent/thread", now); err != nil || receipt.ID != "" {
		t.Fatal("disabled source admitted review", receipt, err)
	}
	s.scrub([]mc.Source{{FleetID: "fleet", AgentID: "agent", ThreadID: "thread", From: 1, Through: 10}})
	if len(s.ImportedSources["agent/thread"].Evidence) != 0 {
		t.Fatal("forget left imported private evidence")
	}
	s.Purge(lifecycle.Target{FleetID: "fleet", AgentIDs: []string{"agent"}})
	if len(s.ImportedSources) != 0 || len(s.ParticipationExcluded) != 0 || len(s.Reviews) != 0 {
		t.Fatal("Agent purge left private import state")
	}
}
