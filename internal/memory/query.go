package memory

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/memory/knowledge"
)

func preview(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "…"
}

func matches(text, query string) bool {
	if strings.TrimSpace(query) == "" {
		return true
	}
	text = strings.ToLower(text)
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func (s *State) Search(query mc.Query, now time.Time) (mc.Page, error) {
	if err := knowledge.ValidateQuery(query); err != nil {
		return mc.Page{}, invalid(err.Error())
	}
	if query.Limit == 0 {
		query.Limit = 20
	}
	entries := []mc.Entry{}
	for _, entry := range s.Entries {
		if query.Workspace != "" && entry.Scope.Workspace != query.Workspace || query.Project != "" && entry.Scope.Project != query.Project {
			continue
		}
		if query.SourceAgentID != "" {
			found := false
			for _, ref := range entry.Sources {
				if ref.AgentID == query.SourceAgentID {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		projected, visible := knowledge.Project(entry, query, now)
		if !visible || !matches(projected.Name+" "+projected.Summary+" "+projected.Body, query.Text) {
			continue
		}
		projected.Summary = preview(projected.Summary, 512)
		projected.Body = preview(projected.Body, 1024)
		projected.Sources, projected.Entities, projected.Facts = nil, nil, nil
		entries = append(entries, projected)
	}
	slices.SortFunc(entries, func(a, b mc.Entry) int {
		if n := b.UpdatedAt.Compare(a.UpdatedAt); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
	page := mc.Page{Entries: []mc.Entry{}, Next: -1, Fence: s.Fence}
	for i := query.Offset; i < len(entries) && len(page.Entries) < query.Limit; i++ {
		page.Entries = append(page.Entries, entries[i])
	}
	if query.Offset+len(page.Entries) < len(entries) {
		page.Next = query.Offset + len(page.Entries)
	}
	return page, nil
}

func (s *State) Read(request mc.ReadRequest, now time.Time) (mc.Entry, error) {
	query := mc.Query{View: request.View, At: request.At}
	if err := knowledge.ValidateQuery(query); err != nil {
		return mc.Entry{}, invalid(err.Error())
	}
	entry, found := s.Entries[request.ID]
	if !found {
		return mc.Entry{}, application.ErrDenied
	}
	projected, visible := knowledge.Project(entry, query, now)
	if !visible {
		return mc.Entry{}, application.ErrDenied
	}
	return projected, nil
}

func (s *State) Facts(query mc.Query, now time.Time) (mc.FactPage, error) {
	if err := knowledge.ValidateQuery(query); err != nil {
		return mc.FactPage{}, invalid(err.Error())
	}
	if query.Limit == 0 {
		query.Limit = 20
	}
	page := mc.FactPage{Facts: []mc.FactView{}, Next: -1, Fence: s.Fence}
	values := []mc.FactView{}
	for _, entry := range s.Entries {
		for _, fact := range entry.Facts {
			if query.Domain == "" || fact.Domain == query.Domain {
				page.DomainTotal++
			}
		}
		for _, fact := range knowledge.Select(entry, query, now) {
			if matches(knowledge.Text(fact)+" "+fact.Fact.ID+" "+fact.Fact.Domain, query.Text) {
				values = append(values, fact)
			}
		}
	}
	slices.SortFunc(values, func(a, b mc.FactView) int {
		if n := cmp.Compare(a.Fact.ID, b.Fact.ID); n != 0 {
			return n
		}
		return cmp.Compare(a.EntryID, b.EntryID)
	})
	page.Total = len(values)
	for i := query.Offset; i < len(values) && len(page.Facts) < query.Limit; i++ {
		page.Facts = append(page.Facts, values[i])
	}
	if query.Offset+len(page.Facts) < len(values) {
		page.Next = query.Offset + len(page.Facts)
	}
	return page, nil
}
