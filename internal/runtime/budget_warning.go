package runtime

import (
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

const (
	BudgetWarningKindIterationLimit = "runtime_iteration_warning"
	BudgetWarningKindTimeout        = "runtime_timeout_warning"

	runtimeBudgetFinalizationHint = "Runtime budget is nearly exhausted"
)

type turnBudgetStatus struct {
	TurnID            string
	IterationNear     bool
	DurationNear      bool
	RemainingIters    int
	MaxIters          int
	RemainingDuration time.Duration
	MaxDuration       time.Duration
}

func currentTurnBudgetStatus(turnID string, iter, maxIters int, start time.Time, maxDuration time.Duration) turnBudgetStatus {
	remainingIters := maxIters - iter
	if remainingIters < 0 {
		remainingIters = 0
	}
	remainingDuration := time.Duration(0)
	durationNear := false
	if maxDuration > 0 {
		remainingDuration = maxDuration - time.Since(start)
		if remainingDuration < 0 {
			remainingDuration = 0
		}
		durationNear = remainingDuration <= budgetWarningDurationThreshold(maxDuration)
	}
	return turnBudgetStatus{
		TurnID:            turnID,
		IterationNear:     remainingIters <= 1,
		DurationNear:      durationNear,
		RemainingIters:    remainingIters,
		MaxIters:          maxIters,
		RemainingDuration: remainingDuration,
		MaxDuration:       maxDuration,
	}
}

func budgetWarningDurationThreshold(maxDuration time.Duration) time.Duration {
	if maxDuration <= 0 {
		return 0
	}
	threshold := maxDuration / 5
	if threshold > 30*time.Second {
		return 30 * time.Second
	}
	if threshold < time.Second {
		return maxDuration / 2
	}
	return threshold
}

func (s turnBudgetStatus) Near() bool {
	return s.IterationNear || s.DurationNear
}

func (s turnBudgetStatus) IterationWarningDetails() map[string]any {
	return map[string]any{
		"kind":            BudgetWarningKindIterationLimit,
		"budget":          "iterations",
		"turn_id":         s.TurnID,
		"remaining_iters": s.RemainingIters,
		"max_iters":       s.MaxIters,
	}
}

func (s turnBudgetStatus) DurationWarningDetails() map[string]any {
	return map[string]any{
		"kind":                  BudgetWarningKindTimeout,
		"budget":                "duration",
		"turn_id":               s.TurnID,
		"remaining_duration":    s.RemainingDuration.String(),
		"remaining_duration_ms": s.RemainingDuration.Milliseconds(),
		"max_duration":          s.MaxDuration.String(),
		"max_duration_ms":       s.MaxDuration.Milliseconds(),
	}
}

func budgetFinalizationMessage(status turnBudgetStatus) llm.Message {
	parts := make([]string, 0, 2)
	if status.IterationNear {
		parts = append(parts, fmt.Sprintf("%d provider request(s) remain", status.RemainingIters))
	}
	if status.DurationNear {
		parts = append(parts, fmt.Sprintf("%s remain", status.RemainingDuration.Round(time.Millisecond)))
	}
	detail := strings.Join(parts, "; ")
	if detail != "" {
		detail = " (" + detail + ")"
	}
	return llm.TextMessage(llm.RoleUser, runtimeBudgetFinalizationHint+detail+". Do not start non-essential tools; provide the best final answer now.")
}
