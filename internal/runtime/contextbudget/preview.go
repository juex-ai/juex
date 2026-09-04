package contextbudget

import "unicode/utf8"

// TextPreview is a UTF-8-safe head/tail projection of longer text.
type TextPreview struct {
	Head              string
	Tail              string
	OmittedBytes      int
	OmittedCharacters int
}

// TextExceedsBudget reports whether text crosses either positive ceiling.
// Non-positive ceilings are disabled.
func TextExceedsBudget(text string, maxTokens, maxBytes int) bool {
	return maxTokens > 0 && EstimateTextTokens(text) > maxTokens ||
		maxBytes > 0 && len(text) > maxBytes
}

// PreviewText retains a balanced head and tail within both positive ceilings.
// The returned omission count covers only original content; callers may add a
// fixed envelope without charging it to the content budget.
func PreviewText(text string, maxTokens, maxBytes int) TextPreview {
	if !TextExceedsBudget(text, maxTokens, maxBytes) {
		return TextPreview{Head: text}
	}
	headTokens, tailTokens := splitPreviewBudget(maxTokens)
	headBytes, tailBytes := splitPreviewBudget(maxBytes)
	return previewTextWithSideBudgets(text, maxTokens > 0, headTokens, tailTokens, maxBytes > 0, headBytes, tailBytes)
}

// PreviewTextWithByteAllocation retains head and tail content under both the
// total token ceiling and the caller's explicit per-side byte allocation.
func PreviewTextWithByteAllocation(text string, maxTokens, headBytes, tailBytes int) TextPreview {
	maxBytes := headBytes + tailBytes
	if !TextExceedsBudget(text, maxTokens, maxBytes) {
		return TextPreview{Head: text}
	}
	headTokens, tailTokens := splitPreviewTokenAllocation(maxTokens, headBytes, tailBytes)
	return previewTextWithSideBudgets(text, maxTokens > 0, headTokens, tailTokens, true, headBytes, tailBytes)
}

func previewTextWithSideBudgets(text string, tokensLimited bool, headTokens, tailTokens int, bytesLimited bool, headBytes, tailBytes int) TextPreview {
	headEnd := prefixEndWithinBudget(text, tokensLimited, headTokens, bytesLimited, headBytes)
	tailStart := headEnd + suffixStartWithinBudget(text[headEnd:], tokensLimited, tailTokens, bytesLimited, tailBytes)
	if tailStart < headEnd {
		tailStart = headEnd
	}
	preview := TextPreview{
		Head:              text[:headEnd],
		Tail:              text[tailStart:],
		OmittedBytes:      tailStart - headEnd,
		OmittedCharacters: utf8.RuneCountInString(text[headEnd:tailStart]),
	}
	if preview.OmittedBytes <= 0 {
		return TextPreview{Head: text}
	}
	return preview
}

func splitPreviewTokenAllocation(total, headBytes, tailBytes int) (head, tail int) {
	if total <= 0 {
		return 0, 0
	}
	byteTotal := headBytes + tailBytes
	if byteTotal <= 0 {
		return splitPreviewBudget(total)
	}
	head = int(float64(total) * float64(headBytes) / float64(byteTotal))
	return head, total - head
}

func splitPreviewBudget(total int) (head, tail int) {
	if total <= 0 {
		return 0, 0
	}
	head = (total + 1) / 2
	return head, total - head
}

type textTokenCounts struct {
	ascii int
	cjk   int
	other int
}

func (c textTokenCounts) withRune(r rune) textTokenCounts {
	switch {
	case r <= 0x7f:
		c.ascii++
	case isCJKRune(r):
		c.cjk++
	default:
		c.other++
	}
	return c
}

func (c textTokenCounts) tokens() int {
	return ceilDiv(c.ascii, 4) + c.cjk + ceilDiv(c.other, 3)
}

func prefixEndWithinBudget(text string, tokensLimited bool, maxTokens int, bytesLimited bool, maxBytes int) int {
	var counts textTokenCounts
	end := 0
	for index, r := range text {
		_, size := utf8.DecodeRuneInString(text[index:])
		runeEnd := index + size
		if bytesLimited && runeEnd > maxBytes {
			break
		}
		next := counts.withRune(r)
		if tokensLimited && next.tokens() > maxTokens {
			break
		}
		counts = next
		end = runeEnd
	}
	return end
}

func suffixStartWithinBudget(text string, tokensLimited bool, maxTokens int, bytesLimited bool, maxBytes int) int {
	var counts textTokenCounts
	start := len(text)
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:start])
		if size <= 0 {
			break
		}
		nextStart := start - size
		if bytesLimited && len(text)-nextStart > maxBytes {
			break
		}
		next := counts.withRune(r)
		if tokensLimited && next.tokens() > maxTokens {
			break
		}
		counts = next
		start = nextStart
	}
	return start
}
