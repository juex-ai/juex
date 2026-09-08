package agent

import (
	"context"
	"fmt"

	"github.com/juex-ai/juex/internal/framework/runtime"
)

func (a *Agent) ExecuteCommand(ctx context.Context, cmd Command) (CommandResult, error) {
	switch cmd.Kind {
	case CommandKindCompact:
		return a.executeCompactCommand(ctx, cmd, "")
	case CommandKindStatus:
		status, err := a.executionPolicy.Status()
		if err != nil {
			return CommandResult{}, err
		}
		return CommandResult{Name: cmd.Name, Text: status.Text, Status: status.JSON}, nil
	case CommandKindNew:
		if err := a.NewContext(ctx); err != nil {
			return CommandResult{}, err
		}
		status, err := a.executionPolicy.Status()
		if err != nil {
			return CommandResult{}, err
		}
		text := fmt.Sprintf("New context generation: %s", status.GenerationID)
		return CommandResult{Name: cmd.Name, Text: text, Status: status.JSON}, nil
	default:
		return CommandResult{}, fmt.Errorf("unknown command %q", cmd.Name)
	}
}

func (a *Agent) executeCompactCommand(ctx context.Context, cmd Command, admittedTurnID string) (CommandResult, error) {
	var (
		compact runtime.CompactionResult
		err     error
	)
	if admittedTurnID == "" {
		compact, err = a.CompactWithInstructions(ctx, "manual", false, cmd.Args)
	} else {
		compact, err = a.CompactAdmittedWithInstructions(ctx, admittedTurnID, "manual", false, cmd.Args)
	}
	if err != nil {
		return CommandResult{}, err
	}
	text := "No eligible context to compact."
	if compact.MessageID != "" {
		text = fmt.Sprintf("Context compacted: %d -> %d tokens (%d summary chars).",
			compact.TokensBefore, compact.TokensAfter, compact.SummaryChars)
	}
	return CommandResult{Name: cmd.Name, Text: text, Compact: &compact}, nil
}
