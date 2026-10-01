package execprotocol

import (
	"encoding/json"
	"strings"
)

// HookCommand is an argv operation. Input is data on stdin, never shell source.
type HookCommand struct {
	Extension        *ExtensionContext `json:"extension,omitempty"`
	Command          []string          `json:"command"`
	Input            json.RawMessage   `json:"input"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	TimeoutMS        int               `json:"timeout_ms"`
	MaxOutputBytes   int               `json:"max_output_bytes"`
}

func (h HookCommand) Validate() error {
	if h.Extension.Validate() != nil || len(h.Command) == 0 || len(h.Command) > 64 || strings.TrimSpace(h.Command[0]) == "" || h.TimeoutMS < 1 || h.TimeoutMS > 300000 || h.MaxOutputBytes < 1 || h.MaxOutputBytes > 64<<10 || len(h.Input) > 256<<10 || !json.Valid(h.Input) || len(h.Environment) > 64 {
		return ErrInvalid
	}
	size := 0
	for _, arg := range h.Command {
		size += len(arg)
		if strings.ContainsRune(arg, 0) {
			return ErrInvalid
		}
	}
	if size > 128<<10 {
		return ErrInvalid
	}
	return nil
}

type HookOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Overflow bool   `json:"overflow"`
}
