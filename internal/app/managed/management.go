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
	"github.com/juex-ai/juex/internal/foundation/maintenance"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/postgres"
)

type ManagementConfig struct {
	Maintenance  maintenance.Gate
	DatabaseURL  string
	MasterKey    string
	PublicURL    string
	SMTP         *maildelivery.Config
	InsecureHTTP bool
	AuditDays    int
}

type Management struct {
	Maintenance maintenance.Gate
	Pool        *pgxpool.Pool
	Directory   *postgres.Directory
	Auth        *postgres.Auth
	Mailer      *maildelivery.SMTP
	Authority   RuntimeAuthority
	auditDays   int
	Purger      *management.Purger
}

func OpenManagement(ctx context.Context, config ManagementConfig) (*Management, error) {
	auditDays, err := auditRetentionDays(config.AuditDays)
	if err != nil {
		return nil, err
	}
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
	return &Management{Maintenance: config.Maintenance, Pool: pool, Directory: d, Auth: auth, Mailer: mailer, Authority: RuntimeAuthority{Directory: d}, auditDays: auditDays}, nil
}

func (m *Management) Close() { m.Pool.Close() }

func (m *Management) RunBackground(ctx context.Context) {
	retained := make(chan struct{})
	go func() {
		defer close(retained)
		runAuditRetention(ctx, "management", m.auditDays, m.Directory.PruneAudit)
	}()
	defer func() { <-retained }()
	done := make(chan struct{})
	go func() { defer close(done); m.runPurges(ctx) }()
	defer func() { <-done }()
	m.runMail(ctx)
}

func (m *Management) runPurges(ctx context.Context) {
	if m.Purger == nil {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 35*time.Second)
		done, err := m.Maintenance.Enter()
		if err == nil {
			_, err = m.Purger.Reconcile(pass)
			done()
		}
		cancel()
		if err != nil && !errors.Is(err, maintenance.ErrDraining) && ctx.Err() == nil {
			slog.Error("resource cleanup step failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Management) runMail(ctx context.Context) {
	if m.Mailer == nil {
		<-ctx.Done()
		return
	}
	for ctx.Err() == nil {
		var worked bool
		done, err := m.Maintenance.Enter()
		if err == nil {
			worked, err = m.Directory.DeliverMail(ctx, m.Mailer)
			done()
		}
		if err != nil && !errors.Is(err, maintenance.ErrDraining) && ctx.Err() == nil {
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
