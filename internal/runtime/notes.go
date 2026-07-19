package runtime

import (
	"fmt"
	"path/filepath"
	"strings"

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
	return e.currentNotesStore()
}

// SetNotesStore installs the store used by all Notes runtime paths.
func (e *Engine) SetNotesStore(store *NotesStore) {
	e.setNotesStore(store)
	e.clearNotesContextError()
}

func (e *Engine) NotesStatusSnapshot() (*NotesSnapshot, error) {
	store := e.notesStoreLocked()
	if store == nil {
		return nil, nil
	}
	return store.StatusSnapshot()
}

func (e *Engine) notesContextSnapshot() (string, bool) {
	store := e.notesStoreLocked()
	if store == nil {
		return "", false
	}
	return e.notesContextFromStore(store)
}

func (e *Engine) notesContextLocked() (string, bool) {
	store := e.notesStoreLocked()
	if store == nil {
		return "", false
	}
	return e.notesContextFromStore(store)
}

func (e *Engine) notesContextFromStore(store *NotesStore) (string, bool) {
	if e == nil || store == nil {
		return "", false
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		return e.notesUnavailableContext(store, err), true
	}
	e.clearNotesContextError()
	return snapshot.RenderProviderContext()
}

func (e *Engine) notesUnavailableContext(store *NotesStore, err error) string {
	errorText := err.Error()
	e.recordNotesContextError(store, err)

	reason := strings.Join(strings.Fields(errorText), " ")
	return fmt.Sprintf("Working notes unavailable (%s); fix %s or rewrite with update_notes", reason, notesProviderPath(store))
}

func (e *Engine) recordNotesContextError(store *NotesStore, err error) {
	if e == nil || store == nil || err == nil {
		return
	}
	notesPath := filepath.Join(store.SessionDir, NotesFileName)
	errorText := err.Error()
	errorKey := notesPath + "\x00" + errorText
	e.notesContextErrorMu.Lock()
	emit := e.notesContextErrorKey != errorKey
	e.notesContextErrorKey = errorKey
	e.notesContextErrorMu.Unlock()
	if emit {
		e.emit(events.Event{Type: "notes.errored", TurnID: e.PendingInputStatus().TurnID, Payload: NotesErroredPayload{
			Error: errorText,
			Path:  notesPath,
		}})
	}
}

func notesProviderPath(store *NotesStore) string {
	sessionID := filepath.Base(filepath.Clean(store.SessionDir))
	return filepath.ToSlash(filepath.Join(".juex", "sessions", sessionID, NotesFileName))
}

func (e *Engine) clearNotesContextError() {
	if e == nil {
		return
	}
	e.notesContextErrorMu.Lock()
	e.notesContextErrorKey = ""
	e.notesContextErrorMu.Unlock()
}

func (e *Engine) emitNotesUpdated(turnID string, snapshot NotesSnapshot) {
	if e == nil {
		return
	}
	e.clearNotesContextError()
	e.emit(events.Event{Type: "notes.updated", TurnID: turnID, Payload: NotesUpdatedPayload{
		Content:   snapshot.Content,
		UpdatedAt: snapshot.UpdatedAt,
	}})
}
