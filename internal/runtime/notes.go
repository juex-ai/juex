package runtime

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/runtime/workmem"
)

func (m *NotesModule) NotesStore() *workmem.NotesStore {
	if m == nil {
		return nil
	}
	return m.store
}

func (m *NotesModule) NotesStatusSnapshot() (*workmem.NotesSnapshot, error) {
	store := m.NotesStore()
	if store == nil {
		return nil, nil
	}
	return store.StatusSnapshot()
}

func (m *NotesModule) NotesCompactionState() (string, error) {
	store := m.NotesStore()
	if store == nil {
		return "", nil
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		m.recordNotesContextError(store, err)
		return "", nil
	}
	m.clearNotesContextError()
	return snapshot.Content, nil
}

func (m *NotesModule) notesContextFromStore(store *workmem.NotesStore) (string, bool) {
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

func (m *NotesModule) notesUnavailableContext(store *workmem.NotesStore, err error) string {
	errorText := err.Error()
	m.recordNotesContextError(store, err)

	reason := strings.Join(strings.Fields(errorText), " ")
	return fmt.Sprintf("Working notes unavailable (%s); fix %s or rewrite with update_notes", reason, notesProviderPath(store))
}

func (m *NotesModule) recordNotesContextError(store *workmem.NotesStore, err error) {
	if m == nil || store == nil || err == nil {
		return
	}
	notesPath := filepath.Join(store.SessionDir, workmem.NotesFileName)
	errorText := err.Error()
	errorKey := notesPath + "\x00" + errorText
	m.notesContextErrorMu.Lock()
	emit := m.notesContextErrorKey != errorKey
	m.notesContextErrorKey = errorKey
	m.notesContextErrorMu.Unlock()
	if emit {
		_ = m.emit(events.Event{Type: "notes.errored", TurnID: m.activeTurnID(), Payload: NotesErroredPayload{
			Error: errorText,
			Path:  notesPath,
		}})
	}
}

func notesProviderPath(store *workmem.NotesStore) string {
	sessionID := filepath.Base(filepath.Clean(store.SessionDir))
	return filepath.ToSlash(filepath.Join(".juex", "sessions", sessionID, workmem.NotesFileName))
}

func (m *NotesModule) clearNotesContextError() {
	if m == nil {
		return
	}
	m.notesContextErrorMu.Lock()
	m.notesContextErrorKey = ""
	m.notesContextErrorMu.Unlock()
}

func (m *NotesModule) emitNotesUpdated(turnID string, snapshot workmem.NotesSnapshot) {
	if m == nil {
		return
	}
	m.clearNotesContextError()
	_ = m.emit(events.Event{Type: "notes.updated", TurnID: turnID, Payload: NotesUpdatedPayload{
		Content:   snapshot.Content,
		UpdatedAt: snapshot.UpdatedAt,
	}})
}

func (m *NotesModule) activeTurnID() string {
	if m == nil || m.currentTurnID == nil {
		return ""
	}
	return m.currentTurnID()
}

func (m *NotesModule) emit(event events.Event) error {
	if m == nil || m.eventSink == nil {
		return nil
	}
	return m.eventSink(event)
}
