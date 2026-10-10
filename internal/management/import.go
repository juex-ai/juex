package management

import (
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/processenv"
)

// ErrImportProofVersion requires explicit recovery against the retained v1 payload.
var ErrImportProofVersion = errors.New("retained import uses proof version 1")

// ImportedAgent binds a source identity to initial configuration. It carries
// neither old execution epochs nor a caller-selected target identity.
type ImportedAgent struct {
	SourceAgentID string      `json:"source_agent_id"`
	Config        AgentConfig `json:"config"`
	// Private offline payload, never exposed by HTTP or a model tool. These
	// initial declarations are covered by the same immutable import receipt.
	Environment     map[string]string `json:"environment,omitempty"`
	AgentManagement bool              `json:"agent_management,omitempty"`
}

// AgentsImport is an offline creation request for an empty retained Fleet.
// ExpectedFleetID prevents a retry from recreating data after purge and rejoin.
// Resource installation and other service imports have separate owners.
type AgentsImport struct {
	ExpectedFleetID string          `json:"expected_fleet_id"`
	Source          string          `json:"source"`
	SourceSHA256    string          `json:"source_sha256"`
	Agents          []ImportedAgent `json:"agents"`
}

func (v AgentsImport) Validate() error {
	id, err := uuid.Parse(v.ExpectedFleetID)
	if err != nil || id == uuid.Nil || id.String() != v.ExpectedFleetID {
		return ErrInvalid
	}
	hash, err := hex.DecodeString(v.SourceSHA256)
	if err != nil || len(hash) != 32 || strings.ToLower(v.SourceSHA256) != v.SourceSHA256 || strings.TrimSpace(v.Source) == "" || !utf8.ValidString(v.Source) || len(v.Source) > 512 || len(v.Agents) == 0 || len(v.Agents) > 1000 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, item := range v.Agents {
		if strings.TrimSpace(item.SourceAgentID) != item.SourceAgentID || item.SourceAgentID == "" || len(item.SourceAgentID) > 128 || seen[item.SourceAgentID] || item.Config.Validate() != nil || processenv.Validate(item.Environment) != nil || !validImportedText(item) {
			return ErrInvalid
		}
		seen[item.SourceAgentID] = true

	}
	return nil
}

// JSON replaces invalid UTF-8. Reject it before hashing or copying: replacement
// could merge distinct source identities or change instructions and commands.
func validImportedText(item ImportedAgent) bool {
	text := []string{item.SourceAgentID, item.Config.Name, item.Config.Instructions}
	for _, hook := range item.Config.Hooks {
		text = append(text, hook.Source, hook.WorkingDirectory)
		text = append(text, hook.Command...)
		text = append(text, hook.Tools...)
		if hook.Extension != nil {
			text = append(text, hook.Extension.Directory)
		}
		for key, value := range hook.Environment {
			text = append(text, key, value)
		}
	}
	for _, value := range text {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return true
}
