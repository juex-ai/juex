package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

const usageSums = `COALESCE(sum(attempts),0)::bigint,COALESCE(sum(reported),0)::bigint,COALESCE(sum(partial),0)::bigint,COALESCE(sum(unknown),0)::bigint,COALESCE(sum(input_tokens),0)::bigint,COALESCE(sum(output_tokens),0)::bigint,COALESCE(sum(cached_input_tokens),0)::bigint`
const usageFilter = ` FROM runtime.usage_daily WHERE ($1='' OR tenant_id=NULLIF($1,'')::uuid) AND ($2='' OR user_id=NULLIF($2,'')::uuid) AND day>=$3::date AND day<$4::date`

func usageTargets(v *managedruntime.UsageCounts) []any {
	return []any{&v.Attempts, &v.Reported, &v.Partial, &v.Unknown, &v.InputTokens, &v.OutputTokens, &v.CachedInputTokens}
}

func (s *Store) Usage(ctx context.Context, q managedruntime.UsageQuery) (managedruntime.UsageReport, error) {
	result := managedruntime.UsageReport{Query: q, Rows: []managedruntime.UsageRow{}, Periods: []managedruntime.UsagePeriod{}}
	if err := q.Validate(true); err != nil {
		return result, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT detail_days,clock_timestamp() FROM runtime.usage_policy WHERE id=1`).Scan(&result.DetailDays, &now); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT id,timezone,effective_from,effective_until FROM runtime.usage_periods ORDER BY id`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var p managedruntime.UsagePeriod
		if err := rows.Scan(&p.ID, &p.Timezone, &p.EffectiveFrom, &p.EffectiveUntil); err != nil {
			rows.Close()
			return result, err
		}
		result.Periods = append(result.Periods, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	q, err = q.WithDefaultRange(now, result.Periods[len(result.Periods)-1].Timezone)
	if err != nil {
		return result, err
	}
	result.Query = q
	args := []any{q.TenantID, q.UserID, q.From, q.Until}
	if err := tx.QueryRow(ctx, `SELECT `+usageSums+usageFilter, args...).Scan(usageTargets(&result.Totals)...); err != nil {
		return result, err
	}
	result.Totals.TotalTokens = result.Totals.InputTokens + result.Totals.OutputTokens
	args = append(args, q.Group, q.Limit+1, q.Offset)
	rows, err = tx.Query(ctx, `SELECT period_id,to_char(date_trunc($5,day),'YYYY-MM-DD') AS bucket,tenant_id,user_id,model_id,provider,model,kind,`+usageSums+usageFilter+` GROUP BY period_id,bucket,tenant_id,user_id,model_id,provider,model,kind ORDER BY bucket DESC,period_id,tenant_id,user_id,provider,model,model_id,kind LIMIT $6 OFFSET $7`, args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var v managedruntime.UsageRow
		targets := []any{&v.PeriodID, &v.Bucket, &v.TenantID, &v.UserID, &v.ModelID, &v.Provider, &v.Model, &v.Kind}
		if err := rows.Scan(append(targets, usageTargets(&v.UsageCounts)...)...); err != nil {
			rows.Close()
			return result, err
		}
		v.TotalTokens = v.InputTokens + v.OutputTokens
		result.Rows = append(result.Rows, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Rows) > q.Limit {
		result.HasMore = true
		result.Rows = result.Rows[:q.Limit]
	}
	return result, tx.Commit(ctx)
}

func (s *Store) ConfigureUsage(ctx context.Context, policy managedruntime.UsagePolicy) (managedruntime.UsagePeriod, error) {
	var period managedruntime.UsagePeriod
	if len(policy.Timezone) > 100 || policy.Timezone == "" || policy.Timezone == "Local" || policy.DetailDays < 1 || policy.DetailDays > 3650 {
		return period, managedruntime.ErrInvalid
	}
	if _, err := time.LoadLocation(policy.Timezone); err != nil {
		return period, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return period, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.runtime.usage_policy'))`); err != nil {
		return period, err
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_timezone_names WHERE name=$1)`, policy.Timezone).Scan(&valid); err != nil {
		return period, err
	}
	if !valid {
		return period, managedruntime.ErrInvalid
	}
	if err := tx.QueryRow(ctx, `SELECT id,timezone,effective_from,effective_until FROM runtime.usage_periods WHERE effective_until IS NULL`).Scan(&period.ID, &period.Timezone, &period.EffectiveFrom, &period.EffectiveUntil); err != nil {
		return period, err
	}
	if period.Timezone != policy.Timezone {
		var boundary time.Time
		if err := tx.QueryRow(ctx, `UPDATE runtime.usage_periods SET effective_until=clock_timestamp() WHERE id=$1 RETURNING effective_until`, period.ID).Scan(&boundary); err != nil {
			return period, err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO runtime.usage_periods(timezone,effective_from) VALUES($1,$2) RETURNING id,timezone,effective_from,effective_until`, policy.Timezone, boundary).Scan(&period.ID, &period.Timezone, &period.EffectiveFrom, &period.EffectiveUntil); err != nil {
			return period, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.usage_policy SET detail_days=$1 WHERE id=1`, policy.DetailDays); err != nil {
		return period, err
	}
	return period, tx.Commit(ctx)
}

func (s *Store) PruneUsage(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM runtime.usage_records WHERE attempt_id IN (SELECT attempt_id FROM runtime.usage_records WHERE settled AND started_at<clock_timestamp()-make_interval(days=>(SELECT detail_days FROM runtime.usage_policy WHERE id=1)) ORDER BY started_at LIMIT 10000)`)
	return err
}
