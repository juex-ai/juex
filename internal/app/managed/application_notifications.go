package managed

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/application"
)

type ApplicationNotifications struct {
	Runtime interface {
		RecordApplicationNotice(context.Context, application.Event) error
	}
	Management interface {
		RecordNotification(context.Context, application.Event) error
	}
}

func (a ApplicationNotifications) Main(ctx context.Context, event application.Event) error {
	return memoryWorkerError(a.Runtime.RecordApplicationNotice(ctx, event))
}
func (a ApplicationNotifications) Inbox(ctx context.Context, event application.Event) error {
	return a.Management.RecordNotification(ctx, event)
}
