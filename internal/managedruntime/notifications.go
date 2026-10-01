package managedruntime

import (
	"context"
	"errors"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
)

type NotificationDelivery struct {
	Event      application.Event
	LeaseEpoch int64
}

type NotificationStore interface {
	ClaimNotification(context.Context) (NotificationDelivery, error)
	FinishNotification(context.Context, NotificationDelivery, bool) error
}

type NotificationGateway interface {
	RecordNotification(context.Context, application.Event) error
}

func (r *Runner) deliverNotifications(ctx context.Context) {
	store, ok := r.store.(NotificationStore)
	if !ok || r.config.Notifications == nil {
		return
	}
	observationLoop(ctx, "Runtime notification delivery delayed", func(ctx context.Context) error {
		d, err := store.ClaimNotification(ctx)
		if err != nil {
			return err
		}
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = r.config.Notifications.RecordNotification(call, d.Event)
		cancel()
		finished := err == nil || errors.Is(err, application.ErrDenied) || errors.Is(err, ErrDenied)
		return errors.Join(err, store.FinishNotification(ctx, d, finished))
	})
}
