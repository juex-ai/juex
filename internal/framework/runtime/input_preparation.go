package runtime

import (
	"context"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func (e *Engine) prepareAdmittedInput(ctx context.Context, turnID, inputID string, message llm.Message) error {
	if inputID == "" {
		if queue := e.currentPendingInputQueue(); queue != nil {
			records, err := queue.Records()
			if err != nil {
				return err
			}
			for id, record := range records {
				if record.MessageID == message.ID {
					inputID = id
					break
				}
			}
		}
	}
	if inputID == "" {
		inputID = message.ID
	}
	return runtimemodule.PrepareInputs(ctx, runtimemodule.InputPreparationRequest{Runtime: e.policyRuntimeContext(), Thread: e.policyThreadContext(), TurnID: turnID, InputID: inputID, PreparationID: turnID + "/" + inputID, Message: message, Budget: 500 * time.Millisecond, Observer: e.policyObserver(turnID)}, e.policySets()...)
}
