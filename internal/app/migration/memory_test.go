package migration

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func memoryConversionFixture(t *testing.T) (application.Scope, legacy.Fleet, MemoryBindings) {
	t.Helper()
	scope, agent, rb := runtimeFixture()
	runtime, err := ConvertRuntime(scope, agent, rb)
	if err != nil {
		t.Fatal(err)
	}
	owner := application.Scope{Access: application.Access{ActorID: scope.UserID, TenantID: scope.TenantID, UserID: scope.UserID}, FleetID: scope.FleetID, ActorEpoch: 1, MemberEpoch: 1}
	private := owner
	private.AgentID, private.AgentEpoch = scope.AgentID, 1
	at := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	ref := mc.Source{FleetID: "legacy-fleet", AgentID: agent.Definition.ID, ThreadID: "0", GenerationID: "g000001", From: 2, Through: 2}
	evidence := mc.Evidence{Source: ref, Kind: "user", Text: "retained evidence", RecordedAt: at}
	caller := legacy.MemoryCaller{FleetID: ref.FleetID, AgentID: ref.AgentID, ThreadID: ref.ThreadID, Profile: "agent"}
	entry := mc.Entry{ID: "preference", Name: "Preference", Summary: "Retained preference", Body: "original knowledge", Type: "user", Revision: 7, CreatedAt: at, UpdatedAt: at, Sources: []mc.Source{ref}}
	entry.Entities = []mc.Entity{{ID: "user", Name: "User", Kind: "person"}}
	entry.Facts = []mc.Fact{{ID: "theme", Domain: "other", Subject: "user", Predicate: "open_fact", Value: "dark", Qualifiers: map[string]string{"topic": "theme"}, Status: "valid", SourceType: "user_statement", Reason: "original statement", Sources: []mc.Source{ref}, RecordedAt: at}}
	work := &legacy.MemoryWork{Caller: caller, Proposal: mc.Proposal{Key: "manual", Text: "remember preference", Sources: []mc.Source{ref}, Evidence: []mc.Evidence{evidence}}, Receipt: mc.Receipt{ID: "original-review", State: "failed", Attempts: 3, UpdatedAt: at}, Fingerprint: "original-fingerprint", DecisionHash: "original-decision", Token: "must-not-be-imported"}
	state := legacy.MemoryState{Fleet: ref.FleetID, Strategy: mc.Basic, Requests: map[string]*legacy.MemoryWork{work.Receipt.ID: work}, Keys: map[string]string{"proposal/abcdef/0/manual": work.Receipt.ID}, Deleted: map[string]bool{"forgotten": true}, Access: map[string]uint64{"preference": 9}, Uses: map[string]uint64{}}
	evidence.Kind = "assistant"
	state.Sources = map[string]*legacy.MemorySource{"abcdef/0": {Caller: caller, Epoch: "original-epoch", Enabled: false, AcceptedThrough: 4, ProcessedThrough: 1, EndedGenerations: []string{"g000001"}, Evidence: []mc.Evidence{evidence}}}
	suppressed := ref
	suppressed.GenerationID, suppressed.From, suppressed.Through = "", 3, 4
	state.Suppressed = []mc.Source{suppressed}
	fleet := legacy.Fleet{ID: ref.FleetID, Agents: []legacy.Agent{agent}, Memory: &legacy.Memory{State: state, Entries: []mc.Entry{entry}}}
	bindings := MemoryBindings{SourceSHA256: strings.Repeat("c", 64), Control: application.Control{Enabled: true, Epoch: 1, Version: 1}, AdvancedSince: at, Agents: map[string]MemoryAgentBinding{agent.Definition.ID: {Scope: private, Runtime: runtime}}}
	return owner, fleet, bindings
}

func TestMemoryConversionRetainsHistoryAndMapsClosedCommitRanges(t *testing.T) {
	owner, source, bindings := memoryConversionFixture(t)
	before, _ := json.Marshal(source)
	got, err := ConvertMemory(owner, source, bindings)
	if err != nil {
		t.Fatal(err)
	}
	s, err := got.BuildState(owner)
	if err != nil {
		t.Fatal(err)
	}
	b := bindings.Agents["abcdef"]
	span := b.Runtime.CommitSpans["0"][2]
	want := mc.Source{FleetID: owner.FleetID, AgentID: b.Scope.AgentID, ThreadID: b.Runtime.Identities.Threads["0"], GenerationID: "1", From: uint64(span.First), Through: uint64(span.Last)}
	entry := s.Entries["preference"]
	if entry.Revision != 7 || !entry.CreatedAt.Equal(source.Memory.Entries[0].CreatedAt) || entry.Body != "original knowledge" || entry.Sources[0] != want || entry.Facts[0].Sources[0] != want {
		t.Fatal("knowledge/provenance changed", entry)
	}
	r := s.Reviews["original-review"]
	if r == nil || r.Receipt.State != "failed" || r.Receipt.Attempts != 3 || r.WorkerID != "" || r.DecisionHash != "" || r.Imported.DecisionHash != "original-decision" || r.Proposal.Evidence[0].Source != want {
		t.Fatal("receipt outcome or authority changed", r)
	}
	key := b.Scope.AgentID + "/" + want.ThreadID
	history := s.ImportedSources[key]
	if history.AcceptedThrough != uint64(b.Runtime.CommitSpans["0"][4].Last) || history.ProcessedThrough != uint64(b.Runtime.CommitSpans["0"][1].Last) || history.Evidence[0].Kind != "assistant" || history.EndedGenerations[0] != "1" || !s.ParticipationExcluded[key] || len(s.Participation) != 0 {
		t.Fatal("history became active work or lost its opt-out", history)
	}
	if !s.Deleted["forgotten"] || len(s.Suppressed) != 1 || s.Suppressed[0].GenerationID != "" || s.Suppressed[0].From != uint64(b.Runtime.CommitSpans["0"][3].First) || s.Suppressed[0].Through != uint64(b.Runtime.CommitSpans["0"][4].Last) {
		t.Fatal("deletion constraints narrowed", s.Suppressed)
	}
	again, err := ConvertMemory(owner, source, bindings)
	after, _ := json.Marshal(source)
	encoded, _ := json.Marshal(got)
	if err != nil || !reflect.DeepEqual(got, again) || string(before) != string(after) || strings.Contains(string(encoded), "must-not-be-imported") {
		t.Fatal("conversion mutated source, leaked authority or is nondeterministic", err)
	}
	got.Entries[0].Facts[0].Sources[0].AgentID = "modified"
	got.Reviews[0].Proposal.Evidence[0].Text = "modified"
	after, _ = json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("conversion shares mutable source storage")
	}
}

func TestMemoryConversionRejectsUnprovenProvenance(t *testing.T) {
	for _, scenario := range []string{"missing-agent", "foreign-owner", "foreign-source-fleet", "missing-thread", "missing-generation", "wrong-generation", "missing-range", "reversed-range", "missing-span", "wrong-span", "changed-commit", "unsettled-source", "absent-memory", "unrecognized-index"} {
		t.Run(scenario, func(t *testing.T) {
			owner, source, bindings := memoryConversionFixture(t)
			b := bindings.Agents["abcdef"]
			switch scenario {
			case "missing-agent":
				delete(bindings.Agents, "abcdef")
			case "foreign-owner":
				b.Scope.FleetID = "foreign"
				bindings.Agents["abcdef"] = b
			case "foreign-source-fleet":
				source.Memory.State.Fleet = "foreign"
			case "missing-thread":
				source.Memory.Entries[0].Sources[0].ThreadID = "missing"
			case "missing-generation":
				source.Memory.Entries[0].Sources[0].GenerationID = "missing"
			case "wrong-generation":
				source.Memory.Entries[0].Sources[0].GenerationID = "g000002"
			case "missing-range":
				source.Memory.Entries[0].Sources[0].Through = 100
			case "reversed-range":
				source.Memory.Entries[0].Sources[0].From = 3
			case "missing-span":
				delete(b.Runtime.CommitSpans["0"], 2)
			case "wrong-span":
				b.Runtime.CommitSpans["0"][2] = b.Runtime.CommitSpans["0"][3]
			case "changed-commit":
				source.Agents[0].Threads[0].Commits[1].At = "2026-09-19T00:00:00Z"
			case "unsettled-source":
				source.Memory.State.Sources["abcdef/0"].Job = "pending"
			case "absent-memory":
				source.Memory = nil
			case "unrecognized-index":
				source.Memory.State.Keys["unrecognized"] = "original-review"
			}
			if _, err := ConvertMemory(owner, source, bindings); err == nil {
				t.Fatal("unproven Memory import accepted")
			}
		})
	}
}

func TestMemoryConversionRetainsRetriesAndHumanAdministration(t *testing.T) {
	for _, scenario := range []string{"manual-maintenance-retry", "human-administration", "independent-proposal-namespaces"} {
		t.Run(scenario, func(t *testing.T) {
			owner, source, bindings := memoryConversionFixture(t)
			work := source.Memory.State.Requests["original-review"]
			switch scenario {
			case "manual-maintenance-retry":
				source.Memory.State.Strategy = mc.Advanced
				source.Memory.State.Sources["abcdef/0"].Enabled = true
				work.Automatic, work.SourceKey, work.Through = true, "abcdef/0", 4
				work.Proposal.Key = "maintenance/abcdef/0/original-epoch/1/4"
				retry := *work
				retry.Receipt.ID, retry.Receipt.State = "retry-review", "no_change"
				source.Memory.State.Requests[retry.Receipt.ID] = &retry
				source.Memory.State.Keys = map[string]string{work.Proposal.Key: retry.Receipt.ID}
			case "human-administration":
				source.Memory.State.Requests["human-action"] = &legacy.MemoryWork{Caller: legacy.MemoryCaller{FleetID: source.ID, AgentID: "user", ThreadID: "0", Profile: "user"}, Proposal: mc.Proposal{Key: "delete-forgotten"}, Receipt: mc.Receipt{ID: "human-action", State: "applied", Committed: true, EntryIDs: []string{"forgotten"}, UpdatedAt: work.Receipt.UpdatedAt}, Fingerprint: "original-admin-hash"}
				source.Memory.State.Keys["admin/delete-forgotten"] = "human-action"
			case "independent-proposal-namespaces":
				source.Memory.State.Strategy = mc.Advanced
				source.Memory.State.Sources["abcdef/0"].Enabled = true
				automatic := *work
				automatic.Automatic, automatic.Receipt.ID = true, "automatic-review"
				source.Memory.State.Requests[automatic.Receipt.ID] = &automatic
				source.Memory.State.Keys[work.Proposal.Key] = automatic.Receipt.ID
			}
			got, err := ConvertMemory(owner, source, bindings)
			if err != nil {
				t.Fatal("valid source history rejected", err)
			}
			s, err := got.BuildState(owner)
			if err != nil || len(s.Reviews) != 2 {
				t.Fatal("history dropped", err)
			}
			b := bindings.Agents["abcdef"]
			switch scenario {
			case "manual-maintenance-retry":
				review := s.Reviews["retry-review"]
				if receipt, err := s.Propose(b.Scope, review.ThreadID, review.Proposal, true, time.Now()); err != nil || receipt.ID != review.ID || s.Reviews["original-review"].Receipt.State != "failed" {
					t.Fatal("old key owner replaced or prior failed receipt lost", receipt, err)
				}
			case "independent-proposal-namespaces":
				for _, id := range []string{"original-review", "automatic-review"} {
					r := s.Reviews[id]
					if receipt, err := s.Propose(b.Scope, r.ThreadID, r.Proposal, r.Automatic, time.Now()); err != nil || receipt.ID != id {
						t.Fatal("automatic and explicit proposal indexes collided", receipt, err)
					}
				}
			case "human-administration":
				r := s.Reviews["human-action"]
				if r.Scope.AgentID != "" || r.ThreadID != "" || r.WorkerID != "" || r.Imported.Fingerprint != "original-admin-hash" || len(s.Admin) != 0 {
					t.Fatal("human receipt gained Agent or current decision authority", r)
				}
				s.Purge(lifecycle.Target{FleetID: owner.FleetID, AgentIDs: []string{b.Scope.AgentID}})
				if len(s.Reviews) != 1 || s.Reviews["human-action"] == nil {
					t.Fatal("Agent purge deleted Fleet administration history")
				}
				s.Purge(lifecycle.Target{FleetID: owner.FleetID, WholeFleet: true})
				if len(s.Reviews) != 0 {
					t.Fatal("Fleet purge retained administration history")
				}
			}
		})
	}
}
