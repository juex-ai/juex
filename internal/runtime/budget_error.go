package runtime

import (
	"errors"
	"fmt"
	"time"
)

const (
	BudgetErrorKindIterationLimit = "runtime_iteration_limit"
	BudgetErrorKindTimeout        = "runtime_timeout"
)

type BudgetError struct {
	Kind        string
	Budget      string
	TurnID      string
	MaxIters    int
	MaxDuration time.Duration
	Cause       error
}

func (e *BudgetError) Error() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case BudgetErrorKindIterationLimit:
		return fmt.Sprintf("turn iterations exceeded (%d)", e.MaxIters)
	case BudgetErrorKindTimeout:
		if e.Cause != nil {
			return fmt.Sprintf("turn runtime timeout after %s: %v", e.MaxDuration, e.Cause)
		}
		return fmt.Sprintf("turn runtime timeout after %s", e.MaxDuration)
	default:
		if e.Cause != nil {
			return e.Cause.Error()
		}
		return "turn runtime budget exceeded"
	}
}

func (e *BudgetError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *BudgetError) Details() map[string]any {
	if e == nil {
		return nil
	}
	out := map[string]any{
		"kind":   e.Kind,
		"budget": e.Budget,
	}
	if e.TurnID != "" {
		out["turn_id"] = e.TurnID
	}
	if e.MaxIters > 0 {
		out["max_iters"] = e.MaxIters
	}
	if e.MaxDuration > 0 {
		out["max_duration"] = e.MaxDuration.String()
		// Keep a machine-friendly scalar so external evaluators do not need
		// to parse Go duration strings.
		out["max_duration_ms"] = e.MaxDuration.Milliseconds()
	}
	return out
}

func AsBudgetError(err error) (*BudgetError, bool) {
	var budgetErr *BudgetError
	if errors.As(err, &budgetErr) {
		return budgetErr, true
	}
	return nil, false
}
