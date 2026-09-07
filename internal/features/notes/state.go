package notes

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/framework/runtime/workmem"
)

func (m *Module) NotesStore() *workmem.NotesStore {
	if m == nil {
		return nil
	}
	return m.store
}

func (m *Module) NotesStatusSnapshot() (*workmem.NotesSnapshot, error) {
	store := m.NotesStore()
	if store == nil {
		return nil, nil
	}
	return store.StatusSnapshot()
}

func (m *Module) notesContextFromStore(store *workmem.NotesStore) (string, bool) {
	if m == nil || store == nil {
		return "", false
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		return m.notesUnavailableContext(store, err), true
	}
	m.clearNotesContextError()
	return snapshot.RenderProviderContext()
}

func (m *Module) notesUnavailableContext(store *workmem.NotesStore, err error) string {
	errorText := err.Error()
	m.recordNotesContextError(store, err)

	reason := strings.Join(strings.Fields(errorText), " ")
	return fmt.Sprintf("Working notes unavailable (%s); fix %s or rewrite with update_notes", reason, notesProviderPath(store))
}

func (m *Module) recordNotesContextError(store *workmem.NotesStore, err error) {
	if m == nil || store == nil || err == nil {
		return
	}
	notesPath := store.Path
	errorText := err.Error()
	errorKey := notesPath + "\x00" + errorText
	m.notesContextErrorMu.Lock()
	emit := m.notesContextErrorKey != errorKey
	m.notesContextErrorKey = errorKey
	m.notesContextErrorMu.Unlock()
	if emit {
		_ = m.emit(events.Event{Type: "notes.errored", TurnID: m.activeTurnID(), Payload: workmem.NotesErroredPayload{
			Error: errorText,
			Path:  notesPath,
		}})
	}
}

func notesProviderPath(store *workmem.NotesStore) string {
	threadID := filepath.Base(filepath.Clean(store.ThreadDir))
	relative, _ := filepath.Rel(store.ThreadDir, store.Path)
	return filepath.ToSlash(filepath.Join(".juex", "threads", threadID, relative))
}

func (m *Module) clearNotesContextError() {
	if m == nil {
		return
	}
	m.notesContextErrorMu.Lock()
	m.notesContextErrorKey = ""
	m.notesContextErrorMu.Unlock()
}

func (m *Module) emitNotesUpdated(turnID string, snapshot workmem.NotesSnapshot) {
	if m == nil {
		return
	}
	m.clearNotesContextError()
	_ = m.emit(events.Event{Type: "notes.updated", TurnID: turnID, Payload: workmem.NotesUpdatedPayload(snapshot)})
}

func (m *Module) activeTurnID() string {
	if m == nil || m.currentTurnID == nil {
		return ""
	}
	return m.currentTurnID()
}

func (m *Module) emit(event events.Event) error {
	if m == nil || m.eventSink == nil {
		return nil
	}
	return m.eventSink(event)
}
