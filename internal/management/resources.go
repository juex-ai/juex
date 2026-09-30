package management

import (
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

type AgentStatus string

const (
	AgentActive   AgentStatus = "active"
	AgentArchived AgentStatus = "archived"
)

type Agent struct {
	ID             string      `json:"id"`
	FleetID        string      `json:"fleet_id"`
	Name           string      `json:"name"`
	Instructions   string      `json:"instructions"`
	ModelID        string      `json:"model_id"`
	Status         AgentStatus `json:"status"`
	Version        int64       `json:"version"`
	ExecutionEpoch int64       `json:"-"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type AgentConfig struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	ModelID      string `json:"model_id"`
}

func (c AgentConfig) Validate() error {
	if strings.TrimSpace(c.Name) == "" || len([]rune(c.Name)) > 100 || len(c.Instructions) > 64<<10 {
		return ErrInvalid
	}
	return nil
}

type Model struct {
	ID            string       `json:"id"`
	Provider      string       `json:"provider"`
	Name          string       `json:"name"`
	Protocol      llm.Protocol `json:"protocol"`
	ContextWindow int          `json:"context_window"`
	MaxOutput     int          `json:"max_output"`
	Enabled       bool         `json:"enabled"`
}

// ModelConfiguration is accepted only by the deployment operator. APIKey and
// endpoint are intentionally absent from the public Model read model.
type ModelConfiguration struct {
	Provider, Name, Endpoint, APIKey string
	Protocol                         llm.Protocol
	ContextWindow, MaxOutput         int
	Enabled                          bool
}

type FleetSettings struct {
	DefaultModelID  string `json:"default_model_id"`
	MemoryEnabled   bool   `json:"memory_enabled"`
	CalendarEnabled bool   `json:"calendar_enabled"`
	Version         int64  `json:"version"`
}

type FleetOverview struct {
	Fleet
	Owner      User          `json:"owner"`
	Membership Membership    `json:"membership"`
	Settings   FleetSettings `json:"settings"`
	Agents     []Agent       `json:"agents"`
}

// FleetAuthority is a current authorization snapshot for private services.
// RemovalEpoch prevents retained device grants from reviving after rejoining.
type FleetAuthority struct {
	Fleet                    Fleet         `json:"fleet"`
	ActorID                  string        `json:"actor_id"`
	ActorAuthorizationEpoch  int64         `json:"actor_authorization_epoch"`
	MembershipVersion        int64         `json:"membership_version"`
	MembershipExecutionEpoch int64         `json:"membership_execution_epoch"`
	RemovalEpoch             int64         `json:"removal_epoch"`
	CanExecute               bool          `json:"can_execute"`
	OwnerEmail               string        `json:"owner_email"`
	TenantName               string        `json:"tenant_name"`
	Settings                 FleetSettings `json:"settings"`
}

// AgentAuthority is a server-derived snapshot for service admission. Services
// recheck authority before each execution; it is not a durable grant or token.
type AgentAuthority struct {
	Agent                    Agent
	Fleet                    Fleet
	ActorID                  string
	ActorAuthorizationEpoch  int64
	MembershipVersion        int64
	MembershipExecutionEpoch int64
	ModelID                  string
	CanExecute               bool
}

type AgentDetail struct {
	Agent            Agent  `json:"agent"`
	OwnerID          string `json:"owner_id"`
	CanExecute       bool   `json:"can_execute"`
	EffectiveModelID string `json:"effective_model_id"`
}

type ResolvedModel struct {
	Model    Model
	Endpoint string
	APIKey   string
}
