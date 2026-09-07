package goal

import (
	"fmt"
	"regexp"
)

var (
	goalSecretAssignmentPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|authorization|cookie|token)[A-Za-z0-9_-]*\s*[:=]\s*("[^"\n\r]*"|'[^'\n\r]*'|(bearer\s+)?[^ \n\r\t]+)`)
	goalBearerPattern           = regexp.MustCompile(`(?i)bearer\s+[^ \n\r\t]+`)
	goalOpenAIKeyPattern        = regexp.MustCompile(`sk-[A-Za-z0-9_-]{6,}`)
)

func redactGoalText(text string) string {
	text = goalSecretAssignmentPattern.ReplaceAllString(text, "[REDACTED]")
	text = goalBearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
	text = goalOpenAIKeyPattern.ReplaceAllString(text, "[REDACTED]")
	return text
}

func sanitizeGoalTextLimit(text string, maxBytes int) string {
	return redactGoalText(truncate(text, maxBytes))
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
