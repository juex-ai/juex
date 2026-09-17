package tasks

import (
	"fmt"
	"regexp"
)

var (
	tasksSecretAssignmentPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|authorization|cookie|token)[A-Za-z0-9_-]*\s*[:=]\s*("[^"\n\r]*"|'[^'\n\r]*'|(bearer\s+)?[^ \n\r\t]+)`)
	tasksBearerPattern           = regexp.MustCompile(`(?i)bearer\s+[^ \n\r\t]+`)
	tasksOpenAIKeyPattern        = regexp.MustCompile(`sk-[A-Za-z0-9_-]{6,}`)
)

func redactTaskText(text string) string {
	text = tasksSecretAssignmentPattern.ReplaceAllString(text, "[REDACTED]")
	text = tasksBearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
	text = tasksOpenAIKeyPattern.ReplaceAllString(text, "[REDACTED]")
	return text
}

func sanitizeTaskTextLimit(text string, maxBytes int) string {
	return redactTaskText(truncate(text, maxBytes))
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
