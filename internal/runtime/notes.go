package runtime

import (
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func notesContextMessage(text string) llm.Message {
	message := llm.TextMessage(llm.RoleUser, text)
	message.ID = "runtime-notes"
	message.Kind = llm.MessageKindRuntimeContext
	return message
}

func (e *Engine) notesStoreLocked() *NotesStore {
	if e == nil {
		return nil
	}
	return e.Notes
}

func (e *Engine) NotesStatusSnapshot() (*NotesSnapshot, error) {
	if e == nil || e.Session == nil || e.Session.Dir == "" {
		return nil, nil
	}
	return NewNotesStore(e.Session.Dir).StatusSnapshot()
}

func (e *Engine) notesContextSnapshot() (string, bool) {
	if e == nil || e.Session == nil || e.Session.Dir == "" {
		return "", false
	}
	snapshot, err := NewNotesStore(e.Session.Dir).Snapshot()
	if err != nil {
		return "", false
	}
	return snapshot.RenderProviderContext()
}

func (e *Engine) notesContextLocked() (string, bool) {
	store := e.notesStoreLocked()
	if store == nil {
		return "", false
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		return "", false
	}
	return snapshot.RenderProviderContext()
}

func (e *Engine) emitNotesUpdated(turnID string, snapshot NotesSnapshot) {
	if e == nil {
		return
	}
	e.emit(events.Event{Type: "notes.updated", TurnID: turnID, Payload: NotesUpdatedPayload{
		Content:   snapshot.Content,
		UpdatedAt: snapshot.UpdatedAt,
	}})
}
