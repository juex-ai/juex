package managedruntime

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

type UsageQuery struct {
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id,omitempty"`
	From     string `json:"from"`
	Until    string `json:"until"`
	Group    string `json:"group"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

func (q UsageQuery) Validate(operator bool) error {
	for _, id := range []string{q.TenantID, q.UserID} {
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return ErrInvalid
			}
		}
	}
	if !operator && q.TenantID == "" || q.UserID != "" && q.TenantID == "" {
		return ErrInvalid
	}
	if q.Group != "day" && q.Group != "month" || q.Offset < 0 || q.Offset > 1_000_000 || q.Limit < 1 || q.Limit > 200 {
		return ErrInvalid
	}
	if q.From == "" && q.Until == "" {
		return nil
	}
	from, e1 := time.Parse(time.DateOnly, q.From)
	until, e2 := time.Parse(time.DateOnly, q.Until)
	if e1 != nil || e2 != nil || !until.After(from) || until.Sub(from) > 3660*24*time.Hour {
		return ErrInvalid
	}
	return nil
}

func (q UsageQuery) WithDefaultRange(now time.Time, timezone string) (UsageQuery, error) {
	if q.From != "" || q.Until != "" {
		return q, nil
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return q, ErrInvalid
	}
	today := now.In(location)
	q.From, q.Until = today.AddDate(0, 0, -29).Format(time.DateOnly), today.AddDate(0, 0, 1).Format(time.DateOnly)
	return q, nil
}

// Reported totals exclude unknown attempts. Cached tokens are a subset of input.
type UsageCounts struct {
	Attempts          int64 `json:"attempts"`
	Reported          int64 `json:"reported"`
	Partial           int64 `json:"partial"`
	Unknown           int64 `json:"unknown"`
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
}

type UsagePeriod struct {
	ID             int64      `json:"id"`
	Timezone       string     `json:"timezone"`
	EffectiveFrom  time.Time  `json:"effective_from"`
	EffectiveUntil *time.Time `json:"effective_until"`
}

type UsageRow struct {
	UsageCounts
	PeriodID int64  `json:"period_id"`
	Bucket   string `json:"bucket"`
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id"`
	ModelID  string `json:"model_id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Kind     string `json:"kind"`
}

type UsageReport struct {
	Query      UsageQuery    `json:"query"`
	Totals     UsageCounts   `json:"totals"`
	Periods    []UsagePeriod `json:"periods"`
	Rows       []UsageRow    `json:"rows"`
	HasMore    bool          `json:"has_more"`
	DetailDays int           `json:"detail_days"`
}

type UsagePolicy struct {
	Timezone   string `json:"timezone"`
	DetailDays int    `json:"detail_days"`
}

type UsageStore interface {
	Usage(context.Context, UsageQuery) (UsageReport, error)
	ConfigureUsage(context.Context, UsagePolicy) (UsagePeriod, error)
	PruneUsage(context.Context) error
}

type UsageAuthority interface {
	AuthorizeUsage(context.Context, string, string, string) error
}

func (s *Service) Usage(ctx context.Context, actor string, query UsageQuery) (UsageReport, error) {
	if err := query.Validate(false); err != nil {
		return UsageReport{}, err
	}
	authority, ok := s.Authority.(UsageAuthority)
	if !ok {
		return UsageReport{}, ErrDenied
	}
	if err := authority.AuthorizeUsage(ctx, actor, query.TenantID, query.UserID); err != nil {
		return UsageReport{}, err
	}
	return s.OperatorUsage(ctx, query)
}

// The private transport restricts operator methods to the Management identity.
func (s *Service) OperatorUsage(ctx context.Context, query UsageQuery) (UsageReport, error) {
	if err := query.Validate(true); err != nil {
		return UsageReport{}, err
	}
	store, ok := s.Store.(UsageStore)
	if !ok {
		return UsageReport{}, ErrInvalid
	}
	return store.Usage(ctx, query)
}

func (s *Service) ConfigureUsage(ctx context.Context, policy UsagePolicy) (UsagePeriod, error) {
	store, ok := s.Store.(UsageStore)
	if !ok {
		return UsagePeriod{}, ErrInvalid
	}
	return store.ConfigureUsage(ctx, policy)
}

func (r *Runner) retainUsage(ctx context.Context) {
	store, ok := r.store.(UsageStore)
	if !ok {
		return
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := store.PruneUsage(call); err != nil && ctx.Err() == nil {
			slog.Warn("Usage detail retention delayed", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
