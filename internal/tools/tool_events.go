package tools

import (
	"context"

	"github.com/juex-ai/juex/internal/toolevents"
)

type OutputDelta = toolevents.OutputDelta

type OutputEmitter func(OutputDelta)

type ToolCallEvents struct {
	Name      string
	ToolUseID string
	Emit      OutputEmitter
}

type toolCallEventsKey struct{}

func WithToolCallEvents(ctx context.Context, events ToolCallEvents) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, toolCallEventsKey{}, events)
}

func ToolCallEventsFromContext(ctx context.Context) ToolCallEvents {
	if ctx == nil {
		return ToolCallEvents{}
	}
	events, _ := ctx.Value(toolCallEventsKey{}).(ToolCallEvents)
	return events
}
