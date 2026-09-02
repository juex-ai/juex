package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/juex-ai/juex/internal/events"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/tools"
)

const NotesToolUpdate = "update_notes"

const notesGuide = `Guide available via skill_load("juex-thread-state").`

const NotesModuleID runtimemodule.ID = "notes"

type NotesModuleOptions struct {
	EventSink     func(events.Event) error
	CurrentTurnID func() string
}

type NotesModule struct {
	store                *workmem.NotesStore
	eventSink            func(events.Event) error
	currentTurnID        func() string
	notesContextErrorMu  sync.Mutex
	notesContextErrorKey string
}

func NewNotesModule(store *workmem.NotesStore) *NotesModule {
	return NewNotesModuleWithOptions(store, NotesModuleOptions{})
}

func NewNotesModuleWithOptions(store *workmem.NotesStore, opts NotesModuleOptions) *NotesModule {
	return &NotesModule{store: store, eventSink: opts.EventSink, currentTurnID: opts.CurrentTurnID}
}

func (*NotesModule) ID() runtimemodule.ID { return NotesModuleID }

func (m *NotesModule) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return NotesTools(m), nil
}

func (m *NotesModule) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	text, ok := m.notesContextFromStore(m.store)
	if !ok {
		return nil, nil
	}
	return []runtimemodule.ContextSection{{
		Key:        "thread_notes",
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
		Group:       tools.ToolGroupThreadState,
		Description: "Replace concise thread working notes; use scratchpad files for long material. " + notesGuide,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"content"},
		},
	}}
}

func NotesTools(module *NotesModule) []tools.Tool {
	definition := NotesToolDefinitions()[0]
	if module == nil || module.store == nil {
		return []tools.Tool{definition.Bind(func(context.Context, map[string]any) (string, error) {
			return "", fmt.Errorf("notes store is unavailable")
		})}
	}
	return []tools.Tool{definition.Bind(func(_ context.Context, input map[string]any) (string, error) {
		return module.handleUpdateNotes(input)
	})}
}

func (m *NotesModule) handleUpdateNotes(input map[string]any) (string, error) {
	store := m.store
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
	m.emitNotesUpdated(m.activeTurnID(), snapshot)
	data, err := json.Marshal(map[string]any{"notes": snapshot})
	if err != nil {
		return "", fmt.Errorf("notes response encode: %w", err)
	}
	return string(data), nil
}
