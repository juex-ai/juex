package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func commitDomain(t *testing.T, s *Store, user mc.Caller, key string, entries ...mc.Entry) {
	t.Helper()
	var changes []mc.Change
	for _, e := range entries {
		changes = append(changes, mc.Change{Entry: e, ExpectedRevision: s.entries[e.ID].Revision})
	}
	if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: key, Action: "correct", Changes: changes}); err != nil {
		t.Fatal(err)
	}
}
func TestDomainTemplatesValidateExamples(t *testing.T) {
	s, a, _, user := fixture(t)
	domains, _ := s.Domains(t.Context(), a, mc.DomainRequest{})
	for _, d := range domains {
		templates, _ := s.Domains(t.Context(), a, mc.DomainRequest{ID: d.ID})
		for _, r := range templates[0].Relations {
			t.Run(d.ID+"/"+r.Predicate, func(t *testing.T) {
				store, _, _, admin := fixture(t)
				e := domainEntry("example", "example-fact", d.ID, r.Predicate, "supported literal")
				kind := r.Subjects[0]
				if kind == "any" {
					kind = "person"
				}
				e.Entities[0].Kind = kind
				f := &e.Facts[0]
				f.SourceType = r.SourceTypes[0]
				if r.ValueType == "number" {
					f.Value = "1250.50"
				}
				for _, q := range r.Qualifiers {
					if f.Qualifiers == nil {
						f.Qualifiers = map[string]string{}
					}
					f.Qualifiers[q] = "explicit-" + q
				}
				if len(r.Objects) > 0 {
					kind := r.Objects[0]
					if kind == "any" {
						kind = "topic"
					}
					e.Entities = append(e.Entities, mc.Entity{ID: "object", Name: "Known object", Kind: kind})
					f.Object = "object"
					f.Value = ""
				}
				if r.Positive == "" || r.Negative == "" || r.Evidence == "" || r.Temporal == "" {
					t.Fatal("incomplete template")
				}
				commitDomain(t, store, admin, "positive", e)
				bad := clone(e)
				bad.ID = "bad"
				bad.Facts[0].ID = "bad-fact"
				bad.Facts[0].Predicate = "invented_synonym"
				if _, err := store.Admin(t.Context(), admin, mc.AdminRequest{Key: "bad", Action: "correct", Changes: []mc.Change{{Entry: bad}}}); err == nil {
					t.Fatal("undeclared predicate accepted")
				}
			})
		}
	}
	_ = user
}
func TestDomainCompetitionAndIdentityCases(t *testing.T) {
	cases := []struct {
		name              string
		domain, predicate string
		first, second     string
		qualifiers        map[string]string
		mutate            func(*mc.Entry)
		wantError         string
	}{
		{name: "additive preferences", domain: "preferences", predicate: "prefers", first: "climbing", second: "photography"},
		{name: "duplicate confirmation", domain: "preferences", predicate: "prefers", first: "climbing", second: "climbing", wantError: "duplicate assertion"},
		{name: "same name distinct identities", domain: "preferences", predicate: "prefers", first: "climbing", second: "climbing", mutate: func(e *mc.Entry) { e.Entities[0].ID = "another-person"; e.Facts[0].Subject = "another-person" }},
		{name: "same entity inconsistent kind", domain: "preferences", predicate: "prefers", first: "climbing", second: "photography", mutate: func(e *mc.Entry) { e.Entities[0].Kind = "project" }, wantError: ""},
		{name: "different employments income", domain: "finance", predicate: "income", first: "1000", second: "2000", qualifiers: map[string]string{"currency": "CNY", "period": "month", "engagement": "job-a"}, mutate: func(e *mc.Entry) { e.Facts[0].Qualifiers["engagement"] = "job-b" }},
		{name: "missing currency", domain: "finance", predicate: "income", first: "1000", second: "2000", qualifiers: map[string]string{"currency": "CNY", "period": "month", "engagement": "job-a"}, mutate: func(e *mc.Entry) { delete(e.Facts[0].Qualifiers, "currency") }, wantError: "requires qualifier currency"},
		{name: "non numeric money", domain: "finance", predicate: "income", first: "1000", second: "a lot", qualifiers: map[string]string{"currency": "CNY", "period": "month", "engagement": "job-a"}, wantError: "numeric"},
		{name: "overlapping income", domain: "finance", predicate: "income", first: "1000", second: "2000", qualifiers: map[string]string{"currency": "CNY", "period": "month", "engagement": "job-a"}, wantError: "competing"},
		{name: "different projects", domain: "finance", predicate: "income", first: "1000", second: "2000", qualifiers: map[string]string{"currency": "CNY", "period": "month", "engagement": "job-a"}, mutate: func(e *mc.Entry) { e.Scope.Project = "B" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, user := fixture(t)
			a := domainEntry("one", "f-one", tc.domain, tc.predicate, tc.first)
			a.Facts[0].Qualifiers = tc.qualifiers
			commitDomain(t, s, user, "one", a)
			b := clone(a)
			b.ID = "two"
			b.Facts[0].ID = "f-two"
			b.Facts[0].Value = tc.second
			if tc.mutate != nil {
				tc.mutate(&b)
			}
			_, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "two", Action: "correct", Changes: []mc.Change{{Entry: b}}})
			if tc.name == "same entity inconsistent kind" {
				if err == nil {
					t.Fatal("inconsistent entity accepted")
				}
				return
			}
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("got %v; want %q", err, tc.wantError)
			}
		})
	}
}
func TestDomainLifecycleAsOfAndAtomicSupersession(t *testing.T) {
	s, a, _, user := fixture(t)
	old := domainEntry("residence", "residence-before", "identity", "resides_in", "")
	old.Entities = append(old.Entities, mc.Entity{ID: "shanghai", Name: "Shanghai", Kind: "place"})
	old.Facts[0].Object = "shanghai"
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	move := start.AddDate(1, 0, 0)
	old.Facts[0].ValidFrom = &start
	commitDomain(t, s, user, "seed", old)
	newer := clone(old)
	newer.ID = "new-residence"
	newer.Facts[0].ID = "residence-after"
	newer.Entities[1] = mc.Entity{ID: "hangzhou", Name: "Hangzhou", Kind: "place"}
	newer.Facts[0].Object = "hangzhou"
	newer.Facts[0].ValidFrom = &move
	newer.Facts[0].Replaces = []string{"residence-before"}
	old.Facts[0].Status = "superseded"
	old.Facts[0].ValidUntil = &move
	old.Facts[0].Reason = "Explicitly moved at the start of 2026"
	// A stale old-entry revision cannot publish half of the move.
	_, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "stale", Action: "correct", Changes: []mc.Change{{Entry: newer}, {Entry: old, ExpectedRevision: 0}}})
	if err == nil || !strings.Contains(err.Error(), "revision conflict") || len(s.entries) != 1 {
		t.Fatalf("atomicity: %v", err)
	}
	commitDomain(t, s, user, "move", old, newer)
	then := move.Add(-time.Second)
	for _, tc := range []struct {
		q    mc.Query
		want string
	}{{mc.Query{}, "residence-after"}, {mc.Query{View: "as_of", At: &then}, "residence-before"}, {mc.Query{View: "as_of", At: &move}, "residence-after"}} {
		page, err := s.Facts(t.Context(), a, tc.q)
		if err != nil || len(page.Facts) != 1 || page.Facts[0].Fact.ID != tc.want {
			t.Fatalf("query %+v: %+v %v", tc.q, page, err)
		}
	}
	hist, err := s.Facts(t.Context(), a, mc.Query{View: "history", Status: "superseded"})
	if err != nil || len(hist.Facts) != 1 || hist.Facts[0].Fact.ID != "residence-before" {
		t.Fatalf("history %+v %v", hist, err)
	}
	// A mistaken earlier assertion remains visible in audit, never as effective truth.
	newer.Facts[0].Status = "corrected"
	newer.Facts[0].Reason = "User corrected the earlier statement"
	commitDomain(t, s, user, "correction", newer)
	page, _ := s.Facts(t.Context(), a, mc.Query{View: "as_of", At: &move})
	if len(page.Facts) != 0 {
		t.Fatal("corrected assertion returned as truth")
	}
}
func TestDomainExpiryAndOverdueAreDistinct(t *testing.T) {
	s, a, _, user := fixture(t)
	past := s.now().Add(-time.Hour)
	temporary := domainEntry("trip", "trip-fact", "temporary", "temporarily_at", "")
	temporary.Entities = append(temporary.Entities, mc.Entity{ID: "beijing", Name: "Beijing", Kind: "place"})
	temporary.Facts[0].Object = "beijing"
	temporary.Facts[0].ValidUntil = &past
	debt := domainEntry("promise", "promise-fact", "obligations", "committed_to", "")
	debt.Entities = append(debt.Entities, mc.Entity{ID: "send-report", Name: "Send the report", Kind: "obligation"})
	debt.Facts[0].Object = "send-report"
	debt.Facts[0].DueAt = &past
	commitDomain(t, s, user, "seed", temporary, debt)
	page, err := s.Facts(t.Context(), a, mc.Query{})
	if err != nil || len(page.Facts) != 1 || page.Facts[0].Lifecycle != "overdue" || page.Facts[0].Fact.Status != "valid" {
		t.Fatalf("overdue %+v %v", page, err)
	}
	page, _ = s.Facts(t.Context(), a, mc.Query{View: "history", Status: "expired"})
	if len(page.Facts) != 1 || page.Facts[0].Fact.ID != "trip-fact" {
		t.Fatal("expired trip missing")
	}
}
func TestDomainConfirmationRequiresNewUserEvidenceAndRetainsHistory(t *testing.T) {
	for _, kind := range []string{"assistant", "user"} {
		t.Run(kind, func(t *testing.T) {
			s, a, super, user := fixture(t)
			e := domainEntry("preference", "climbing", "preferences", "prefers", "climbing")
			commitDomain(t, s, user, "seed", e)
			source := testSource()
			source.From = 3
			source.Through = 3
			proposal := mc.Proposal{Key: "confirmation", Text: "I still like climbing", Reason: "Remember", Sources: []mc.Source{source}, Evidence: []mc.Evidence{{Source: source, Kind: kind, Text: "I still like climbing", RecordedAt: s.now()}}}
			receipt, err := s.Propose(t.Context(), a, proposal)
			if err != nil {
				t.Fatal(err)
			}
			job, err := s.Claim(t.Context(), super)
			if err != nil || job == nil {
				t.Fatal(err)
			}
			super.AssignmentID = job.ID
			super.Token = job.Token
			current := clone(s.entries[e.ID])
			current.Sources = append(current.Sources, source)
			current.Facts[0].Sources = append(current.Facts[0].Sources, source)
			decision := mc.Decision{Outcome: "applied", Reason: "Repeated original confirmation", Changes: []mc.Change{{Entry: current, ExpectedRevision: 1}}}
			result, err := s.Decide(t.Context(), super, decision)
			if kind == "assistant" {
				if err == nil || !strings.Contains(err.Error(), "original user evidence") {
					t.Fatalf("assistant confirmation: %v", err)
				}
				state, _ := s.Result(t.Context(), a, receipt.ID)
				if state.Committed || state.State != "running" {
					t.Fatal("invalid decision settled")
				}
				return
			}
			if err != nil || !result.Committed {
				t.Fatalf("confirmation %+v %v", result, err)
			}
			facts, _ := s.Facts(t.Context(), a, mc.Query{})
			if len(facts.Facts) != 1 || len(facts.Facts[0].Fact.Sources) != 2 || !facts.Facts[0].Fact.RecordedAt.Equal(e.Facts[0].RecordedAt) {
				t.Fatal("confirmation duplicated or refreshed fact")
			}
		})
	}
}
func TestDomainPagedEntityQueryAndForget(t *testing.T) {
	s, a, _, user := fixture(t)
	for i := 0; i < 3; i++ {
		e := domainEntry(fmt.Sprintf("entry-%d", i), fmt.Sprintf("f-%d", i), "preferences", "prefers", fmt.Sprintf("hobby %d", i))
		commitDomain(t, s, user, fmt.Sprint(i), e)
	}
	page, _ := s.Facts(t.Context(), a, mc.Query{Entity: "self", Limit: 2})
	if page.Total != 3 || len(page.Facts) != 2 || page.Next != 2 {
		t.Fatalf("page %+v", page)
	}
	next, _ := s.Facts(t.Context(), a, mc.Query{Entity: "self", Offset: page.Next, Limit: 2})
	if next.Next != -1 || len(next.Facts) != 1 || next.Facts[0].Fact.ID == page.Facts[0].Fact.ID {
		t.Fatal("unstable pagination")
	}
	if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "forget", Action: "no_store", Sources: []mc.Source{testSource()}}); err != nil {
		t.Fatal(err)
	}
	page, _ = s.Facts(t.Context(), a, mc.Query{View: "history"})
	if page.Total != 0 || len(page.Facts) != 0 {
		t.Fatal("forgotten knowledge visible")
	}
}

func TestDomainUnknownTransitionAndDisputeDoNotInventTruth(t *testing.T) {
	s, a, _, user := fixture(t)
	old := domainEntry("home", "old-home", "identity", "resides_in", "")
	old.Entities = append(old.Entities, mc.Entity{ID: "old-city", Name: "Old city", Kind: "place"})
	old.Facts[0].Object = "old-city"
	commitDomain(t, s, user, "seed", old)
	old.Facts[0].Status = "superseded"
	old.Facts[0].TimeNote = "Previously lived here; date of move unknown."
	next := clone(old)
	next.ID = "new-home"
	next.Facts[0].ID = "new-home"
	next.Facts[0].Status = "valid"
	next.Facts[0].Replaces = []string{"old-home"}
	next.Facts[0].TimeNote = "Now lives here; date of move unknown."
	next.Entities[1] = mc.Entity{ID: "new-city", Name: "New city", Kind: "place"}
	next.Facts[0].Object = "new-city"
	commitDomain(t, s, user, "unknown-move", old, next)
	historicalTime := s.now().AddDate(-1, 0, 0)
	page, err := s.Facts(t.Context(), a, mc.Query{View: "as_of", At: &historicalTime})
	if err != nil || len(page.Facts) != 0 {
		t.Fatalf("invented historical interval: %+v %v", page, err)
	}
	current, _ := s.Facts(t.Context(), a, mc.Query{})
	if len(current.Facts) != 1 || current.Facts[0].Fact.ID != "new-home" {
		t.Fatal("current fact lost")
	}
	conflict := clone(next)
	conflict.ID = "uncertain-home"
	conflict.Facts[0].ID = "uncertain-home"
	conflict.Facts[0].Replaces = nil
	conflict.Facts[0].Object = "other-city"
	conflict.Entities[1] = mc.Entity{ID: "other-city", Name: "Other city", Kind: "place"}
	conflict.Facts[0].Status = "disputed"
	if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "only-one-disputed", Action: "correct", Changes: []mc.Change{{Entry: conflict}}}); err == nil {
		t.Fatal("disputed claim silently lost against a certain value")
	}
	next.Facts[0].Status = "disputed"
	commitDomain(t, s, user, "both-disputed", next, conflict)
	current, _ = s.Facts(t.Context(), a, mc.Query{})
	if len(current.Facts) != 0 {
		t.Fatal("disputed claim chosen as current truth")
	}
	audit, _ := s.Facts(t.Context(), a, mc.Query{View: "history", Status: "disputed"})
	if len(audit.Facts) != 2 {
		t.Fatal("contradictory evidence lost")
	}
}

func TestDomainHistoricalSingleValueIntervalsCannotOverlap(t *testing.T) {
	s, a, _, user := fixture(t)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	old := domainEntry("past", "past-residence", "identity", "resides_in", "")
	old.Entities = append(old.Entities, mc.Entity{ID: "shanghai", Name: "Shanghai", Kind: "place"})
	old.Facts[0].Object = "shanghai"
	old.Facts[0].Status = "superseded"
	old.Facts[0].ValidFrom, old.Facts[0].ValidUntil = &start, &end
	commitDomain(t, s, user, "past", old)
	next := clone(old)
	next.ID, next.Facts[0].ID = "present", "present-residence"
	next.Entities[1] = mc.Entity{ID: "hangzhou", Name: "Hangzhou", Kind: "place"}
	next.Facts[0].Object, next.Facts[0].Status = "hangzhou", "valid"
	next.Facts[0].ValidUntil = nil
	// Omitting replaces must not bypass the historical competition constraint.
	if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "overlap", Action: "correct", Changes: []mc.Change{{Entry: next}}}); err == nil {
		t.Fatal("overlapping historical single-value assertions accepted")
	}
	next.Facts[0].ValidFrom = &end
	commitDomain(t, s, user, "adjacent", next)
	page, err := s.Facts(t.Context(), a, mc.Query{View: "as_of", At: &end})
	if err != nil || len(page.Facts) != 1 || page.Facts[0].Fact.ID != "present-residence" {
		t.Fatalf("boundary: %+v %v", page, err)
	}
}

func TestDomainSharedReplacementHistoryAndCycles(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(fmt.Sprintf("cycle=%v", cycle), func(t *testing.T) {
			s, a, _, user := fixture(t)
			e := domainEntry("history", "first", "preferences", "prefers", "mistaken preference")
			original := e.Facts[0]
			e.Facts = nil
			for i := 0; i < 40; i++ {
				f := clone(original)
				f.ID = fmt.Sprintf("claim-%02d", i)
				f.Status = "corrected"
				if i > 0 {
					f.Replaces = append(f.Replaces, fmt.Sprintf("claim-%02d", i-1))
				}
				if i > 1 {
					f.Replaces = append(f.Replaces, fmt.Sprintf("claim-%02d", i-2))
				}
				e.Facts = append(e.Facts, f)
			}
			if cycle {
				e.Facts[0].Replaces = []string{"claim-39"}
			}
			_, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "history", Action: "correct", Changes: []mc.Change{{Entry: e}}})
			if cycle {
				if err == nil || !strings.Contains(err.Error(), "cycle") || len(s.entries) != 0 {
					t.Fatalf("cyclic commit: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			page, err := s.Facts(t.Context(), a, mc.Query{View: "history", Limit: 50})
			if err != nil || len(page.Facts) != 40 {
				t.Fatalf("shared ancestry lost: %+v %v", page, err)
			}
		})
	}
}
