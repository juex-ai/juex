package agent

import (
	"encoding/json"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

type CommandKind string

const (
	CommandKindStatus  CommandKind = "status"
	CommandKindNew     CommandKind = "new"
	CommandKindCompact CommandKind = "compact"
	CommandKindPrompt  CommandKind = "prompt"
)

type CommandStatus struct {
	JSON         json.RawMessage
	Text         string
	GenerationID string
}

// InputPolicy resolves product capabilities and command presentation outside execution.
type InputPolicy struct {
	CheckInput         func(string, TurnAdmissionRequest) error
	CheckExecution     func(string) error
	ParseCommand       func(string) (Command, bool, error)
	CommandHelp        string
	Status             func() (CommandStatus, error)
	AttachmentWarnings func(int) []TurnWarning
	NewContextInput    llm.Message
}
