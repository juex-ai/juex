package contextbudget

import "github.com/juex-ai/juex/internal/llm"

type Selection struct {
	PreviousSummary    llm.Message
	HasPreviousSummary bool
	SummaryInput       []llm.Message
	RetainedTail       []llm.Message
	FirstKeptMessageID string
	TailStartMessageID string
	LatestCompactIndex int
	RetainedTailStart  int
	SummaryInputEnd    int
}

func SelectInput(history []llm.Message, policy Policy) Selection {
	return SelectInputWithEstimator(history, policy, EstimateMessageTokens)
}

func SelectInputWithEstimator(history []llm.Message, policy Policy, estimateMessages func([]llm.Message) int) Selection {
	if estimateMessages == nil {
		estimateMessages = EstimateMessageTokens
	}
	latestCompact := -1
	for i := range history {
		if history[i].Kind == llm.MessageKindCompact {
			latestCompact = i
		}
	}
	start := 0
	sel := Selection{LatestCompactIndex: latestCompact}
	if latestCompact >= 0 {
		sel.PreviousSummary = history[latestCompact]
		sel.HasPreviousSummary = true
		start = latestCompact + 1
	}
	work := history[start:]
	if len(work) == 0 {
		sel.RetainedTailStart = len(history)
		sel.SummaryInputEnd = len(history)
		return sel
	}

	cut := chooseTailCut(work, policy, estimateMessages)
	cut = protectToolPairCut(work, cut)
	summaryEnd := start + cut
	tailStart := start + cut
	sel.SummaryInput = append([]llm.Message(nil), history[start:summaryEnd]...)
	sel.RetainedTail = append([]llm.Message(nil), history[tailStart:]...)
	sel.RetainedTailStart = tailStart
	sel.SummaryInputEnd = summaryEnd
	if len(sel.RetainedTail) > 0 {
		sel.TailStartMessageID = sel.RetainedTail[0].ID
		sel.FirstKeptMessageID = sel.RetainedTail[0].ID
	}
	return sel
}

func chooseTailCut(work []llm.Message, policy Policy, estimateMessages func([]llm.Message) int) int {
	cut := len(work)
	turns := 0
	tokens := 0
	for i := len(work) - 1; i >= 0; i-- {
		tokens += estimateMessages(work[i : i+1])
		if isUserTurnStart(work[i]) {
			turns++
		}
		if turns >= policy.TailTurns {
			cut = i
			break
		}
		if policy.KeepRecentTokens > 0 && tokens >= policy.KeepRecentTokens {
			cut = i
			break
		}
	}
	if cut == len(work) && len(work) > 0 {
		cut = len(work) - 1
	}
	return cut
}

func protectToolPairCut(work []llm.Message, cut int) int {
	if cut < 0 {
		return 0
	}
	if cut > len(work) {
		return len(work)
	}
	for cut > 0 && StartsWithToolResult(work[cut]) {
		cut--
	}
	for {
		missingID := firstToolResultWithoutUse(work[cut:])
		if missingID == "" {
			break
		}
		idx := findToolUseBefore(work[:cut], missingID)
		if idx < 0 {
			break
		}
		cut = idx
		for cut > 0 && StartsWithToolResult(work[cut]) {
			cut--
		}
	}
	return cut
}

func isUserTurnStart(m llm.Message) bool {
	if m.Role != llm.RoleUser || m.Kind == llm.MessageKindCompact || m.Kind == llm.MessageKindRuntimeContext {
		return false
	}
	for _, b := range m.Blocks {
		if b.Type != llm.BlockToolResult {
			return true
		}
	}
	return false
}

func StartsWithToolResult(m llm.Message) bool {
	return len(m.Blocks) > 0 && m.Blocks[0].Type == llm.BlockToolResult
}

func firstToolResultWithoutUse(msgs []llm.Message) string {
	seenUses := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse && b.ToolUseID != "" {
				seenUses[b.ToolUseID] = true
			}
			if b.Type == llm.BlockToolResult && b.ToolUseID != "" && !seenUses[b.ToolUseID] {
				return b.ToolUseID
			}
		}
	}
	return ""
}

func findToolUseBefore(msgs []llm.Message, id string) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, b := range msgs[i].Blocks {
			if b.Type == llm.BlockToolUse && b.ToolUseID == id {
				return i
			}
		}
	}
	return -1
}
