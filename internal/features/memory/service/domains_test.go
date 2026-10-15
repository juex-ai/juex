package service

import (
	"strings"
	"testing"
	"time"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func domainEntry(id, factID, domain, predicate, value string) mc.Entry {
	e := entry(id)
	e.Entities = []mc.Entity{{ID: "self", Name: "User", Kind: "person"}}
	e.Facts = []mc.Fact{{ID: factID, Domain: domain, Subject: "self", Predicate: predicate, Value: value, Status: "valid", SourceType: "user_statement", Sources: e.Sources, RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Reason: "Explicit original user statement"}}
	return e
}
func TestDomainsAndWholeStoreConflict(t *testing.T) {
	s, a, _, user := fixture(t)
	domains, err := s.Domains(t.Context(), a, mc.DomainRequest{})
	if err != nil || len(domains) != 11 {
		t.Fatalf("domains=%+v %v", domains, err)
	}
	for _, d := range domains {
		full, err := s.Domains(t.Context(), a, mc.DomainRequest{ID: d.ID})
		if err != nil || len(full) != 1 || len(full[0].Relations) == 0 {
			t.Fatalf("template %s: %+v %v", d.ID, full, err)
		}
	}
	first := domainEntry("home-a", "residence-a", "identity", "resides_in", "")
	first.Entities = append(first.Entities, mc.Entity{ID: "shanghai", Name: "Shanghai", Kind: "place"})
	first.Facts[0].Object = "shanghai"
	if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "seed", Action: "correct", Changes: []mc.Change{{Entry: first}}}); err != nil {
		t.Fatal(err)
	}
	other := clone(first)
	other.ID = "home-b"
	other.Facts[0].ID = "residence-b"
	other.Entities[1] = mc.Entity{ID: "hangzhou", Name: "Hangzhou", Kind: "place"}
	other.Facts[0].Object = "hangzhou"
	if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "conflict", Action: "correct", Changes: []mc.Change{{Entry: other}}}); err == nil || !strings.Contains(err.Error(), "competing") {
		t.Fatalf("cross-entry conflict: %v", err)
	}
	if len(s.entries) != 1 {
		t.Fatal("partial commit")
	}
	other.Facts[0].Predicate = "intends_residence"
	if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "intention", Action: "correct", Changes: []mc.Change{{Entry: other}}}); err != nil {
		t.Fatal(err)
	}
}
func TestMixedFactProjectionNeverLeaksProse(t *testing.T) {
	s, a, _, user := fixture(t)
	s.state.Strategy = mc.Advanced
	e := domainEntry("preferences", "climb", "preferences", "prefers", "climbing")
	old := clone(e.Facts[0])
	old.ID = "old"
	old.Value = "obsolete-sensitive-value"
	old.Status = "retracted"
	e.Facts = append(e.Facts, old)
	e.Name = "obsolete-sensitive-value"
	e.Summary = "obsolete-sensitive-value"
	e.Body = "obsolete-sensitive-value but also climbing"
	if _, err := s.Admin(t.Context(), user, mc.AdminRequest{Key: "seed", Action: "correct", Changes: []mc.Change{{Entry: e}}}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", "climbing", "obsolete-sensitive-value"} {
		page, err := s.Search(t.Context(), a, mc.Query{Text: text})
		if err != nil {
			t.Fatal(err)
		}
		if text == "obsolete-sensitive-value" && len(page.Entries) != 0 {
			t.Fatal("stale prose affected search")
		}
		for _, x := range page.Entries {
			if strings.Contains(x.Name+x.Summary+x.Body, "obsolete-sensitive-value") {
				t.Fatal("preview leak")
			}
		}
		recall, err := s.Recall(t.Context(), a, mc.Query{Text: text})
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range recall.Entries {
			if strings.Contains(x.Name+x.Summary+x.Body, "obsolete-sensitive-value") || len(x.Facts) != 1 {
				t.Fatal("recall leak")
			}
		}
	}
	current, err := s.Read(t.Context(), a, mc.ReadRequest{ID: e.ID})
	if err != nil || len(current.Facts) != 1 || strings.Contains(current.Body, "obsolete-sensitive-value") {
		t.Fatalf("read leak: %+v %v", current, err)
	}
	audit, err := s.Read(t.Context(), user, mc.ReadRequest{ID: e.ID})
	if err != nil || len(audit.Facts) != 2 {
		t.Fatalf("audit %+v %v", audit, err)
	}
}
