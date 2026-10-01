package memory

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Recall struct {
	Epoch int64  `json:"epoch"`
	Fence uint64 `json:"fence"`
	Text  string `json:"text"`
}

// Content and its validity boundary are read in the same Fleet snapshot.
func (s *Service) Recall(ctx context.Context, access application.Access, query string) (Recall, error) {
	var value Recall
	if access.AgentID == "" || len(query) > 32<<10 {
		return value, application.ErrInvalid
	}
	err := s.transact(ctx, access, false, func(state *State, _ application.Scope) error {
		if state.Strategy != mc.Advanced {
			return nil
		}
		value.Epoch, value.Fence = state.Control.Epoch, state.Fence
		terms := recallTerms(query)
		if terms == "" {
			return nil
		}
		page, err := state.Search(mc.Query{Text: terms, Limit: 8}, time.Now())
		if err != nil {
			return err
		}
		entries := []mc.Entry{}
		for _, entry := range page.Entries {
			candidate := append(entries, entry)
			encoded, err := json.Marshal(candidate)
			if err != nil {
				return err
			}
			if len(encoded) > 4096 {
				continue
			}
			entries = candidate
			value.Text = string(encoded)
		}
		return nil
	})
	return value, err
}

// Automatic recall receives sentences rather than the explicit keywords of a
// search tool. Bound the lexical expansion, including unsegmented Han text.
func recallTerms(query string) string {
	var terms []string
	bytes := 0
	seen := map[string]bool{}
	add := func(term string) {
		if len(terms) < 128 && bytes+len(term)+1 <= 4096 && utf8.RuneCountInString(term) >= 2 && len(term) <= 64 && !seen[term] {
			terms = append(terms, term)
			bytes += len(term) + 1
			seen[term] = true
		}
	}
	var word []rune
	var han rune
	for _, r := range strings.ToLower(query) {
		if unicode.Is(unicode.Han, r) {
			add(string(word))
			word = nil
			if han != 0 {
				add(string([]rune{han, r}))
			}
			han = r
		} else {
			han = 0
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				word = append(word, r)
			} else {
				add(string(word))
				word = nil
			}
		}
		if len(terms) >= 128 {
			break
		}
	}
	add(string(word))
	return strings.Join(terms, " ")
}
