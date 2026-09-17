package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	"strings"
)

func ToolDefinitions() []toolcore.ToolDefinition {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	fields := func() map[string]any {
		return map[string]any{
			"title": text(), "description": text(), "acceptance": text(), "status_reason": text(),
			"status":   map[string]any{"type": "string", "enum": []string{"todo", "doing", "done", "pending", "failed"}},
			"priority": map[string]any{"type": "string", "enum": []string{"p0", "p1", "p2"}},
		}
	}
	definition := func(name, description string, properties map[string]any, required ...string) toolcore.ToolDefinition {
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		return toolcore.ToolDefinition{Name: name, Group: toolcore.ToolGroupThreadState, Guide: toolcore.ToolGuide{Loader: "skill_load", Name: "juex-thread-state"}, ExecutionPolicy: toolcore.ToolExecutionSerial, Description: description, Schema: schema}
	}
	update := fields()
	update["id"] = text()
	return []toolcore.ToolDefinition{
		definition(ToolList, "Read this Thread's model-owned tasks before creating or updating work.", map[string]any{}),
		definition(ToolCreate, "Record a task with a title and description. Defaults to todo and p1. Record completion criteria in acceptance. A request fully captured in durable tasks can then be checked with check_inputs.", fields(), "title", "description"),
		definition(ToolUpdate, "Update a task by ID. Use doing while working, pending when new external input is required, done only after verifying acceptance, or failed when it cannot be completed.", update, "id"),
		definition(ToolDelete, "Delete a task by ID when it no longer belongs in this Thread's task list.", map[string]any{"id": text()}, "id"),
	}
}
func BoundTools(module *Module) []toolcore.Tool {
	definitions := ToolDefinitions()
	result := make([]toolcore.Tool, 0, len(definitions))
	for _, definition := range definitions {
		name := definition.Name
		result = append(result, definition.Bind(func(_ context.Context, in map[string]any) (string, error) {
			if module == nil || module.store == nil {
				return "", fmt.Errorf("tasks state is not configured")
			}
			return module.call(name, in)
		}))
	}
	return result
}
func (m *Module) call(name string, in map[string]any) (string, error) {
	str := func(key string) string { value, _ := in[key].(string); return strings.TrimSpace(value) }
	marshal := func(value any) (string, error) { data, err := json.Marshal(value); return string(data), err }
	if name == ToolList {
		state, err := m.store.Snapshot()
		if err != nil {
			return "", err
		}
		return marshal(TasksSnapshot{Tasks: state.Tasks})
	}
	var task Task
	var err error
	switch name {
	case ToolCreate:
		task, err = m.store.Create(Create{Title: str("title"), Description: str("description"), Acceptance: str("acceptance"), Status: Status(str("status")), Priority: Priority(str("priority")), StatusReason: str("status_reason")})
	case ToolUpdate:
		update := Update{}
		changed := false
		for key, target := range map[string]**string{"title": &update.Title, "description": &update.Description, "acceptance": &update.Acceptance, "status_reason": &update.StatusReason} {
			if _, ok := in[key]; ok {
				value := str(key)
				*target = &value
				changed = true
			}
		}
		if _, ok := in["status"]; ok {
			update.Status = Status(str("status"))
			if update.Status == "" {
				return "", fmt.Errorf("task status cannot be empty")
			}
			changed = true
		}
		if _, ok := in["priority"]; ok {
			update.Priority = Priority(str("priority"))
			if update.Priority == "" {
				return "", fmt.Errorf("task priority cannot be empty")
			}
			changed = true
		}
		if !changed {
			return "", fmt.Errorf("update_task requires at least one editable task field")
		}
		task, err = m.store.Update(str("id"), update)
	case ToolDelete:
		err = m.store.Delete(str("id"))
	default:
		return "", fmt.Errorf("unknown tasks tool %q", name)
	}
	if err != nil {
		return "", err
	}
	m.emitTasksUpdated(m.activeTurnID())
	if name == ToolDelete {
		return marshal(map[string]any{"deleted": true, "id": str("id")})
	}
	return marshal(map[string]any{"task": task})
}
