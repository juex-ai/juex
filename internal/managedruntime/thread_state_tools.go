package managedruntime

import (
	"context"
	"errors"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func threadStateTools() []llm.ToolSpec {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	fields := func() map[string]any {
		return map[string]any{"title": text(), "description": text(), "acceptance": text(), "status_reason": text(), "status": map[string]any{"type": "string", "enum": []string{"todo", "doing", "done", "pending", "failed"}}, "priority": map[string]any{"type": "string", "enum": []string{"p0", "p1", "p2"}}}
	}
	tool := func(name, description string, fields map[string]any, required ...string) llm.ToolSpec {
		schema := map[string]any{"type": "object", "properties": fields, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		return llm.ToolSpec{Name: name, Description: description, Schema: schema}
	}
	update := fields()
	update["id"] = text()
	return []llm.ToolSpec{
		tool("check_inputs", "Explicitly confirm fully handled direct user inputs, or requirements completely recorded in durable Tasks. Other tools must have succeeded in a previous response: do not combine this call with other tools. Do not check waiting work or still-applicable constraints. This records model judgement only and never changes execution outcome.", map[string]any{"input_ids": map[string]any{"type": "array", "items": text(), "minItems": 1, "maxItems": MaxTrackedInputs}}, "input_ids"),
		tool("context_new", "Request an empty context after this entire tool batch settles. Clears enabled Notes and done Tasks, preserves unfinished Tasks, Thread identity, working files and history. Refused while inputs remain unchecked or await processing. Use only after current work and durable notes are complete.", map[string]any{}),
		tool("context_compact", "Request context compaction after this entire tool batch settles. Keeps unfinished Tasks, Notes, working files and complete history. Optional instructions focus the summary.", map[string]any{"instructions": text()}),
		tool("update_notes", "Replace this Thread's working notes in full, up to 2048 characters. Notes are current context on every model call. Empty content clears them. Completing all tasks clears Notes; finish Notes edits before marking the last task done.", map[string]any{"content": text()}, "content"),
		tool("list_tasks", "Read this Thread's current model-owned tasks.", map[string]any{}),
		tool("create_task", "Record a task with a title and description. Defaults to todo and p1. Record completion criteria in acceptance. Create a task before recording Notes for new work.", fields(), "title", "description"),
		tool("update_task", "Update a task by ID. Use doing while working, pending when external input is required, done after verifying acceptance, or failed when it cannot be completed. An unfinished todo/doing task prevents completion. Completing all tasks clears Notes.", update, "id"),
		tool("delete_task", "Delete a task by ID when it no longer belongs in this Thread's task list.", map[string]any{"id": text()}, "id"),
	}
}

func (r toolRunner) threadStateTool(ctx context.Context, work ToolWork) (ToolOutcome, bool) {
	if !IsThreadStateTool(work.Call.ToolName) {
		return ToolOutcome{}, false
	}
	if r.threadState == nil {
		return toolResult(work.Call, map[string]string{"error": "thread state unavailable"}, true), true
	}
	action, err := ParseThreadStateAction(work.Call)
	if err != nil {
		return toolResult(work.Call, map[string]string{"error": err.Error()}, true), true
	}
	result, err := r.threadState.ApplyThreadStateAction(ctx, work, action)
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrDenied) || errors.Is(err, ErrConflict) {
			return toolResult(work.Call, map[string]string{"error": err.Error()}, true), true
		}
		return retryTool(), true
	}
	return ToolOutcome{State: "ready", Content: string(result)}, true
}
