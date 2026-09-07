package module

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/cancellation"
)

// CompactionContribution is a frozen snapshot for one summary operation,
// including all model retries. State is JSON data, never prompt instructions.
// Reconcile receives only the declared section body and must use the captured
// snapshot, without reading or mutating authoritative state.
type CompactionContribution struct {
	State     string
	Guidance  string
	Section   string
	Reconcile func(context.Context, string) (string, error)
}

type CompactionStateProvider interface {
	CompactionContribution(context.Context) (CompactionContribution, error)
}

// OwnedCompactionContribution gets its owner from the sealed Module set.
type OwnedCompactionContribution struct {
	ModuleID ID
	CompactionContribution
}

type CompactionBudget struct {
	MaxBytes       int
	MaxTokens      int
	EstimateTokens func(string) int
}

func (b CompactionBudget) Fits(text string) bool {
	return b.MaxBytes > 0 && b.MaxTokens > 0 && b.EstimateTokens != nil &&
		utf8.ValidString(text) && len(text) <= b.MaxBytes && b.EstimateTokens(text) <= b.MaxTokens
}

func CollectCompactionContributions(ctx context.Context, budget CompactionBudget, sets ...*Set) ([]OwnedCompactionContribution, error) {
	ctx = nonNilContext(ctx)
	var contributions []OwnedCompactionContribution
	claimed := map[string]ID{}
	var content strings.Builder
	for _, set := range sets {
		parts, err := set.compactionContributions(ctx)
		if err != nil {
			return nil, err
		}
		for _, owned := range parts {
			part := owned.CompactionContribution
			if part.State == "" && part.Guidance == "" && part.Section == "" && part.Reconcile == nil {
				continue
			}
			if !json.Valid([]byte(part.State)) || !utf8.ValidString(part.State) || !utf8.ValidString(part.Guidance) {
				return nil, fmt.Errorf("runtime module %q compaction state: invalid JSON or UTF-8", owned.ModuleID)
			}
			if !validCompactionSection(part.Section) {
				return nil, fmt.Errorf("runtime module %q compaction state: invalid section", owned.ModuleID)
			}
			key := strings.ToLower(part.Section)
			if owner, ok := claimed[key]; ok {
				return nil, fmt.Errorf("compaction section %q claimed by modules %q and %q", part.Section, owner, owned.ModuleID)
			}
			claimed[key] = owned.ModuleID
			content.WriteString(part.State)
			content.WriteString(part.Guidance)
			content.WriteString(part.Section)
			if !budget.Fits(content.String()) {
				return nil, fmt.Errorf("runtime module %q compaction contributions exceed request budget", owned.ModuleID)
			}
			contributions = append(contributions, owned)
		}
	}
	return contributions, nil
}

func (s *Set) compactionContributions(ctx context.Context) ([]OwnedCompactionContribution, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.closed {
		return nil, fmt.Errorf("runtime modules: %s set is closed", s.scope)
	}
	var parts []OwnedCompactionContribution
	// Keep lifecycle resources available while snapshotting, then hand off only
	// immutable values and pure reconciliation to the longer model operation.
	for _, entry := range s.modules {
		if err := cancellation.ContextError(ctx); err != nil {
			return nil, err
		}
		provider, ok := entry.module.(CompactionStateProvider)
		if !ok {
			continue
		}
		part, err := provider.CompactionContribution(ctx)
		if err != nil {
			return nil, fmt.Errorf("runtime module %q compaction state: %w", entry.id, err)
		}
		if err := cancellation.ContextError(ctx); err != nil {
			return nil, err
		}
		parts = append(parts, OwnedCompactionContribution{ModuleID: entry.id, CompactionContribution: part})
	}
	return parts, nil
}

func validCompactionSection(section string) bool {
	if section == "" || strings.TrimSpace(section) != section || len(section) > 80 || !utf8.ValidString(section) {
		return false
	}
	for _, r := range section {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != ' ' && r != '-' && r != '&' {
			return false
		}
	}
	return true
}
