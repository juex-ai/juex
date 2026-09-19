package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func fixture(t *testing.T) (*Store, mc.Caller, mc.Caller, mc.Caller) {
	t.Helper()
	s, err := Open(t.TempDir(), "fleet-a", mc.Basic)
	if err != nil {
		t.Fatal(err)
	}
	a := mc.Caller{FleetID: "fleet-a", AgentID: "agent-a", ThreadID: "0", Profile: mc.ProfileAgent, Scope: mc.Scope{Workspace: "/project"}}
	super := mc.Caller{FleetID: "fleet-a", AgentID: "supervisor", ThreadID: "0", Profile: mc.ProfileSupervisor}
	user := super
	user.Profile = mc.ProfileUser
	return s, a, super, user
}

func proposal(t *testing.T, s *Store, a, super mc.Caller, key string) (mc.Receipt, mc.Caller) {
	t.Helper()
	r, err := s.Propose(context.Background(), a, mc.Proposal{Key: key, Text: "Keep release instructions", Reason: "explicit user request", Sources: []mc.Source{testSource()}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(context.Background(), super)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	super.AssignmentID, super.Token, super.Purpose = job.ID, job.Token, "maintenance"
	return r, super
}

func testSource() mc.Source {
	return mc.Source{FleetID: "fleet-a", AgentID: "agent-a", ThreadID: "0", GenerationID: "g000001", From: 1, Through: 2}
}

func entry(id string) mc.Entry {
	return mc.Entry{ID: id, Name: id, Summary: "release convention", Type: "project", Body: "Use the release checklist", Sources: []mc.Source{testSource()}, Scope: mc.Scope{Workspace: "/project"}}
}

func TestProposalCommitReplayAndScope(t *testing.T) {
	s, a, super, _ := fixture(t)
	ctx := context.Background()
	r, worker := proposal(t, s, a, super, "one")
	decision := mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: entry("release")}}}
	if _, err := s.Decide(ctx, a, decision); err == nil {
		t.Fatal("ordinary caller committed")
	}
	got, err := s.Decide(ctx, worker, decision)
	if err != nil || !got.Committed || got.ID != r.ID {
		t.Fatalf("commit %+v %v", got, err)
	}
	again, err := s.Decide(ctx, worker, decision)
	if err != nil || again.ID != got.ID {
		t.Fatalf("replay %+v %v", again, err)
	}
	other := a
	other.AgentID = "agent-b"
	read, err := s.Read(ctx, other, mc.ReadRequest{ID: "release"})
	if err != nil || read.Revision != 1 {
		t.Fatalf("shared read %+v %v", read, err)
	}
	other.Scope.Workspace = "/unrelated"
	if _, err = s.Read(ctx, other, mc.ReadRequest{ID: "release"}); err == nil {
		t.Fatal("scope leak")
	}
	other = a
	other.FleetID = "fleet-b"
	if _, err = s.Search(ctx, other, mc.Query{}); err == nil {
		t.Fatal("Fleet leak")
	}
	if _, err = s.Propose(ctx, a, mc.Proposal{Key: "one", Text: "different", Reason: "explicit user request", Sources: []mc.Source{testSource()}}); err == nil {
		t.Fatal("idempotency conflict accepted")
	}
}

func TestDecisionEntryIDValidationCanBeCorrectedInSameAssignment(t *testing.T) {
	s, a, super, _ := fixture(t)
	r, worker := proposal(t, s, a, super, "birthday:example")
	for _, id := range []string{"birthday:example", "../entry", "", "-entry", strings.Repeat("a", 65)} {
		decision := mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: entry(id)}}}
		if _, err := s.Decide(t.Context(), worker, decision); err == nil || !strings.Contains(err.Error(), "memory entry ID") || !strings.Contains(err.Error(), "1-64") {
			t.Errorf("invalid ID %q must explain the Memory contract: %v", id, err)
		}
		got, err := s.Result(t.Context(), a, r.ID)
		if err != nil || got.Committed || got.State != "running" || got.Attempts != 1 {
			t.Fatalf("validation settled assignment: %+v %v", got, err)
		}
	}
	decision := mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: entry("birthday-example")}}}
	got, err := s.Decide(t.Context(), worker, decision)
	if err != nil || !got.Committed || got.ID != r.ID || got.Attempts != 1 {
		t.Fatalf("corrected assignment: %+v %v", got, err)
	}
	if e, err := s.Read(t.Context(), a, mc.ReadRequest{ID: "birthday-example"}); err != nil || e.Revision != 1 {
		t.Fatalf("corrected knowledge unavailable: %+v %v", e, err)
	}
}

func TestAssignmentCannotBroadenKnowledgeScope(t *testing.T) {
	for _, scope := range []mc.Scope{{Workspace: "/project"}, {Project: "project"}, {Workspace: "/project", Project: "project"}} {
		t.Run(fmt.Sprint(scope), func(t *testing.T) {
			s, a, super, user := fixture(t)
			a.Scope = scope
			ctx := context.Background()
			global := entry("global")
			global.Scope = mc.Scope{}
			if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: "global", Action: "correct", Changes: []mc.Change{{Entry: global}}}); err != nil {
				t.Fatal(err)
			}
			_, worker := proposal(t, s, a, super, "scoped")
			broader := entry("broader")
			broader.Scope = mc.Scope{}
			for _, change := range []mc.Change{
				{Entry: broader},
				{Entry: global, ExpectedRevision: 1},
				{Entry: mc.Entry{ID: global.ID}, ExpectedRevision: 1, Delete: true},
			} {
				if _, err := s.Decide(ctx, worker, mc.Decision{Outcome: "applied", Changes: []mc.Change{change}}); err == nil {
					t.Fatalf("assignment changed broader knowledge: %+v", change)
				}
			}
			local := entry("local")
			local.Scope = scope
			if _, err := s.Decide(ctx, worker, mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: local}}}); err != nil {
				t.Fatalf("same-scope commit: %v", err)
			}
			other := a
			other.Scope = mc.Scope{Workspace: "/unrelated", Project: "unrelated"}
			if _, err := s.Read(ctx, other, mc.ReadRequest{ID: local.ID}); err == nil {
				t.Fatal("unrelated scope read committed knowledge")
			}
		})
	}
}

func TestHotIndexAndExplicitAccessOnly(t *testing.T) {
	s, a, super, user := fixture(t)
	ctx := context.Background()
	for start := 0; start < 201; start += 20 {
		var changes []mc.Change
		for i := start; i < start+20 && i < 201; i++ {
			changes = append(changes, mc.Change{Entry: entry(fmt.Sprintf("entry-%03d", i))})
		}
		if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: fmt.Sprint(start), Action: "correct", Changes: changes}); err != nil {
			t.Fatal(err)
		}
	}
	index, err := os.ReadFile(filepath.Join(s.dir, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if contains(index, "entry-000") || !contains(index, "entry-200") {
		t.Fatal("incorrect eviction")
	}
	page, err := s.Search(ctx, a, mc.Query{Text: "entry-000"})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Body != "" {
		t.Fatalf("cold preview %+v %v", page, err)
	}
	super.Purpose = "maintenance"
	super.Scope = a.Scope
	if _, err := s.Read(ctx, super, mc.ReadRequest{ID: "entry-000"}); err != nil {
		t.Fatal(err)
	}
	index, _ = os.ReadFile(filepath.Join(s.dir, "MEMORY.md"))
	if contains(index, "entry-000") {
		t.Fatal("maintenance heated index")
	}
	if _, err := s.Read(ctx, a, mc.ReadRequest{ID: "entry-000"}); err != nil {
		t.Fatal(err)
	}
	index, _ = os.ReadFile(filepath.Join(s.dir, "MEMORY.md"))
	if !contains(index, "entry-000") {
		t.Fatal("explicit read did not reenter")
	}
}

func contains(data []byte, term string) bool { return strings.Contains(string(data), term) }

func TestRecoverIntentBeforeReadersAndDegradedIndex(t *testing.T) {
	s, a, super, _ := fixture(t)
	ctx := context.Background()
	_, worker := proposal(t, s, a, super, "merge")
	fail := true
	s.beforeWrite = func(path string) error {
		if fail && filepath.Base(path) == "second.md" {
			return errors.New("disk failure")
		}
		return nil
	}
	_, err := s.Decide(ctx, worker, mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: entry("first")}, {Entry: entry("second")}}})
	if err == nil {
		t.Fatal("expected publication failure")
	}
	if _, err = s.Search(ctx, a, mc.Query{}); err == nil {
		t.Fatal("reader observed partial commit")
	}
	fail = false
	page, err := s.Search(ctx, a, mc.Query{})
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("recovered view %+v %v", page, err)
	}
	reopened, err := Open(s.dir, "fleet-a", mc.Basic)
	if err != nil {
		t.Fatal(err)
	}
	r, err := reopened.Result(ctx, a, worker.AssignmentID)
	if err != nil || !r.Committed {
		t.Fatalf("receipt recovery %+v %v", r, err)
	}
	_, worker = proposal(t, reopened, a, super, "index")
	reopened.beforeWrite = func(path string) error {
		if filepath.Base(path) == "MEMORY.md" {
			return errors.New("index failure")
		}
		return nil
	}
	r, err = reopened.Decide(ctx, worker, mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: entry("third")}}})
	if err != nil || !r.Committed || r.IndexReady {
		t.Fatalf("commit/index distinction %+v %v", r, err)
	}
	page, err = reopened.Search(ctx, a, mc.Query{Text: "third"})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("degraded cold search %+v %v", page, err)
	}
}

func TestDeleteFencesAssignmentsAndScrubsOwnedContent(t *testing.T) {
	s, a, super, user := fixture(t)
	ctx := context.Background()
	source := mc.Source{FleetID: a.FleetID, AgentID: a.AgentID, ThreadID: a.ThreadID, GenerationID: "g000001", From: 1, Through: 2}
	secret := entry("old")
	secret.Body = "private forgotten detail"
	secret.Sources = []mc.Source{source}
	if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: "seed", Action: "correct", Changes: []mc.Change{{Entry: secret}}}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Propose(ctx, a, mc.Proposal{Key: "stale", Text: secret.Body, Reason: "remember", Sources: []mc.Source{source}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, super)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	worker := super
	worker.AssignmentID = job.ID
	worker.Token = job.Token
	if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: "forget", Action: "delete", EntryIDs: []string{"old"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(ctx, worker, mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: secret}}}); err == nil {
		t.Fatal("stale resurrection")
	}
	if _, err := s.Propose(ctx, a, mc.Proposal{Key: "retry", Text: secret.Body, Reason: "remember", Sources: []mc.Source{source}}); err == nil {
		t.Fatal("suppressed source accepted")
	}
	got, err := s.Result(ctx, a, r.ID)
	if err != nil || got.State != "rejected" {
		t.Fatalf("settled request %+v %v", got, err)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "state", "state.json"))
	if err != nil || contains(data, secret.Body) {
		t.Fatalf("content retained: %v", err)
	}
}

func TestExpiredAttemptCannotCommit(t *testing.T) {
	s, a, super, _ := fixture(t)
	_, worker := proposal(t, s, a, super, "expiry")
	s.now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	if _, err := s.Decide(context.Background(), worker, mc.Decision{Outcome: "no_change"}); err == nil {
		t.Fatal("expired assignment committed")
	}
}

func TestNoStoreRemovesAffectedKnowledgeAndRecall(t *testing.T) {
	s, a, _, user := fixture(t)
	ctx := context.Background()
	s, err := Open(s.dir, a.FleetID, mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	secret := entry("secret")
	secret.Body = "private durable preference"
	other := entry("unrelated")
	other.Sources = []mc.Source{{FleetID: a.FleetID, AgentID: a.AgentID, ThreadID: "0", GenerationID: "g000002", From: 20, Through: 21}}
	if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: "seed", Action: "correct", Changes: []mc.Change{{Entry: secret}, {Entry: other}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: "exclude", Action: "no_store", Sources: secret.Sources}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, a, mc.ReadRequest{ID: secret.ID}); err == nil {
		t.Fatal("excluded knowledge remained readable")
	}
	if _, err := s.Read(ctx, a, mc.ReadRequest{ID: other.ID}); err != nil {
		t.Fatalf("unrelated knowledge removed: %v", err)
	}
	recalled, err := s.Recall(ctx, a, mc.Query{Text: "private durable"})
	if err != nil || len(recalled.Entries) != 0 {
		t.Fatalf("excluded recall %+v %v", recalled, err)
	}
}

func TestMergeRetainsEvidenceWithoutCreatingForgetConstraint(t *testing.T) {
	s, a, super, user := fixture(t)
	ctx := context.Background()
	old := entry("old")
	if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: "seed", Action: "correct", Changes: []mc.Change{{Entry: old}}}); err != nil {
		t.Fatal(err)
	}
	_, worker := proposal(t, s, a, super, "merge")
	if _, err := s.Decide(ctx, worker, mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: entry("merged")}, {Entry: mc.Entry{ID: "old"}, ExpectedRevision: 1, Delete: true}}}); err != nil {
		t.Fatal(err)
	}
	_, worker = proposal(t, s, a, super, "refine")
	refined := entry("merged")
	refined.Body = "refined release checklist"
	if _, err := s.Decide(ctx, worker, mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: refined, ExpectedRevision: 1}}}); err != nil {
		t.Fatalf("merge incorrectly suppressed evidence: %v", err)
	}
}

func TestNoStoreMixedEntryPreservesUnselectedSourcesAfterReopen(t *testing.T) {
	s, a, _, user := fixture(t)
	ctx := t.Context()
	selected := testSource()
	other := selected
	other.GenerationID, other.From, other.Through = "g000002", 20, 21
	mixed := entry("mixed")
	mixed.Sources = []mc.Source{selected, other}
	if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: "mixed", Action: "correct", Changes: []mc.Change{{Entry: mixed}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: "exclude", Action: "no_store", Sources: []mc.Source{selected}}); err != nil {
		t.Fatal(err)
	}
	s, err := Open(s.dir, a.FleetID, mc.Basic)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, a, mc.ReadRequest{ID: mixed.ID}); err == nil {
		t.Fatal("mixed entry survived no-store")
	}
	if _, err := s.Propose(ctx, a, mc.Proposal{Key: "selected", Text: "excluded", Reason: "test", Sources: []mc.Source{selected}}); err == nil {
		t.Fatal("selected source was not suppressed")
	}
	if _, err := s.Propose(ctx, a, mc.Proposal{Key: "other", Text: "permitted", Reason: "test", Sources: []mc.Source{other}}); err != nil {
		t.Fatalf("unselected source was suppressed: %v", err)
	}
}
