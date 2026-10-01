package management

import (
	"errors"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

var ErrModelUnavailable = errors.New("configured model is unavailable or no longer authorized")

type ModelCallScope struct {
	ActorID, TenantID, AgentID, UserID, FleetID                            string
	ActorAuthorizationEpoch, MembershipExecutionEpoch, AgentExecutionEpoch int64
}

// ModelCandidate freezes routing and permissions without carrying a credential.
type ModelCandidate struct {
	ModelID                 string       `json:"model_id"`
	Provider                string       `json:"provider"`
	Model                   string       `json:"model"`
	Protocol                llm.Protocol `json:"protocol"`
	Endpoint                string       `json:"endpoint"`
	ContextWindow           int          `json:"context_window"`
	MaxOutput               int          `json:"max_output"`
	ModelAuthorizationEpoch int64        `json:"model_authorization_epoch"`
	TenantAccessEpoch       int64        `json:"tenant_access_epoch"`
}

type ModelPlan struct {
	Extensions                     []extensionpolicy.Binding
	Hooks                          []hookpolicy.Declaration
	WorkerDepth                    int
	AgentVersion                   int64
	Instructions, RequestedModelID string
	Candidates                     []ModelCandidate
}

type AgentStatus string

const (
	AgentActive   AgentStatus = "active"
	AgentArchived AgentStatus = "archived"
)

type PeerAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Agent struct {
	Extensions     []extensionpolicy.Binding `json:"extensions"`
	Hooks          []hookpolicy.Declaration  `json:"hooks"`
	Purging        bool                      `json:"purging"`
	WorkerDepth    int                       `json:"worker_depth"`
	ID             string                    `json:"id"`
	FleetID        string                    `json:"fleet_id"`
	Name           string                    `json:"name"`
	Instructions   string                    `json:"instructions"`
	ModelID        string                    `json:"model_id"`
	Status         AgentStatus               `json:"status"`
	Version        int64                     `json:"version"`
	ExecutionEpoch int64                     `json:"-"`
	CreatedAt      time.Time                 `json:"created_at"`
	UpdatedAt      time.Time                 `json:"updated_at"`
}

type AgentConfig struct {
	Hooks        []hookpolicy.Declaration `json:"hooks,omitempty"`
	WorkerDepth  int                      `json:"worker_depth,omitempty"`
	Name         string                   `json:"name"`
	Instructions string                   `json:"instructions"`
	ModelID      string                   `json:"model_id"`
}

func (c AgentConfig) Validate() error {
	if hookpolicy.Validate(c.Hooks) != nil {
		return ErrInvalid
	}
	if c.WorkerDepth < 0 || c.WorkerDepth > 2 {
		return ErrInvalid
	}
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
	DefaultModelID string `json:"default_model_id"`
	Version        int64  `json:"version"`
}

type FleetOverview struct {
	Fleet
	Purged                 bool          `json:"purged"`
	Purging                bool          `json:"purging"`
	PlatformDefaultModelID string        `json:"platform_default_model_id"`
	Owner                  User          `json:"owner"`
	Membership             Membership    `json:"membership"`
	Settings               FleetSettings `json:"settings"`
	Agents                 []Agent       `json:"agents"`
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

func (c AgentConfig) EffectiveWorkerDepth() int {
	if c.WorkerDepth == 0 {
		return 1
	}
	return c.WorkerDepth
}
