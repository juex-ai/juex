package agent

import (
	"encoding/json"

	"github.com/juex-ai/juex/internal/framework/runtime"
)

type Command struct {
	Kind   CommandKind `json:"-"`
	Prompt string      `json:"-"`
	Name   string      `json:"name"`
	Args   string      `json:"args,omitempty"`
}

type CommandResult struct {
	Name    string                    `json:"name"`
	Text    string                    `json:"text"`
	Compact *runtime.CompactionResult `json:"compact,omitempty"`
	Status  json.RawMessage           `json:"status,omitempty"`
}
