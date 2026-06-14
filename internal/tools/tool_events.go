package tools

import "context"

type OutputDelta struct {
	Tool      string
	ToolUseID string
	SessionID string
	ChunkID   int
	Stream    string
	Text      string
	Truncated bool
}

type OutputEmitter func(OutputDelta)

type ToolCallEvents struct {
	Tool      string
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
