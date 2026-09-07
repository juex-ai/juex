package module

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/llm"
)

// ToolResultPair contains one completed call and its execution fact. Framework
// supplies only the contributing Module's pairs, isolated from durable history.
type ToolResultPair struct {
	// ID identifies this completed exchange, independently of Provider ID reuse.
	ID     string
	Use    llm.Block
	Result llm.Block
}

type ToolSummary struct {
	PairID string
	Text   string
}

type ProviderHistoryPlan struct {
	// Omit contains Framework pair IDs, never Provider tool-use IDs.
	Omit      []string
	Summaries []ToolSummary
}

type ProviderHistoryBudget struct {
	MaxBytes       int
	MaxTokens      int
	EstimateTokens func(string) int
}

// FitsSummary applies the same per-result limits as ordinary tool output.
// The Engine still accounts for all summaries in its final context budget.
func (b ProviderHistoryBudget) FitsSummary(text string) bool {
	return (b.MaxBytes > 0 || b.MaxTokens > 0) &&
		(b.MaxBytes <= 0 || len(text) <= b.MaxBytes) &&
		(b.MaxTokens <= 0 || (b.EstimateTokens != nil && b.EstimateTokens(text) <= b.MaxTokens))
}

type ProviderHistoryProjector interface {
	ProjectProviderHistory(context.Context, []ToolResultPair, ProviderHistoryBudget) (ProviderHistoryPlan, error)
}

// ProjectProviderHistory applies validated plans to request copies only. The
// caller applies final context projection and budgeting after these summaries.
func ProjectProviderHistory(ctx context.Context, history []llm.Message, budget ProviderHistoryBudget, sets ...*Set) ([]llm.Message, error) {
	ctx = nonNilContext(ctx)
	if err := cancellation.ContextError(ctx); err != nil {
		return nil, err
	}
	out := history
	for _, set := range sets {
		var err error
		out, err = set.projectProviderHistory(ctx, out, budget)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Set) projectProviderHistory(ctx context.Context, history []llm.Message, budget ProviderHistoryBudget) ([]llm.Message, error) {
	if s == nil {
		return history, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.closed {
		return nil, fmt.Errorf("runtime modules: %s set is closed", s.scope)
	}
	out := history
	for _, registered := range s.historyProjectors {
		if err := cancellation.ContextError(ctx); err != nil {
			return nil, err
		}
		pairs := ownedToolResultPairs(out, registered.id)
		// Each callback sees its own copy, including nested arguments and facts.
		input := make([]ToolResultPair, len(pairs))
		for i, pair := range pairs {
			blocks := cloneMessage(llm.Message{Blocks: []llm.Block{pair.Use, pair.Result}}).Blocks
			input[i] = ToolResultPair{ID: pair.ID, Use: blocks[0], Result: blocks[1]}
		}
		plan, err := registered.module.(ProviderHistoryProjector).ProjectProviderHistory(ctx, input, budget)
		if err != nil {
			return nil, fmt.Errorf("runtime module %q provider history: %w", registered.id, err)
		}
		if err := cancellation.ContextError(ctx); err != nil {
			return nil, err
		}
		if err := validateProviderHistoryPlan(plan, pairs, budget); err != nil {
			return nil, fmt.Errorf("runtime module %q provider history: %w", registered.id, err)
		}
		if len(plan.Omit) == 0 {
			continue
		}
		out = applyProviderHistoryPlan(out, plan, pairs)
		if err := llm.ValidateToolTranscript(out); err != nil {
			return nil, fmt.Errorf("runtime module %q provider transcript: %w", registered.id, err)
		}
	}
	return out, nil
}

type toolBlockLocation struct{ message, block int }

type indexedToolResultPair struct {
	ToolResultPair
	useAt, resultAt toolBlockLocation
}

func ownedToolResultPairs(history []llm.Message, owner ID) []indexedToolResultPair {
	type pendingUse struct {
		block llm.Block
		at    toolBlockLocation
	}
	uses := map[string][]pendingUse{}
	var pairs []indexedToolResultPair
	for messageIndex, message := range history {
		for blockIndex, block := range message.Blocks {
			if block.ToolUseID == "" {
				continue
			}
			at := toolBlockLocation{messageIndex, blockIndex}
			switch block.Type {
			case llm.BlockToolUse:
				uses[block.ToolUseID] = append(uses[block.ToolUseID], pendingUse{block: block, at: at})
			case llm.BlockToolResult:
				pending := uses[block.ToolUseID]
				if len(pending) == 0 {
					continue
				}
				use := pending[0]
				uses[block.ToolUseID] = pending[1:]
				if block.ResultFact != nil && block.ResultFact.Owner == string(owner) {
					pairs = append(pairs, indexedToolResultPair{
						ToolResultPair: ToolResultPair{ID: fmt.Sprintf("pair-%d-%d", messageIndex, blockIndex), Use: use.block, Result: block},
						useAt:          use.at, resultAt: at,
					})
				}
			}
		}
	}
	return pairs
}

func validateProviderHistoryPlan(plan ProviderHistoryPlan, pairs []indexedToolResultPair, budget ProviderHistoryBudget) error {
	owned := map[string]bool{}
	for _, pair := range pairs {
		owned[pair.ID] = true
	}
	omitted := map[string]bool{}
	for _, id := range plan.Omit {
		if !owned[id] || omitted[id] {
			return fmt.Errorf("omission %q is not a unique owned completed pair", id)
		}
		omitted[id] = true
	}
	summaries := map[string]bool{}
	for _, summary := range plan.Summaries {
		if !omitted[summary.PairID] || summaries[summary.PairID] {
			return fmt.Errorf("summary anchor %q is not a unique omitted pair", summary.PairID)
		}
		if strings.TrimSpace(summary.Text) == "" || !utf8.ValidString(summary.Text) {
			return fmt.Errorf("invalid provider history summary")
		}
		if !budget.FitsSummary(summary.Text) {
			return fmt.Errorf("provider history summary exceeds tool output budget")
		}
		summaries[summary.PairID] = true
	}
	return nil
}

func applyProviderHistoryPlan(history []llm.Message, plan ProviderHistoryPlan, pairs []indexedToolResultPair) []llm.Message {
	indexed := map[string]indexedToolResultPair{}
	for _, pair := range pairs {
		indexed[pair.ID] = pair
	}
	omitted := map[toolBlockLocation]bool{}
	for _, id := range plan.Omit {
		pair := indexed[id]
		omitted[pair.useAt], omitted[pair.resultAt] = true, true
	}
	summaries := map[toolBlockLocation]string{}
	for _, summary := range plan.Summaries {
		summaries[indexed[summary.PairID].resultAt] = summary.Text
	}
	out := cloneMessages(history)
	for i := range out {
		var blocks, deferred []llm.Block
		for j, block := range out[i].Blocks {
			at := toolBlockLocation{i, j}
			if omitted[at] {
				if summaries[at] != "" {
					deferred = append(deferred, llm.Block{Type: llm.BlockText, Text: summaries[at]})
				}
				continue
			}
			blocks = append(blocks, block)
		}
		out[i].Blocks = append(blocks, deferred...)
	}
	return out
}

func cloneMessages(history []llm.Message) []llm.Message {
	out := make([]llm.Message, len(history))
	for i, message := range history {
		out[i] = cloneMessage(message)
	}
	return out
}

// ToolOwner comes from the sealed serving catalogs, never the result payload.
func ToolOwner(name string, sets ...*Set) ID {
	for _, set := range sets {
		if set == nil {
			continue
		}
		for _, entry := range set.toolCatalog.entries {
			if entry.Tool.Name == name {
				return entry.ModuleID
			}
		}
	}
	return ""
}
