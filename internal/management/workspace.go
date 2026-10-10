package management

import (
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"time"
)

type WorkspaceReadRequest struct {
	RequestID     string                      `json:"request_id"`
	EnvironmentID string                      `json:"environment_id"`
	Directory     string                      `json:"directory"`
	Query         execprotocol.WorkspaceQuery `json:"query"`
}

type WorkspaceReadReceipt struct {
	AuthorizationVersion int64                          `json:"authorization_version"`
	ReadStartedAt        time.Time                      `json:"read_started_at"`
	OperationID          string                         `json:"operation_id"`
	EnvironmentID        string                         `json:"environment_id"`
	Directory            string                         `json:"directory"`
	Query                execprotocol.WorkspaceQuery    `json:"query"`
	State                string                         `json:"state"`
	Error                string                         `json:"error"`
	Listing              *execprotocol.WorkspaceListing `json:"listing,omitempty"`
}
