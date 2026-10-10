package management

import "github.com/juex-ai/juex/internal/foundation/extensionpolicy"

// ExtensionInspectionAuthority is supplied by the Execution receipt adapter,
// never by a public configuration body.
type ExtensionInspectionAuthority struct {
	ActorID                                 string
	ActorEpoch, MembershipEpoch, AgentEpoch int64
}

type ExtensionInspectionRequest struct {
	SourceKind    string `json:"source_kind,omitempty"`
	RequestID     string `json:"request_id"`
	EnvironmentID string `json:"environment_id"`
	Directory     string `json:"directory"`
}

type ExtensionInspection struct {
	SourceKind           string                   `json:"source_kind,omitempty"`
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
