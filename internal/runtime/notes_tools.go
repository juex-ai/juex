package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/juex-ai/juex/internal/tools"
)

const NotesToolUpdate = "update_notes"

const notesGuide = `Guide available via skill_load("juex-session-state").`

func NotesToolDefinitions() []tools.ToolDefinition {
	return []tools.ToolDefinition{{
		Name:        NotesToolUpdate,
		Group:       tools.ToolGroupSessionState,
		Description: "Replace concise session working notes; use scratchpad files for long material. " + notesGuide,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string"},
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
