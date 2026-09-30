package memory

import (
	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

// Tools describes business capabilities. Runtime supplies identity and evidence.
func Tools(review bool) []llm.ToolSpec {
	query := objectSchema(map[string]any{"text": field(), "domain": field(), "entity": field(), "subject": field(), "predicate": field(), "view": field(), "status": field(), "at": field(), "source_agent_id": field(), "workspace": field(), "project": field(), "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}, "text")
	result := []llm.ToolSpec{
		{Name: "memory_domains", Description: "Read canonical Memory templates by id. Pass an empty id to discover domain names.", Schema: objectSchema(map[string]any{"id": field()}, "id")},
		{Name: "memory_search", Description: "Search shared Fleet knowledge previews, including cold entries. Use empty text to list. Scope fields are exact applicability filters. Knowledge does not grant raw Thread history access.", Schema: query},
		{Name: "memory_facts", Description: "Find persisted facts and entity identities. Use view=history before maintenance; view=as_of with RFC3339 at for effective-time truth. Equal names do not prove entity identity. Empty text lists matching facts.", Schema: query},
		{Name: "memory_read", Description: "Read a shared Entry ID returned by search/facts, including revision and provenance. Do not guess IDs. If Runtime returns a context handle for a large result, use read_context to read its complete content.", Schema: objectSchema(map[string]any{"id": entryIDField(), "view": field(), "at": field()}, "id")},
	}
	if review {
		return append(result, llm.ToolSpec{Name: "memory_decide", Description: "Settle this assigned review with applied, no_change or rejected. Applied requires bounded changes with expected_revision and original user evidence from this assignment. Preserve existing fact identities and provenance. Validation errors do not settle the review; correct and retry. Stop after a successful receipt.", Schema: objectSchema(map[string]any{"outcome": map[string]any{"type": "string", "enum": []string{"applied", "no_change", "rejected"}}, "reason": field(), "changes": map[string]any{"type": "array", "maxItems": 20, "items": changeSchema()}}, "outcome", "reason")})
	}
	return append(result, llm.ToolSpec{Name: "memory_propose", Description: "Submit explicitly requested stable knowledge for background review with original evidence from the current user input. After acceptance acknowledge submission and continue; do not poll or claim it is remembered. Never submit secrets, temporary progress, tool output or readily recovered repository facts. Reuse the key only with identical content.", Schema: objectSchema(map[string]any{"key": field(), "text": field(), "reason": field()}, "key", "text", "reason")})
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func field() map[string]any { return map[string]any{"type": "string"} }
func entryIDField() map[string]any {
	return map[string]any{"type": "string", "pattern": mc.EntryIDPattern, "minLength": 1, "maxLength": 64, "description": mc.EntryIDDescription}
}
func changeSchema() map[string]any {
	source := objectSchema(map[string]any{"fleet_id": field(), "agent_id": field(), "thread_id": field(), "generation_id": field(), "from": map[string]any{"type": "integer", "minimum": 1}, "through": map[string]any{"type": "integer", "minimum": 1}}, "fleet_id", "agent_id", "thread_id", "generation_id", "from", "through")
	sources := map[string]any{"type": "array", "maxItems": 100, "items": source}
	entity := objectSchema(map[string]any{"id": field(), "name": field(), "kind": field()}, "id", "name", "kind")
	fact := objectSchema(map[string]any{"id": entryIDField(), "domain": field(), "qualifiers": map[string]any{"type": "object", "additionalProperties": field()}, "reason": field(), "replaces": map[string]any{"type": "array", "items": entryIDField(), "maxItems": 20}, "due_at": field(), "time_note": field(), "subject": field(), "predicate": field(), "value": field(), "object": field(), "status": map[string]any{"type": "string", "enum": []string{"valid", "superseded", "corrected", "retracted", "disputed"}}, "source_type": map[string]any{"type": "string", "enum": []string{"user_statement", "self_report", "observation", "derived"}}, "sources": sources, "recorded_at": field(), "valid_from": field(), "valid_until": field()}, "id", "domain", "reason", "subject", "predicate", "status", "source_type", "sources", "recorded_at")
	entry := objectSchema(map[string]any{"id": entryIDField(), "name": field(), "summary": field(), "type": map[string]any{"type": "string", "enum": []string{"user", "feedback", "project", "reference"}}, "scope": map[string]any{"type": "object", "description": "Workspace/project applicability metadata, not access isolation. Preserve project-local qualifications.", "properties": map[string]any{"workspace": field(), "project": field()}, "additionalProperties": false}, "body": field(), "sources": sources, "entities": map[string]any{"type": "array", "items": entity, "maxItems": 50}, "facts": map[string]any{"type": "array", "items": fact, "maxItems": 100}}, "id", "name", "summary", "type", "scope", "body", "sources")
	return objectSchema(map[string]any{"entry": entry, "expected_revision": map[string]any{"type": "integer", "minimum": 0}, "delete": map[string]any{"type": "boolean"}}, "entry", "expected_revision")
}
