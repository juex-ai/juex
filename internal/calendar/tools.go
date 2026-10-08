package calendar

import "github.com/juex-ai/juex/internal/foundation/llm"

const AgentGuidance = `Calendar is shared Fleet scheduling. Use calendar_schedules before editing existing schedules; use calendar_occurrences for actual execution receipts. A schedule is not proof of execution. mode=reminder delivers a user notification without model work; mode=agent creates an independent Worker for an explicit same-Fleet Agent; mode=main delivers a new input into its existing Main context. Main accepted means delivery, not completion; cancelling after acceptance cannot retract the input. catch_up=none skips unprepared missed occurrences only during scheduler recovery; prepared deliveries keep their identity. Use agent_list to discover target IDs. Scheduling is independent of execution environments. Changes affect future occurrences only. Paused and archived schedules require explicit resume, which never backfills. Unknown results require checking the original operation, never creating a replacement occurrence. Use explicit IANA timezones and confirm ambiguous dates with the user.`

func object(fields map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": fields, "required": required, "additionalProperties": false}
}
func textField() map[string]any { return map[string]any{"type": "string"} }
func integer() map[string]any   { return map[string]any{"type": "integer"} }
func array(item map[string]any, max int) map[string]any {
	return map[string]any{"type": "array", "items": item, "maxItems": max}
}

func Tools() []llm.ToolSpec {
	rule := object(map[string]any{"frequency": map[string]any{"type": "string", "enum": []string{"once", "daily", "monthly", "yearly", "interval"}}, "timezone": textField(), "at": textField(), "year": integer(), "month": integer(), "day": integer(), "time": textField(), "months": array(integer(), 12), "days": array(integer(), 31), "times": array(textField(), 48), "weekdays": array(textField(), 7), "every_seconds": integer(), "lunar": object(map[string]any{"leap_month": map[string]any{"type": "string", "enum": []string{"regular", "leap", "both"}}}, "leap_month")}, "frequency")
	definition := object(map[string]any{"name": textField(), "content": textField(), "mode": map[string]any{"type": "string", "enum": []string{"reminder", "agent", "main"}}, "agent_id": textField(), "rule": rule, "catch_up": map[string]any{"type": "string", "enum": []string{"latest", "none"}}, "max_lateness_minutes": map[string]any{"type": "integer", "minimum": 1, "maximum": 1440}}, "name", "content", "mode", "rule")
	return []llm.ToolSpec{
		{Name: "calendar_schedules", Description: "List shared Fleet schedules including version, rule, target, pause reason and next occurrence. Paginate with next as offset.", Schema: object(map[string]any{"offset": integer(), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}, "offset", "limit")},
		{Name: "calendar_occurrences", Description: "Read execution history for a schedule ID, or pass empty schedule_id for all. Unknown outcomes retain original operation IDs; never replay them.", Schema: object(map[string]any{"schedule_id": textField(), "offset": integer(), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}, "schedule_id", "offset", "limit")},
		{Name: "calendar_change", Description: "Create or modify a schedule. For a new save use id empty and version 0; Runtime allocates a stable ID. Existing changes require the ID and current version from calendar_schedules. save requires a complete definition; other actions omit definition. cancel_occurrence also requires occurrence_id. once uses future RFC3339 at; lunar once uses year/month/day/time/timezone/lunar. daily uses times and optional weekdays; monthly days/times; yearly months/days/times; interval every_seconds >=60. Omit fields from other frequencies. Missing dates and DST gaps are skipped; folds fire once. Default catch_up=latest within max_lateness_minutes (1440 by default); none skips unprepared missed work on recovery without discarding live timer delays.", Schema: object(map[string]any{"id": textField(), "version": integer(), "action": map[string]any{"type": "string", "enum": []string{"save", "pause", "resume", "archive", "restore", "cancel_occurrence"}}, "definition": definition, "occurrence_id": textField()}, "id", "version", "action")},
	}
}
