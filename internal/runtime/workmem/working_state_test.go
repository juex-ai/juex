package workmem

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeWorkingStatePathsUsesPortableSeparators(t *testing.T) {
	got := normalizeWorkingStatePaths([]string{`dir\artifact.txt`, "dir/artifact.txt"})
	if len(got) != 1 || got[0] != "dir/artifact.txt" {
		t.Fatalf("paths = %+v", got)
	}
}

func TestWorkingStateStorePrunesSuccessfulChecksToRecentRecords(t *testing.T) {
	now := time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC)
	store := NewWorkingStateStore(t.TempDir(), WorkingStateOptions{Now: func() time.Time { return now }})

	for i := 0; i < 30; i++ {
		i := i
		store.Now = func() time.Time { return now.Add(time.Duration(i) * time.Minute) }
		if err := store.RecordSuccessfulCheck(WorkingStateCheckObservation{
			ToolName:     "exec_command",
			ToolUseID:    fmt.Sprintf("check-%02d", i),
			Text:         fmt.Sprintf("check %02d", i),
			RelatedPaths: []string{fmt.Sprintf("pkg/%02d.go", i)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Checks) != workingStateChecksActiveLimit {
		t.Fatalf("checks should be pruned to %d, got %d", workingStateChecksActiveLimit, len(state.Checks))
	}
	if len(state.LastSuccessfulChecks) != workingStateLastSuccessfulChecksActiveLimit {
		t.Fatalf("last successful checks should be pruned to %d, got %d", workingStateLastSuccessfulChecksActiveLimit, len(state.LastSuccessfulChecks))
	}
	if state.LastSuccessfulChecks[0].Text != "check 29" {
		t.Fatalf("newest check should be first, got %+v", state.LastSuccessfulChecks[0])
	}
	if workingStateRecordsContainText(state.LastSuccessfulChecks, "check 00") {
		t.Fatalf("oldest check should have been pruned: %+v", state.LastSuccessfulChecks)
	}
	rendered, ok := state.RenderProviderContext()
	if !ok || !strings.Contains(rendered, "check 29") || strings.Contains(rendered, "check 00") {
		t.Fatalf("provider context should expose recent checks only:\n%s", rendered)
	}
}

func TestPruneWorkingStateRecordsKeepsHighSeverityAndResolvedTail(t *testing.T) {
	now := time.Date(2026, 6, 23, 11, 0, 0, 0, time.UTC)
	records := []WorkingStateRecord{{
		ID:         "critical-old",
		Text:       "critical old issue",
		Severity:   WorkingStateSeverityCritical,
		Confidence: 0.95,
		CreatedAt:  now,
	}}
	for i := 0; i < workingStateOpenIssuesActiveLimit+4; i++ {
		records = append(records, WorkingStateRecord{
			ID:         fmt.Sprintf("low-%02d", i),
			Text:       fmt.Sprintf("low issue %02d", i),
			Severity:   WorkingStateSeverityLow,
			Confidence: 0.60,
			CreatedAt:  now.Add(time.Duration(i+1) * time.Minute),
		})
	}
	for i := 0; i < workingStateOpenIssuesResolvedLimit+3; i++ {
		records = append(records, WorkingStateRecord{
			ID:         fmt.Sprintf("resolved-%02d", i),
			Text:       fmt.Sprintf("resolved issue %02d", i),
			Severity:   WorkingStateSeverityMedium,
			Confidence: 0.70,
			CreatedAt:  now.Add(time.Duration(i) * time.Minute),
			ResolvedAt: now.Add(time.Duration(i+1) * time.Hour),
		})
	}

	pruned := pruneWorkingStateRecords(records, workingStateOpenIssuesActiveLimit, workingStateOpenIssuesResolvedLimit)
	if len(pruned) != workingStateOpenIssuesActiveLimit+workingStateOpenIssuesResolvedLimit {
		t.Fatalf("pruned records length = %d", len(pruned))
	}
	if !workingStateRecordsContainText(pruned, "critical old issue") {
		t.Fatalf("critical active issue should be retained: %+v", pruned)
	}
	if workingStateRecordsContainText(pruned, "low issue 00") {
		t.Fatalf("old low severity issue should be pruned: %+v", pruned)
	}
	if !workingStateRecordsContainText(pruned, fmt.Sprintf("resolved issue %02d", workingStateOpenIssuesResolvedLimit+2)) {
		t.Fatalf("latest resolved issue should be retained: %+v", pruned)
	}
	if workingStateRecordsContainText(pruned, "resolved issue 00") {
		t.Fatalf("old resolved issue should be pruned: %+v", pruned)
	}
}

func TestWorkingStateStoreStalesAndRefreshesRelatedChecksWithAbsolutePaths(t *testing.T) {
	now := time.Date(2026, 6, 14, 11, 0, 0, 0, time.UTC)
	store := NewWorkingStateStore(t.TempDir(), WorkingStateOptions{Now: func() time.Time { return now }})
	target := filepath.Join(t.TempDir(), "artifact.txt")

	if err := store.RecordSuccessfulCheck(WorkingStateCheckObservation{
		ToolName:     "check_ready",
		ToolUseID:    "check-1",
		Text:         "artifact ready",
		RelatedPaths: []string{target},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPathsStale([]string{target}, "write", "write-1"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LastSuccessfulChecks) != 0 || len(activeWorkingStateRecords(state.StaleChecks)) != 1 {
		t.Fatalf("after stale last=%+v stale=%+v", state.LastSuccessfulChecks, state.StaleChecks)
	}

	store.Now = func() time.Time { return now.Add(time.Minute) }
	if err := store.RecordSuccessfulCheck(WorkingStateCheckObservation{
		ToolName:     "check_ready",
		ToolUseID:    "check-2",
		Text:         "artifact ready after rewrite",
		RelatedPaths: []string{target},
	}); err != nil {
		t.Fatal(err)
	}
	state, err = store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LastSuccessfulChecks) != 1 {
		t.Fatalf("last successful checks = %+v", state.LastSuccessfulChecks)
	}
	if len(activeWorkingStateRecords(state.StaleChecks)) != 0 {
		t.Fatalf("stale checks should be resolved: %+v", state.StaleChecks)
	}
}

func workingStateRecordsContainText(records []WorkingStateRecord, text string) bool {
	for _, rec := range records {
		if rec.Text == text {
			return true
		}
	}
	return false
}
