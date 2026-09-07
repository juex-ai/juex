package runtime

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
)

func reconcileCompactionSummary(ctx context.Context, summary string, state compactionSummaryState, maxTokens int) (string, error) {
	if err := cancellation.ContextError(ctx); err != nil {
		return "", err
	}
	var syntax compactionSummarySyntax
	for _, line := range strings.Split(summary, "\n") {
		syntax.literal(line)
	}
	if syntax.fence != 0 {
		return "", fmt.Errorf("compaction summary contains an unterminated literal block")
	}
	if len(state.Contributions) > 0 {
		headings := append([]string(nil), compactionSummaryHeadings...)
		for _, part := range state.Contributions {
			if _, exists := canonicalSummaryHeading(part.Section, headings); !exists {
				headings = append(headings, part.Section)
			}
		}
		sections := map[string]string{}
		var preamble []string
		var current string
		var syntax compactionSummarySyntax
		// Parse once, before inserting any protected multiline data. A value may
		// itself contain a heading; it must not become a new structural section.
		for _, line := range strings.Split(summary, "\n") {
			literal := syntax.literal(line)
			heading, isHeading := canonicalSummaryHeading(line, headings)
			if !literal && isHeading {
				if _, duplicate := sections[heading]; duplicate {
					return "", fmt.Errorf("compaction summary contains duplicate section %q", heading)
				}
				sections[heading], current = "", heading
			} else if current == "" {
				preamble = append(preamble, line)
			} else {
				sections[current] += line + "\n"
			}
		}
		for _, part := range state.Contributions {
			heading, _ := canonicalSummaryHeading(part.Section, headings)
			if part.Reconcile == nil {
				continue
			}
			if err := cancellation.ContextError(ctx); err != nil {
				return "", err
			}
			body, err := part.Reconcile(ctx, strings.TrimSpace(sections[heading]))
			if err != nil {
				return "", fmt.Errorf("runtime module %q compaction section: %w", part.ModuleID, err)
			}
			if err := cancellation.ContextError(ctx); err != nil {
				return "", err
			}
			if !utf8.ValidString(body) {
				return "", fmt.Errorf("runtime module %q compaction section: invalid UTF-8", part.ModuleID)
			}
			sections[heading] = body
		}
		var out []string
		if text := strings.TrimSpace(strings.Join(preamble, "\n")); text != "" {
			out = append(out, text)
		}
		for _, heading := range headings {
			out = append(out, heading)
			if body := sections[heading]; body != "" {
				out = append(out, strings.TrimRight(body, "\n"))
			}
		}
		summary = strings.Join(out, "\n")
	}
	if !utf8.ValidString(summary) || strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("compaction summary is empty or invalid UTF-8")
	}
	if maxTokens <= 0 || contextbudget.EstimateTextTokens(summary) > maxTokens {
		return "", fmt.Errorf("protected compaction summary exceeds budget: %d tokens, limit %d", contextbudget.EstimateTextTokens(summary), maxTokens)
	}
	return summary, cancellation.ContextError(ctx)
}
