package managed

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

func openDatabase(ctx context.Context, address string) (*pgxpool.Pool, error) {
	if address == "" {
		return nil, errors.New("JUEX_DATABASE_URL is required")
	}
	config, err := pgxpool.ParseConfig(address)
	if err != nil {
		return nil, errors.New("invalid JUEX_DATABASE_URL")
	}
	config.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("could not create PostgreSQL pool")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("PostgreSQL unavailable; check database address and credentials")
	}
	return pool, nil
}
