package service

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func TestSharedKnowledgeFiltersAndPersistence(t *testing.T) {
	s, a, _, user := fixture(t)
	b := a
	b.AgentID, b.Scope = "agent-b", mc.Scope{Workspace: "/other", Project: "other"}
	originals := map[string]mc.Entry{}
	files := map[string][]byte{}
	for _, kind := range []string{"user", "feedback", "project", "reference"} {
		e := entry(kind)
		e.Type, e.Scope.Project = kind, "release"
		second := testSource()
		second.AgentID = "contributor"
		e.Sources = append(e.Sources, second)
		if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: kind, Action: "correct", Changes: []mc.Change{{Entry: e}}}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Read(t.Context(), user, mc.ReadRequest{ID: e.ID})
		if err != nil {
			t.Fatal(err)
		}
		originals[e.ID] = got
		data, err := os.ReadFile(filepath.Join(s.dir, "memory", e.ID+".md"))
		if err != nil {
			t.Fatal(err)
		}
		files[e.ID] = data
	}
	global := entry("global")
	global.Scope, global.Sources = mc.Scope{}, nil
	if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "global", Action: "correct", Changes: []mc.Change{{Entry: global}}}); err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []string{mc.Basic, mc.Advanced} {
		t.Run(strategy, func(t *testing.T) {
			reopened, err := Open(s.dir, a.FleetID, strategy)
			if err != nil {
				t.Fatal(err)
			}
			s = reopened
			agentPage, err := s.Search(t.Context(), b, mc.Query{})
			if err != nil {
				t.Fatal(err)
			}
			userPage, err := s.Search(t.Context(), user, mc.Query{})
			if err != nil || !reflect.DeepEqual(agentPage, userPage) || len(agentPage.Entries) != 5 {
				t.Fatalf("visibility mismatch: %+v %+v %v", agentPage, userPage, err)
			}
			for id, original := range originals {
				got, err := s.Read(t.Context(), b, mc.ReadRequest{ID: id})
				if err != nil || !reflect.DeepEqual(original, got) {
					t.Fatalf("persisted knowledge changed: %+v %v", got, err)
				}
				data, err := os.ReadFile(filepath.Join(s.dir, "memory", id+".md"))
				if err != nil || !bytes.Equal(files[id], data) {
					t.Fatalf("entry file rewritten: %s %v", id, err)
				}
			}
			for _, tc := range []struct {
				query mc.Query
				want  int
			}{
				{mc.Query{SourceAgentID: a.AgentID}, 4},
				{mc.Query{SourceAgentID: "contributor"}, 4},
				{mc.Query{Workspace: a.Scope.Workspace}, 4},
				{mc.Query{Project: "release"}, 4},
				{mc.Query{SourceAgentID: "contributor", Workspace: a.Scope.Workspace, Project: "release"}, 4},
				{mc.Query{SourceAgentID: b.AgentID}, 0},
				{mc.Query{Workspace: "/other"}, 0},
				{mc.Query{Project: "other"}, 0},
				{mc.Query{SourceAgentID: "contributor", Project: "other"}, 0},
			} {
				page, err := s.Search(t.Context(), b, tc.query)
				if err != nil || len(page.Entries) != tc.want {
					t.Fatalf("filter %+v: %+v %v", tc.query, page, err)
				}
				recall, err := s.Recall(t.Context(), b, tc.query)
				want := tc.want
				if strategy == mc.Basic {
					want = 0
				}
				if err != nil || len(recall.Entries) != want {
					t.Fatalf("recall %+v: %+v %v", tc.query, recall, err)
				}
			}
			seen := map[string]bool{}
			for offset := 0; offset >= 0; {
				page, err := s.Search(t.Context(), b, mc.Query{SourceAgentID: "contributor", Offset: offset, Limit: 1})
				if err != nil || len(page.Entries) != 1 || seen[page.Entries[0].ID] {
					t.Fatalf("filtered pagination: %+v %v", page, err)
				}
				seen[page.Entries[0].ID] = true
				offset = page.Next
			}
			if len(seen) != 4 {
				t.Fatalf("filtered pagination lost entries: %v", seen)
			}
		})
	}
}

func TestSharedChangesRetainCapabilityAndSourceValidation(t *testing.T) {
	s, a, super, user := fixture(t)
	existing := entry("existing")
	existing.Scope = mc.Scope{Workspace: "/other", Project: "other"}
	existing.Sources[0].AgentID = "agent-b"
	if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "seed", Action: "correct", Changes: []mc.Change{{Entry: existing}}}); err != nil {
		t.Fatal(err)
	}
	_, worker := proposal(t, s, a, super, "correct")
	existing.Sources = append(existing.Sources, testSource())
	decision := mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: existing, ExpectedRevision: 1}}}
	for _, caller := range []mc.Caller{a, super, func() mc.Caller { c := worker; c.Token = "wrong"; return c }(), func() mc.Caller { c := worker; c.FleetID = "other-fleet"; return c }()} {
		if _, err := s.Decide(t.Context(), caller, decision); err == nil {
			t.Fatal("invalid capability accepted")
		}
	}
	for _, mutate := range []func(*mc.Change){
		func(c *mc.Change) { c.ExpectedRevision = 0 },
		func(c *mc.Change) { c.Entry.Sources[0].AgentID = "unknown" },
		func(c *mc.Change) { c.Entry.Sources[0].Through++ },
		func(c *mc.Change) { c.Entry.Sources[0].GenerationID = "g000002" },
	} {
		invalid := clone(decision)
		mutate(&invalid.Changes[0])
		if _, err := s.Decide(t.Context(), worker, invalid); err == nil {
			t.Fatalf("invalid revision/source accepted: %+v", invalid)
		}
	}
	if _, err := s.History(t.Context(), worker, existing.Sources[0]); err == nil {
		t.Fatal("committed source expanded assignment history")
	}
	if _, err := s.Decide(t.Context(), worker, decision); err != nil {
		t.Fatalf("valid cross-source correction: %v", err)
	}
}

func TestSharedDeletionAndNoStore(t *testing.T) {
	for _, action := range []string{"delete", "no_store"} {
		t.Run(action, func(t *testing.T) {
			s, a, super, user := fixture(t)
			_, worker := proposal(t, s, a, super, "seed")
			if _, err := s.Decide(t.Context(), worker, mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: entry("shared")}}}); err != nil {
				t.Fatal(err)
			}
			s, err := Open(s.dir, a.FleetID, mc.Advanced)
			if err != nil {
				t.Fatal(err)
			}
			b := a
			b.AgentID, b.Scope = "agent-b", mc.Scope{Workspace: "/other"}
			request := mc.AdminRequest{Key: "forget", Action: action}
			if action == "delete" {
				request.EntryIDs = []string{"shared"}
			} else {
				request.Sources = []mc.Source{testSource()}
			}
			if _, err := s.Admin(t.Context(), user, request); err != nil {
				t.Fatal(err)
			}
			for _, caller := range []mc.Caller{a, b, user} {
				page, err := s.Search(t.Context(), caller, mc.Query{})
				if err != nil || len(page.Entries) != 0 {
					t.Fatalf("deleted search: %+v %v", page, err)
				}
				if _, err := s.Read(t.Context(), caller, mc.ReadRequest{ID: "shared"}); err == nil {
					t.Fatal("deleted read")
				}
				recall, err := s.Recall(t.Context(), caller, mc.Query{})
				if err != nil || len(recall.Entries) != 0 {
					t.Fatalf("deleted recall: %+v %v", recall, err)
				}
			}
			if _, err := s.Propose(t.Context(), a, mc.Proposal{Key: "relearn", Text: "remember", Sources: []mc.Source{testSource()}}); err == nil {
				t.Fatal("suppressed source relearned")
			}
		})
	}
}
