package notes

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/juex-ai/juex/internal/foundation/events"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

const ToolUpdate = "update_notes"

const ModuleID runtimemodule.ID = "notes"

type Options struct {
	EventSink     func(events.Event) error
	CurrentTurnID func() string
}

type Module struct {
	store                *NotesStore
	eventSink            func(events.Event) error
	currentTurnID        func() string
	notesContextErrorMu  sync.Mutex
	notesContextErrorKey string
}

func New(store *NotesStore) *Module {
	return NewWithOptions(store, Options{})
}

func NewWithOptions(store *NotesStore, opts Options) *Module {
	return &Module{store: store, eventSink: opts.EventSink, currentTurnID: opts.CurrentTurnID}
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]toolcore.Tool, error) {
	return BoundTools(m), nil
}

func (m *Module) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
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

func (m *Module) ClearContextForRenewal(_ context.Context, generationID string) (runtimemodule.ContextRenewalClear, error) {
	if m == nil || m.store == nil {
		return runtimemodule.ContextRenewalClear{
			Finalize: func() error { return nil },
			Rollback: func() error { return nil },
		}, nil
	}
	finalize, rollback, err := m.store.StageClearForContextRenewal(generationID)
	if err != nil {
		return runtimemodule.ContextRenewalClear{}, err
	}
	m.clearNotesContextError()
	return runtimemodule.ContextRenewalClear{Finalize: finalize, Rollback: rollback}, nil
}

func ToolDefinitions() []toolcore.ToolDefinition {
	return []toolcore.ToolDefinition{{
		Name:            ToolUpdate,
		Group:           toolcore.ToolGroupThreadState,
		Guide:           toolcore.ToolGuide{Loader: "skill_load", Name: "juex-thread-state"},
		ExecutionPolicy: toolcore.ToolExecutionSerial,
		Description:     "Replace concise thread working notes; use working files for long material. ",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"content"},
		},
	}}
}

func BoundTools(module *Module) []toolcore.Tool {
	definition := ToolDefinitions()[0]
	if module == nil || module.store == nil {
		return []toolcore.Tool{definition.Bind(func(context.Context, map[string]any) (string, error) {
			return "", fmt.Errorf("notes store is unavailable")
		})}
	}
	return []toolcore.Tool{definition.Bind(func(_ context.Context, input map[string]any) (string, error) {
		return module.handleUpdateNotes(input)
	})}
}

func (m *Module) handleUpdateNotes(input map[string]any) (string, error) {
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
