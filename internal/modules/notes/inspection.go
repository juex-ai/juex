package notes

import (
	"context"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
)

func Inspection() *runtimemodule.Inspection {
	return runtimemodule.InspectState(1, []string{"notes.status"},
		func(t runtimemodule.ThreadContext) []string { return []string{workmem.NewNotesStore(t.Dir).Path} },
		func(_ context.Context, t runtimemodule.ThreadContext) (*workmem.NotesSnapshot, error) {
			return workmem.NewNotesStore(t.Dir).StatusSnapshot()
		},
	)
}
