package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

const NotesToolUpdate = "update_notes"

const notesGuide = `Guide available via skill_load("juex-session-state").`

const NotesModuleID runtimemodule.ID = "notes"

type NotesModule struct {
	engine *Engine
}

func NewNotesModule(engine *Engine) *NotesModule { return &NotesModule{engine: engine} }

func (*NotesModule) ID() runtimemodule.ID { return NotesModuleID }

func (m *NotesModule) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return NotesTools(m.engine), nil
}

func (m *NotesModule) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || m.engine == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	runtime := m.engine.SessionRuntimeSnapshot()
	text, ok := m.engine.notesContextFromStore(runtime.Notes)
	if !ok {
		return nil, nil
	}
	return []runtimemodule.ContextSection{{
		Key:        "session_notes",
		Source:     "runtime",
		Text:       text,
		Projection: runtimemodule.ContextProjectionRuntimeMessage,
		MessageID:  "runtime-notes",
		Budget:     runtimemodule.UnboundedContextBudget(),
	}}, nil
}

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

func NotesTools(engine *Engine) []tools.Tool {
	definition := NotesToolDefinitions()[0]
	if engine == nil {
		return []tools.Tool{definition.Bind(func(context.Context, map[string]any) (string, error) {
			return "", fmt.Errorf("notes store is unavailable")
		})}
	}
	return []tools.Tool{definition.Bind(func(_ context.Context, input map[string]any) (string, error) {
		return engine.handleUpdateNotes(input)
	})}
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
