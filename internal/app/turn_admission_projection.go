package app

import (
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/runtime"
)

type turnAdmissionStatusProjection struct {
	status       *runtime.StatusStore
	completeTurn func(string)
}

func (p turnAdmissionStatusProjection) Publish(event events.Event) {
	if p.completeTurn != nil && isTerminalTurnEvent(event.Type) {
		p.completeTurn(event.TurnID)
	}
	if p.status != nil {
		p.status.Publish(event)
	}
}

func isTerminalTurnEvent(eventType string) bool {
	return eventType == "turn.completed" || eventType == "turn.errored"
}
