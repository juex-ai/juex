package managed

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

func auditRetentionDays(days int) (int, error) {
	if days == 0 {
		days = 90
	}
	if days < 1 || days > 3650 {
		return 0, errors.New("audit retention must be between 1 and 3650 days")
	}
	return days, nil
}

func runAuditRetention(ctx context.Context, service string, days int, prune func(context.Context, int) error) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := prune(pass, days)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Error("audit retention delayed", "service", service, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
