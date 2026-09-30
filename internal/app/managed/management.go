// Package managed composes the platform services without a personal Home.
package managed

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/maildelivery"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/postgres"
)

type ManagementConfig struct {
	DatabaseURL  string
	MasterKey    string
	PublicURL    string
	SMTP         *maildelivery.Config
	InsecureHTTP bool
}

type Management struct {
	Pool      *pgxpool.Pool
	Directory *postgres.Directory
	Auth      *postgres.Auth
	Mailer    *maildelivery.SMTP
	Authority RuntimeAuthority
}

func OpenManagement(ctx context.Context, config ManagementConfig) (*Management, error) {
	origin, err := management.PublicOrigin(config.PublicURL, config.InsecureHTTP)
	if err != nil {
		return nil, err
	}
	var mailer *maildelivery.SMTP
	if config.SMTP != nil {
		mailer, err = maildelivery.NewSMTP(*config.SMTP)
		if err != nil {
			return nil, err
		}
	}
	if config.DatabaseURL == "" {
		return nil, errors.New("JUEX_DATABASE_URL is required")
	}
	key, err := hex.DecodeString(config.MasterKey)
	if err != nil {
		return nil, errors.New("JUEX_MASTER_KEY must be 64 hexadecimal characters")
	}
	box, err := secrets.New(key)
	if err != nil {
		return nil, err
	}
	pool, err := openDatabase(ctx, config.DatabaseURL)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			pool.Close()
		}
	}()
	if err := postgres.Migrate(ctx, pool); err != nil {
		return nil, err
	}
	d := postgres.NewDirectory(pool, postgres.Config{Secrets: box, PublicURL: origin, MailEnabled: mailer != nil})
	auth, err := postgres.NewAuth(d)
	if err != nil {
		return nil, err
	}
	ok = true
	return &Management{Pool: pool, Directory: d, Auth: auth, Mailer: mailer, Authority: RuntimeAuthority{Directory: d}}, nil
}

func (m *Management) Close() { m.Pool.Close() }

func (m *Management) RunBackground(ctx context.Context) {
	m.runMail(ctx)
}

func (m *Management) runMail(ctx context.Context) {
	if m.Mailer == nil {
		<-ctx.Done()
		return
	}
	for ctx.Err() == nil {
		worked, err := m.Directory.DeliverMail(ctx, m.Mailer)
		if err != nil && ctx.Err() == nil {
			slog.Error("management mail queue unavailable", "error", err)
		}
		if worked && err == nil {
			continue
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
