package contextbudget

import "github.com/juex-ai/juex/internal/llm"

type Selection struct {
	PreviousSummary    llm.Message
	HasPreviousSummary bool
	SummaryInput       []llm.Message
	RetainedTail       []llm.Message
	RetainedMessageIDs []string
	OversizedInputIDs  []string
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
	sel := Selection{LatestCompactIndex: latestCompact, RetainedTailStart: len(history), SummaryInputEnd: len(history)}
	var work []llm.Message
	if latestCompact >= 0 {
		sel.PreviousSummary = history[latestCompact]
		sel.HasPreviousSummary = true
		work = append(work, retainedMessagesForCompact(history[:latestCompact], history[latestCompact])...)
		work = append(work, history[latestCompact+1:]...)
	} else {
		work = append(work, history...)
	}
	work = compactionRelevantMessages(work)
	if len(work) == 0 {
		return sel
	}

	keep := chooseRetainedMessages(work, policy.KeepRecentTokens, estimateMessages)
	oversizedInputID := newestOversizedInputID(work, policy.KeepRecentTokens, estimateMessages)
	for _, msg := range work {
		if msg.ID == oversizedInputID {
			sel.SummaryInput = append(sel.SummaryInput, msg)
			continue
		}
		if keep[msg.ID] {
			sel.RetainedTail = append(sel.RetainedTail, msg)
			if msg.ID != "" {
				sel.RetainedMessageIDs = append(sel.RetainedMessageIDs, msg.ID)
			}
			continue
		}
		sel.SummaryInput = append(sel.SummaryInput, msg)
	}
	if oversizedInputID != "" {
		sel.OversizedInputIDs = append(sel.OversizedInputIDs, oversizedInputID)
	}
	if len(sel.RetainedTail) > 0 {
		sel.FirstKeptMessageID = sel.RetainedTail[0].ID
	} else if oversizedInputID != "" {
		sel.FirstKeptMessageID = oversizedInputID
	}
	// A single retained real input may be the entire transcript. Include it in
	// the summary request as well so manual compaction still produces a useful
	// compact marker without discarding the budgeted verbatim copy.
	if len(sel.SummaryInput) == 0 && len(sel.RetainedTail) > 0 {
		sel.SummaryInput = append([]llm.Message(nil), sel.RetainedTail...)
	}
	if start := executionTailStart(work); start >= 0 {
		sel.TailStartMessageID = work[start].ID
	}
	return sel
}

func newestOversizedInputID(work []llm.Message, budget int, estimateMessages func([]llm.Message) int) string {
	if budget <= 0 {
		return ""
	}
	for i := len(work) - 1; i >= 0; i-- {
		if !isRealInput(work[i]) {
			continue
		}
		if messageHasRetainableReference(work[i]) && estimateMessages(work[i:i+1]) > budget {
			return work[i].ID
		}
		return ""
	}
	return ""
}

func messageHasRetainableReference(msg llm.Message) bool {
	for _, block := range msg.Blocks {
		if block.Type == llm.BlockText && block.Text != "" {
			return true
		}
		if block.Type == llm.BlockImage && block.Media != nil && block.Media.ArtifactPath != "" {
			return true
		}
	}
	return false
}

func compactionRelevantMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		switch msg.Kind {
		case llm.MessageKindHookEvent, llm.MessageKindRuntimeContext, llm.MessageKindModelChange, llm.MessageKindSystemNotice, llm.MessageKindCompact:
			continue
		default:
			out = append(out, msg)
		}
	}
	return out
}

func chooseRetainedMessages(work []llm.Message, budget int, estimateMessages func([]llm.Message) int) map[string]bool {
	keep := make(map[string]bool)
	tokens := 0
	for i := len(work) - 1; i >= 0; i-- {
		if !isRealInput(work[i]) {
			continue
		}
		cost := estimateMessages(work[i : i+1])
		if budget > 0 && tokens+cost > budget {
			break
		}
		keep[work[i].ID] = true
		tokens += cost
	}
	if start := executionTailStart(work); start >= 0 {
		for i := start; i < len(work); i++ {
			keep[work[i].ID] = true
		}
	}
	return keep
}

func executionTailStart(work []llm.Message) int {
	if len(work) == 0 || !requiresExecutionTail(work[len(work)-1]) {
		return -1
	}
	for i := len(work) - 1; i >= 0; i-- {
		if isRealInput(work[i]) {
			return i
		}
	}
	for i := len(work) - 1; i >= 0; i-- {
		if work[i].Kind == llm.MessageKindContinuation {
			return i
		}
	}
	return 0
}

func requiresExecutionTail(msg llm.Message) bool {
	if msg.Kind == llm.MessageKindContinuation || msg.Kind == llm.MessageKindToolResult {
		return true
	}
	for _, block := range msg.Blocks {
		if block.Type == llm.BlockToolUse || block.Type == llm.BlockToolResult {
			return true
		}
	}
	return false
}

func isRealInput(msg llm.Message) bool {
	if msg.Role != llm.RoleUser {
		return false
	}
	switch msg.Kind {
	case llm.MessageKindDirect, llm.MessageKindMCPEvent, llm.MessageKindObservation, llm.MessageKindSideSession:
		return true
	case "":
		return llm.ClassifyUserMessage(msg).Kind == llm.MessageKindDirect
	default:
		return false
	}
}

func retainedMessagesForCompact(history []llm.Message, compact llm.Message) []llm.Message {
	if compact.Compaction == nil {
		return nil
	}
	if len(compact.Compaction.RetainedMessageIDs) > 0 {
		wanted := make(map[string]bool, len(compact.Compaction.RetainedMessageIDs))
		for _, id := range compact.Compaction.RetainedMessageIDs {
			wanted[id] = true
		}
		out := make([]llm.Message, 0, len(wanted))
		for _, msg := range history {
			if wanted[msg.ID] {
				out = append(out, msg)
			}
		}
		return out
	}
	if compact.Compaction.TailStartMessageID == "" {
		return nil
	}
	for i, msg := range history {
		if msg.ID == compact.Compaction.TailStartMessageID {
			return append([]llm.Message(nil), history[i:]...)
		}
	}
	return nil
}

func StartsWithToolResult(m llm.Message) bool {
	return len(m.Blocks) > 0 && m.Blocks[0].Type == llm.BlockToolResult
}
