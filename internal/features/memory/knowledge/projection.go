package knowledge

import (
	"fmt"
	"sort"
	"strings"
	"time"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func ValidateQuery(q mc.Query) error {
	if q.Offset < 0 || q.Limit < 0 || q.Limit > 50 || len(q.Text) > 4096 {
		return fmt.Errorf("memory invalid query budget")
	}
	if q.View != "" && q.View != "current" && q.View != "history" && q.View != "as_of" {
		return fmt.Errorf("memory invalid query view")
	}
	if q.View == "as_of" && q.At == nil || q.At != nil && q.View != "" && q.View != "as_of" {
		return fmt.Errorf("memory as_of requires at; at cannot combine with current/history")
	}
	if q.Domain != "" {
		if _, err := Domains(q.Domain); err != nil {
			return err
		}
	}
	switch q.Status {
	case "", "current", "overdue", "future", "expired", "superseded", "corrected", "retracted", "disputed":
	default:
		return fmt.Errorf("memory invalid lifecycle filter")
	}
	return nil
}

// Lifecycle reports effective-time applicability, not what the database knew then.
// Corrections and retractions never become true again in an as-of query.
func Lifecycle(f mc.Fact, at time.Time, asOf bool) string {
	if f.Status == "corrected" || f.Status == "retracted" || f.Status == "disputed" {
		return f.Status
	}
	if f.Status == "superseded" && !asOf {
		return "superseded"
	}
	if asOf && (f.ValidFrom == nil || (f.Status == "superseded" && f.ValidUntil == nil)) {
		return "uncertain"
	}
	if f.ValidFrom != nil && at.Before(*f.ValidFrom) {
		return "future"
	}
	if f.ValidUntil != nil && !at.Before(*f.ValidUntil) {
		if f.Status == "superseded" {
			return "superseded"
		}
		return "expired"
	}
	if f.Domain == "obligations" && f.DueAt != nil && !at.Before(*f.DueAt) {
		return "overdue"
	}
	return "current"
}
func Select(e mc.Entry, q mc.Query, now time.Time) []mc.FactView {
	if q.Workspace != "" && q.Workspace != e.Scope.Workspace || q.Project != "" && q.Project != e.Scope.Project {
		return nil
	}
	entities := map[string]mc.Entity{}
	for _, x := range e.Entities {
		entities[x.ID] = x
	}
	at := now
	if q.At != nil {
		at = *q.At
	}
	var out []mc.FactView
	for _, f := range e.Facts {
		if q.Domain != "" && q.Domain != f.Domain || q.Entity != "" && q.Entity != f.Subject && q.Entity != f.Object || q.Subject != "" && q.Subject != f.Subject || q.Predicate != "" && q.Predicate != f.Predicate {
			continue
		}
		life := Lifecycle(f, at, q.At != nil)
		if q.Status != "" && q.Status != life {
			continue
		}
		if q.View != "history" && life != "current" && life != "overdue" {
			continue
		}
		if q.SourceAgentID != "" {
			match := false
			for _, r := range f.Sources {
				if r.AgentID == q.SourceAgentID {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		v := mc.FactView{EntryID: e.ID, Revision: e.Revision, Scope: e.Scope, Fact: f, Subject: entities[f.Subject], Lifecycle: life}
		if f.Object != "" {
			obj := entities[f.Object]
			v.Object = &obj
		}
		out = append(out, v)
	}
	return out
}
func Text(v mc.FactView) string {
	value := v.Fact.Value
	if v.Object != nil {
		value = v.Object.Name + " (" + v.Object.ID + ")"
	}
	qualifiers := []string{}
	for key, value := range v.Fact.Qualifiers {
		qualifiers = append(qualifiers, key+"="+value)
	}
	sort.Strings(qualifiers)
	line := fmt.Sprintf("[%s] %s (%s) → %s → %s", v.Lifecycle, v.Subject.Name, v.Subject.ID, v.Fact.Predicate, value)
	if len(qualifiers) > 0 {
		line += "; " + strings.Join(qualifiers, ", ")
	}
	if v.Fact.TimeNote != "" {
		line += "; time: " + v.Fact.TimeNote
	}
	return line
}

// Project replaces free prose for structured entries. This is a safe read model,
// not an assertion that a machine has verified the semantics of the stored prose.
func Project(e mc.Entry, q mc.Query, now time.Time) (mc.Entry, bool) {
	if len(e.Facts) == 0 {
		return e, q.Domain == "" && q.Entity == "" && q.Subject == "" && q.Predicate == "" && q.Status == ""
	}
	views := Select(e, q, now)
	e.Name = "Structured knowledge"
	e.Summary = ""
	e.Body = ""
	e.Facts = nil
	e.Entities = nil
	e.Sources = nil
	seenEntity := map[string]bool{}
	seenSource := map[mc.Source]bool{}
	lines := []string{}
	for _, v := range views {
		lines = append(lines, Text(v))
		e.Facts = append(e.Facts, v.Fact)
		for _, x := range []mc.Entity{v.Subject, func() mc.Entity {
			if v.Object != nil {
				return *v.Object
			}
			return mc.Entity{}
		}()} {
			if x.ID != "" && !seenEntity[x.ID] {
				seenEntity[x.ID] = true
				e.Entities = append(e.Entities, x)
			}
		}
		for _, source := range v.Fact.Sources {
			if !seenSource[source] {
				seenSource[source] = true
				e.Sources = append(e.Sources, source)
			}
		}
	}
	e.Summary = strings.Join(lines, "; ")
	e.Body = strings.Join(lines, "\n")
	return e, len(views) > 0
}
