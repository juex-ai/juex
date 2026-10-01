package managed

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/calendar"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	applicationrpc "github.com/juex-ai/juex/internal/foundation/application/rpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
)

type CalendarConfig struct {
	DatabaseURL       string
	ManagementAddress string
	RuntimeAddress    string
	Credentials       platformrpc.Credentials
}

type Calendar struct {
	Pool    *pgxpool.Pool
	Service *calendar.Service
}

func OpenCalendar(ctx context.Context, config CalendarConfig) (*Calendar, error) {
	authority, err := applicationrpc.NewAuthority(config.ManagementAddress, config.Credentials)
	if err != nil {
		return nil, err
	}
	pool, err := openDatabase(ctx, config.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err = calendarpg.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	service := &calendar.Service{Repository: calendarpg.New(pool), Authority: authority}
	client, err := runtimerpc.NewClient(config.RuntimeAddress, config.Credentials)
	if err != nil {
		pool.Close()
		return nil, err
	}
	service.Workers = CalendarWorkers{Runtime: client}
	service.Notifier = ApplicationNotifications{Runtime: client, Management: authority}
	return &Calendar{Pool: pool, Service: service}, nil
}

func (m *Calendar) Close() { m.Pool.Close() }
