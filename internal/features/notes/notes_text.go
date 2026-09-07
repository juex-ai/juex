package notes

import (
	"regexp"
)

var notesSecretAssignmentPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|authorization|cookie|token)[A-Za-z0-9_-]*\s*[:=]\s*("[^"\n\r]*"|'[^'\n\r]*'|(bearer\s+)?[^ \n\r\t]+)`)

var notesBearerPattern = regexp.MustCompile(`(?i)bearer\s+[^ \n\r\t]+`)

var notesOpenAIKeyPattern = regexp.MustCompile(`sk-[A-Za-z0-9_-]{6,}`)

func redactNotesText(text string) string {
	text = notesSecretAssignmentPattern.ReplaceAllString(text, "[REDACTED]")
	text = notesBearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
	return notesOpenAIKeyPattern.ReplaceAllString(text, "[REDACTED]")
}
