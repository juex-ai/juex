package notes

import (
	"testing"
)

func TestNotesContextFromStoreHandlesNil(t *testing.T) {
	var nilModule *Module
	if text, ok := nilModule.notesContextFromStore(NewNotesStore(t.TempDir())); ok || text != "" {
		t.Fatalf("nil module notes context = %q, %v", text, ok)
	}

	module := New(nil)
	if text, ok := module.notesContextFromStore(nil); ok || text != "" {
		t.Fatalf("nil store notes context = %q, %v", text, ok)
	}
}
