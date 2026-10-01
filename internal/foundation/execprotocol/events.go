package execprotocol

import (
	"encoding/json"
	"time"
)

// Event is a durable Execution fact delivered at least once to Runtime. The
// receiver persists the ID before acknowledging; IDs are not commit cursors.
type Event struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	TenantID      string          `json:"tenant_id"`
	UserID        string          `json:"user_id"`
	EnvironmentID string          `json:"environment_id"`
	AgentIDs      []string        `json:"agent_ids"`
	OperationID   string          `json:"operation_id"`
	Data          json.RawMessage `json:"data"`
	CreatedAt     time.Time       `json:"created_at"`
}
