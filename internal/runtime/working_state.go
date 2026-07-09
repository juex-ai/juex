package runtime

import (
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
)

func workingStateContextMessage(text string) llm.Message {
	msg := llm.TextMessage(llm.RoleUser, text)
	msg.ID = "runtime-working-state"
	msg.Kind = llm.MessageKindRuntimeContext
	return msg
}

func (e *Engine) workingStateStoreLocked() *WorkingStateStore {
	if e == nil || e.DisableWorkingState {
		return nil
	}
	if e.WorkingState != nil {
		return e.WorkingState
	}
	if e.Session == nil || e.Session.Dir == "" {
		return nil
	}
	e.WorkingState = NewWorkingStateStore(e.Session.Dir, WorkingStateOptions{})
	return e.WorkingState
}

func (e *Engine) workingStateStoreSnapshot() *WorkingStateStore {
	if e == nil || e.DisableWorkingState {
		return nil
	}
	if e.Session == nil || e.Session.Dir == "" {
		return nil
	}
	// Read-only UI snapshots must not wait for the turn-wide engine lock.
	// Constructing an equivalent store avoids mutating Engine while a turn runs.
	return NewWorkingStateStore(e.Session.Dir, WorkingStateOptions{})
}

func (e *Engine) WorkingStateStatusSnapshot() (*WorkingStateStatusSnapshot, error) {
	if e == nil {
		return nil, nil
	}
	if e.DisableWorkingState {
		return &WorkingStateStatusSnapshot{
			Disabled: true,
			State:    WorkingState{Version: 1},
		}, nil
	}
	store := e.workingStateStoreSnapshot()
	if store == nil {
		return nil, nil
	}
	return store.StatusSnapshot()
}

func (e *Engine) workingStateContextSnapshot() (string, bool) {
	store := e.workingStateStoreSnapshot()
	if store == nil {
		return "", false
	}
	state, err := store.Snapshot()
	if err != nil {
		return "", false
	}
	return state.RenderProviderContext()
}

func (e *Engine) workingStateContextLocked() (string, bool) {
	store := e.workingStateStoreLocked()
	if store == nil {
		return "", false
	}
	state, err := store.Snapshot()
	if err != nil {
		return "", false
	}
	return state.RenderProviderContext()
}

func (e *Engine) recordWorkingStateToolBatch(calls []llm.Block, results []toolCallResult) error {
	store := e.workingStateStoreLocked()
	if store == nil {
		return nil
	}
	workDir := ""
	if e != nil && e.Session != nil {
		workDir = workDirFromSessionDir(e.Session.Dir)
	}
	for i, result := range results {
		var call llm.Block
		if i < len(calls) {
			call = calls[i]
		}
		block := result.Block
		obs := toolFailureObservationFromToolResult(call, result)
		toolName := firstNonEmptyString(obs.ToolName, block.ToolName, call.ToolName)
		toolUseID := firstNonEmptyString(obs.ToolUseID, block.ToolUseID, call.ToolUseID)
		paths := relatedPathsFromInput(workDir, obs.Input)
		if block.IsError {
			errText := obs.Error
			if errText == "" {
				errText = strings.TrimSpace(obs.Content)
			}
			classified := classifyToolFailure(obs)
			if err := store.RecordOpenIssue(WorkingStateIssueObservation{
				ToolName:     toolName,
				ToolUseID:    toolUseID,
				Text:         workingStateIssueText(toolName, toolUseID, errText, obs.Content),
				Severity:     workingStateSeverityForFailure(classified.Classification),
				Confidence:   workingStateConfidenceForFailure(classified.Classification),
				RelatedPaths: paths,
			}); err != nil {
				return err
			}
			continue
		}
		if mutatesRelatedPath(toolName, paths, paths) {
			if err := store.RecordArtifactMutation(toolName, toolUseID, paths); err != nil {
				return err
			}
			if err := store.MarkPathsStale(paths, toolName, toolUseID); err != nil {
				return err
			}
		}
		if isWorkingStateCheckTool(toolName) {
			if err := store.RecordSuccessfulCheck(WorkingStateCheckObservation{
				ToolName:     toolName,
				ToolUseID:    toolUseID,
				Text:         workingStateCheckText(toolName, toolUseID, obs.Content),
				RelatedPaths: paths,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func defaultWorkingStatePatchSource(patch *WorkingStatePatch, source WorkingStateSource) {
	if patch == nil {
		return
	}
	if patch.Goal != nil && patch.Goal.Source == "" {
		patch.Goal.Source = source
	}
	defaultRecordSource(patch.HardConstraints, source)
	defaultRecordSource(patch.Artifacts, source)
	defaultRecordSource(patch.Checks, source)
	defaultRecordSource(patch.OpenIssues, source)
	defaultRecordSource(patch.ToolFailures, source)
	defaultRecordSource(patch.LastSuccessfulChecks, source)
	defaultRecordSource(patch.StaleChecks, source)
	defaultRecordSource(patch.ActiveProcesses, source)
	defaultRecordSource(patch.RuntimeBudget, source)
}

func defaultRecordSource(records []WorkingStateRecord, source WorkingStateSource) {
	for i := range records {
		if records[i].Source == "" {
			records[i].Source = source
		}
	}
}

func workingStateIssueText(toolName, toolUseID, errText, content string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tool=%s", toolName)
	if toolUseID != "" {
		fmt.Fprintf(&b, " tool_use_id=%s", toolUseID)
	}
	b.WriteString(" failed")
	if strings.TrimSpace(errText) != "" {
		fmt.Fprintf(&b, ": %s", strings.TrimSpace(errText))
	} else if strings.TrimSpace(content) != "" {
		fmt.Fprintf(&b, ": %s", strings.TrimSpace(content))
	}
	errText = strings.TrimSpace(errText)
	if preview := strings.TrimSpace(content); preview != "" && preview != errText {
		fmt.Fprintf(&b, " output_preview=%q", preview)
	}
	return b.String()
}

func workingStateCheckText(toolName, toolUseID, content string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tool=%s", toolName)
	if toolUseID != "" {
		fmt.Fprintf(&b, " tool_use_id=%s", toolUseID)
	}
	b.WriteString(" succeeded")
	if preview := strings.TrimSpace(content); preview != "" {
		fmt.Fprintf(&b, ": %s", preview)
	}
	return b.String()
}

func workingStateSeverityForFailure(class ToolFailureClassification) WorkingStateSeverity {
	switch class {
	case ToolFailureRuntimeFatal, ToolFailureRepeatedStuck:
		return WorkingStateSeverityCritical
	case ToolFailureExternalBlocked:
		return WorkingStateSeverityHigh
	case ToolFailureNonblockingExploratory:
		return WorkingStateSeverityLow
	default:
		return WorkingStateSeverityMedium
	}
}

func workingStateConfidenceForFailure(class ToolFailureClassification) float64 {
	switch class {
	case ToolFailureNonblockingExploratory:
		return 0.45
	case ToolFailureRuntimeFatal, ToolFailureRepeatedStuck:
		return 0.90
	default:
		return 0.75
	}
}

func isWorkingStateCheckTool(toolName string) bool {
	name := strings.ToLower(toolName)
	switch name {
	case "grep", "exec_command":
		return true
	}
	return containsAny(name, "check", "test", "lint", "build", "verify")
}
