package maintenance

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Item struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	State string `json:"state"`
}
type Report struct {
	Service string `json:"service"`
	Busy    []Item `json:"busy"`
	Review  []Item `json:"review"`
}
type Query struct {
	Kind, SQL string
	Busy      bool
}

// Inspect uses only the owning service's supplied queries. It never migrates,
// repairs leases, marks results received, or includes private payloads.
func Inspect(ctx context.Context, pool *pgxpool.Pool, service string, queries []Query) (Report, error) {
	report := Report{Service: service, Busy: []Item{}, Review: []Item{}}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return report, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, q := range queries {
		rows, err := tx.Query(ctx, q.SQL)
		if err != nil {
			return report, err
		}
		for rows.Next() {
			item := Item{Kind: q.Kind}
			if err := rows.Scan(&item.ID, &item.State); err != nil {
				rows.Close()
				return report, err
			}
			if q.Busy {
				report.Busy = append(report.Busy, item)
			} else {
				report.Review = append(report.Review, item)
			}
			if len(report.Busy)+len(report.Review) > 100000 {
				rows.Close()
				return report, errors.New("maintenance inventory exceeds 100000 items; narrow or drain work before retrying")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return report, err
		}
	}
	return report, tx.Commit(ctx)
}

func (r Report) Ready() bool { return len(r.Busy) == 0 }
