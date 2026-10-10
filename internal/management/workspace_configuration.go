package management

import (
	"bytes"
	"io"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"gopkg.in/yaml.v3"
)

type WorkspaceConfigurationPreview struct {
	Receipt       WorkspaceReadReceipt    `json:"receipt"`
	Configuration *WorkspaceConfiguration `json:"configuration,omitempty"`
	Effective     *EffectiveConfiguration `json:"effective,omitempty"`
}

type WorkspaceConfigurationChange struct {
	Version       int64  `json:"version"`
	EnvironmentID string `json:"environment_id"`
	OperationID   string `json:"operation_id"`
	SHA256        string `json:"sha256"`
	Remove        bool   `json:"remove"`
}

// ParseWorkspaceConfiguration imports declarations only. Provider credentials,
// environment expansion and unrelated configuration fields are not accepted.
func ParseWorkspaceConfiguration(data []byte, catalog []Model) (Configuration, error) {
	var document struct {
		ModulePreset string                           `yaml:"module_preset"`
		Models       []string                         `yaml:"models"`
		Modules      map[agentpolicy.Capability]*bool `yaml:"modules"`
	}
	if len(data) > 64<<10 || !utf8.Valid(data) {
		return Configuration{}, ErrInvalid
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if decoder.Decode(&document) != nil {
		return Configuration{}, ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Configuration{}, ErrInvalid
	}
	result := Configuration{ModulePreset: document.ModulePreset}
	if document.Models != nil {
		result.Models = []string{}
		for _, reference := range document.Models {
			id := ""
			for _, model := range catalog {
				if reference == model.ID || reference == model.Provider+":"+model.Name {
					if id != "" && id != model.ID {
						return Configuration{}, ErrInvalid
					}
					id = model.ID
				}
			}
			if id == "" {
				return Configuration{}, ErrInvalid
			}
			result.Models = append(result.Models, id)
		}
	}
	if document.Modules != nil {
		result.Modules = map[agentpolicy.Capability]bool{}
		for key, value := range document.Modules {
			if value == nil {
				return Configuration{}, ErrInvalid
			}
			result.Modules[key] = *value
		}
	}
	return result, result.Validate()
}
