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

type WorkingStateSource = workmem.WorkingStateSource

const (
	WorkingStateSourceModelSummary   = workmem.WorkingStateSourceModelSummary
	WorkingStateSourceToolResult     = workmem.WorkingStateSourceToolResult
	WorkingStateSourceHookExtraction = workmem.WorkingStateSourceHookExtraction
	WorkingStateSourceUserInput      = workmem.WorkingStateSourceUserInput
)

type WorkingStateSeverity = workmem.WorkingStateSeverity

const (
	WorkingStateSeverityLow      = workmem.WorkingStateSeverityLow
	WorkingStateSeverityMedium   = workmem.WorkingStateSeverityMedium
	WorkingStateSeverityHigh     = workmem.WorkingStateSeverityHigh
	WorkingStateSeverityCritical = workmem.WorkingStateSeverityCritical
)

type WorkingStateRecord = workmem.WorkingStateRecord
type WorkingState = workmem.WorkingState
type WorkingStateStatusSnapshot = workmem.WorkingStateStatusSnapshot
type WorkingStatePatch = workmem.WorkingStatePatch
type WorkingStateOptions = workmem.WorkingStateOptions
type WorkingStateStore = workmem.WorkingStateStore
type WorkingStateIssueObservation = workmem.WorkingStateIssueObservation
type WorkingStateCheckObservation = workmem.WorkingStateCheckObservation

func NewWorkingStateStore(sessionDir string, opts WorkingStateOptions) *WorkingStateStore {
	return workmem.NewWorkingStateStore(sessionDir, opts)
}
