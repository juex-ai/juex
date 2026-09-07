package notes

import (
	"context"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func Inspection() *runtimemodule.Inspection {
	return runtimemodule.InspectState(1, []string{"notes.status"},
		func(t runtimemodule.ThreadContext) []string { return []string{NewNotesStore(t.Dir).Path} },
		func(_ context.Context, t runtimemodule.ThreadContext) (*NotesSnapshot, error) {
			return NewNotesStore(t.Dir).StatusSnapshot()
		},
	)
}
