package management

import "github.com/juex-ai/juex/internal/foundation/extensionpolicy"

type ExtensionInspectionRequest struct {
	RequestID     string `json:"request_id"`
	EnvironmentID string `json:"environment_id"`
	Directory     string `json:"directory"`
}

type ExtensionInspection struct {
	OperationID          string                   `json:"operation_id"`
	EnvironmentID        string                   `json:"environment_id"`
	AuthorizationVersion int64                    `json:"authorization_version"`
	Directory            string                   `json:"directory"`
	State                string                   `json:"state"`
	Error                string                   `json:"error,omitempty"`
	Catalog              *extensionpolicy.Catalog `json:"catalog,omitempty"`
}

type ExtensionChange struct {
	Version       int64    `json:"version"`
	Enabled       bool     `json:"enabled"`
	Remove        bool     `json:"remove,omitempty"`
	InspectionID  string   `json:"inspection_id,omitempty"`
	EnvironmentID string   `json:"environment_id,omitempty"`
	Resources     []string `json:"resources"`
}
