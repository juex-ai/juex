package runtime

import "github.com/juex-ai/juex/internal/runtime/workmem"

type GoalStatus = workmem.GoalStatus

const (
	GoalStatusInProgress = workmem.GoalStatusInProgress
	GoalStatusSuccess    = workmem.GoalStatusSuccess
	GoalStatusFailure    = workmem.GoalStatusFailure
)

type GoalState = workmem.GoalState
type GoalStateUpdate = workmem.GoalStateUpdate
type GoalStateCreate = workmem.GoalStateCreate
type GoalStateOptions = workmem.GoalStateOptions
type GoalStateStore = workmem.GoalStateStore
type GoalGateDecision = workmem.GoalGateDecision
type GoalStatusSnapshot = workmem.GoalStatusSnapshot

func NewGoalStateStore(sessionDir string, opts GoalStateOptions) *GoalStateStore {
	return workmem.NewGoalStateStore(sessionDir, opts)
}

const MaxNotesCharacters = workmem.MaxNotesCharacters

type NotesSnapshot = workmem.NotesSnapshot
type NotesStore = workmem.NotesStore

func NewNotesStore(sessionDir string) *NotesStore {
	return workmem.NewNotesStore(sessionDir)
}
