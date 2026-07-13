package workmem

import (
	"fmt"
	"regexp"
)

var (
	workmemSecretAssignmentPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|authorization|cookie|token)[A-Za-z0-9_-]*\s*[:=]\s*(bearer\s+)?[^ \n\r\t]+`)
	workmemBearerPattern           = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._-]+`)
	workmemOpenAIKeyPattern        = regexp.MustCompile(`sk-[A-Za-z0-9_-]{6,}`)
)

func redactWorkmemText(text string) string {
	text = workmemSecretAssignmentPattern.ReplaceAllString(text, "[REDACTED]")
	text = workmemBearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
	text = workmemOpenAIKeyPattern.ReplaceAllString(text, "[REDACTED]")
	return text
}

func sanitizeWorkmemText(text string, maxBytes int) string {
	return redactWorkmemText(truncate(text, maxBytes))
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	limit := 0
	for i := range s {
		if i > n {
			break
		}
		limit = i
	}
	return fmt.Sprintf("%s...(truncated, total %d bytes)", s[:limit], len(s))
}
