package management

import (
	"bytes"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/processenv"
)

// Values are write-only. Removing a key restores its lower-layer declaration;
// an empty string is an explicit value, not an inheritance sentinel.
type ProcessEnvironmentChange struct {
	Version          int64             `json:"version"`
	Set              map[string]string `json:"set"`
	Remove           []string          `json:"remove"`
	EnvironmentID    string            `json:"environment_id,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
}

func (c *ProcessEnvironmentChange) UnmarshalJSON(data []byte) error {
	var wire struct {
		Version          int64              `json:"version"`
		Set              map[string]*string `json:"set"`
		Remove           []string           `json:"remove"`
		EnvironmentID    string             `json:"environment_id"`
		WorkingDirectory string             `json:"working_directory"`
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	values := map[string]string{}
	for name, value := range wire.Set {
		if value == nil {
			return ErrInvalid
		}
		values[name] = *value
	}
	*c = ProcessEnvironmentChange{Version: wire.Version, Set: values, Remove: wire.Remove, EnvironmentID: wire.EnvironmentID, WorkingDirectory: wire.WorkingDirectory}
	return nil
}

func (c ProcessEnvironmentChange) Validate() error {
	if c.Version < 0 || processenv.Validate(c.Set) != nil || len(c.Remove) > 64 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, name := range c.Remove {
		if !processenv.ValidName(name) || seen[name] {
			return ErrInvalid
		}
		if _, ok := c.Set[name]; ok {
			return ErrInvalid
		}
		seen[name] = true
	}
	return nil
}

type ProcessEnvironmentLayer struct {
	Version          int64    `json:"version"`
	Keys             []string `json:"keys"`
	EnvironmentID    string   `json:"environment_id,omitempty"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
}
type ProcessEnvironmentVariable struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}
type ProcessEnvironmentView struct {
	Layers         map[string]ProcessEnvironmentLayer `json:"layers"`
	Effective      []ProcessEnvironmentVariable       `json:"effective"`
	TenantWritable bool                               `json:"tenant_writable"`
}

// ProcessEnvironmentAccess is accepted only from the authenticated Execution
// service after its operation/device admission. It is not a public API input.
type ProcessEnvironmentAccess struct {
	Scope            ModelCallScope `json:"scope"`
	EnvironmentID    string         `json:"environment_id"`
	WorkingDirectory string         `json:"working_directory"`
}
