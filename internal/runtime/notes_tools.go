package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/juex-ai/juex/internal/tools"
)

const NotesToolUpdate = "update_notes"

func NotesToolDefinitions() []tools.ToolDefinition {
	return []tools.ToolDefinition{{
		Name:  NotesToolUpdate,
		Group: tools.ToolGroupSessionState,
		Description: "Rewrite the model-owned session working notes with concise Markdown for the current plan, progress, and unresolved issues. " +
			"Use - [ ] and - [x] checkbox items when progress is useful. The full content is replaced on every call and must be at most 2048 characters; move long material to scratchpad files.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string", "description": "Complete replacement Markdown for the current working notes"},
			},
			"required": []string{"content"},
		},
	}}
}

func RegisterNotesTools(reg *tools.Registry, engine *Engine) error {
	if reg == nil {
		return fmt.Errorf("notes tools: nil registry")
	}
	if engine == nil {
		return fmt.Errorf("notes tools: nil engine")
	}
	return reg.Register(NotesToolDefinitions()[0].Bind(func(ctx context.Context, input map[string]any) (string, error) {
		return engine.handleUpdateNotes(input)
	}))
}

func (e *Engine) handleUpdateNotes(input map[string]any) (string, error) {
	store := e.notesStoreLocked()
	if store == nil {
		return "", fmt.Errorf("notes store is unavailable")
	}
	content, ok := input["content"].(string)
	if !ok {
		return "", fmt.Errorf("notes content is required")
	}
	snapshot, err := store.Update(content)
	if err != nil {
		return "", err
	}
	e.emitNotesUpdated(e.activeTurnID, snapshot)
	data, err := json.Marshal(map[string]any{"notes": snapshot})
	if err != nil {
		return "", fmt.Errorf("notes response encode: %w", err)
	}
	return string(data), nil
}
