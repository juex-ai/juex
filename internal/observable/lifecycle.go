package observable

import (
	"context"
	"fmt"
	"time"
)

type DeliveryFunc func(context.Context, ObservationRecord) (DeliveryOutcome, error)

type DeliveryOutcome struct {
	State          string
	TargetSession  string
	PendingInputID string
	DeliveredAt    time.Time
	Error          string
}

func (fn DeliveryFunc) Deliver(ctx context.Context, record ObservationRecord) (DeliveryOutcome, error) {
	if fn == nil {
		return DeliveryOutcome{}, nil
	}
	return fn(ctx, record)
}

func (o DeliveryOutcome) normalized(now func() time.Time) (DeliveryOutcome, error) {
	switch o.State {
	case "":
		return o, nil
	case ObservationStateQueued:
		if o.PendingInputID == "" {
			return DeliveryOutcome{}, fmt.Errorf("observable: queued delivery outcome requires pending input id")
		}
	case ObservationStateDelivered:
		if o.DeliveredAt.IsZero() {
			if now == nil {
				now = time.Now
			}
			o.DeliveredAt = now().UTC()
		}
	case ObservationStateDropped:
		if o.Error == "" {
			o.Error = "observation delivery dropped"
		}
	default:
		return DeliveryOutcome{}, fmt.Errorf("observable: invalid delivery outcome state %q", o.State)
	}
	return o, nil
}

func (o DeliveryOutcome) apply(record ObservationRecord) ObservationRecord {
	if o.State != "" {
		record.State = o.State
	}
	if o.TargetSession != "" {
		record.TargetSession = o.TargetSession
	}
	if o.PendingInputID != "" {
		record.PendingInputID = o.PendingInputID
	}
	if !o.DeliveredAt.IsZero() {
		record.DeliveredAt = o.DeliveredAt
	}
	if o.Error != "" {
		record.Error = o.Error
	}
	return record
}
