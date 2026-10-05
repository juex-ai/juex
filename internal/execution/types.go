// Package execution owns the platform's environments and external operations.
package execution

import (
	"context"
	"encoding/json"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type OwnerScope struct {
	TenantID                 string `json:"tenant_id"`
	UserID                   string `json:"user_id"`
	FleetID                  string `json:"fleet_id"`
	ActorID                  string `json:"actor_id"`
	ActorAuthorizationEpoch  int64  `json:"actor_authorization_epoch"`
	MembershipExecutionEpoch int64  `json:"membership_execution_epoch"`
	RemovalEpoch             int64  `json:"removal_epoch"`
	CanExecute               bool   `json:"can_execute"`
	OwnerEmail               string `json:"owner_email"`
	TenantName               string `json:"tenant_name"`
}

type Scope struct {
	OwnerScope
	AgentID             string `json:"agent_id"`
	AgentExecutionEpoch int64  `json:"agent_execution_epoch"`
}

func (s Scope) SameAuthority(other Scope) bool {
	return s.TenantID == other.TenantID && s.UserID == other.UserID && s.FleetID == other.FleetID && s.AgentID == other.AgentID && s.ActorID == other.ActorID && s.ActorAuthorizationEpoch == other.ActorAuthorizationEpoch && s.MembershipExecutionEpoch == other.MembershipExecutionEpoch && s.RemovalEpoch == other.RemovalEpoch && s.AgentExecutionEpoch == other.AgentExecutionEpoch
}

type Authority interface {
	Owner(context.Context, string, string, string, bool) (OwnerScope, error)
	Agent(context.Context, string, string, string, bool) (Scope, error)
}

type PairRequest struct {
	ID               string                    `json:"id"`
	Name             string                    `json:"name"`
	OS               string                    `json:"os"`
	WorkingDirectory string                    `json:"working_directory"`
	PairSecretHash   string                    `json:"pair_secret_hash"`
	CredentialHash   string                    `json:"credential_hash"`
	Capabilities     []execprotocol.Capability `json:"capabilities"`
}

type Pairing struct {
	ID               string                               `json:"id"`
	EnvironmentID    string                               `json:"environment_id"`
	Name             string                               `json:"name"`
	OS               string                               `json:"os"`
	WorkingDirectory string                               `json:"working_directory"`
	Capabilities     []execprotocol.Capability            `json:"capabilities"`
	State            string                               `json:"state"`
	ExpiresAt        time.Time                            `json:"expires_at"`
	Owner            OwnerScope                           `json:"owner"`
	Grants           map[string][]execprotocol.Capability `json:"grants"`
	AgentEpochs      map[string]int64                     `json:"agent_epochs"`
	ApprovalNonce    string                               `json:"approval_nonce"`
}

type PairConfirmation struct {
	ID            string `json:"id"`
	Secret        string `json:"secret"`
	Credential    string `json:"credential"`
	ApprovalNonce string `json:"approval_nonce"`
}

type Device struct {
	execprotocol.Environment
	TenantID        string                               `json:"tenant_id"`
	UserID          string                               `json:"user_id"`
	FleetID         string                               `json:"fleet_id"`
	RemovalEpoch    int64                                `json:"removal_epoch"`
	Status          string                               `json:"status"`
	Version         int64                                `json:"version"`
	Grants          map[string][]execprotocol.Capability `json:"grants"`
	Ceiling         map[string][]execprotocol.Capability `json:"ceiling"`
	LastSeen        *time.Time                           `json:"last_seen"`
	ConnectionEpoch int64                                `json:"connection_epoch"`
}

type Operation struct {
	Purged          bool                  `json:"-"`
	ID              string                `json:"id"`
	EnvironmentID   string                `json:"environment_id"`
	Scope           Scope                 `json:"scope"`
	Request         execprotocol.Request  `json:"request"`
	State           string                `json:"state"`
	WaitUntil       time.Time             `json:"wait_until"`
	CancelRequested bool                  `json:"cancel_requested"`
	Snapshot        execprotocol.Snapshot `json:"snapshot"`
	ResultCursor    int64                 `json:"result_cursor"`
	Acknowledged    bool                  `json:"acknowledged"`
	CreatedAt       time.Time             `json:"created_at"`
}

type Repository interface {
	DefaultEnvironment(context.Context, Scope) (DefaultEnvironment, error)
	SetDefaultEnvironment(context.Context, Scope, DefaultEnvironment) (DefaultEnvironment, error)
	BeginPair(context.Context, PairRequest) (Pairing, error)
	Pair(context.Context, string, string) (Pairing, error)
	ApprovePair(context.Context, Pairing) (Pairing, error)
	ConfirmPair(context.Context, PairConfirmation) (Device, error)
	Devices(context.Context, OwnerScope) ([]Device, error)
	Device(context.Context, string) (Device, error)
	SetGrants(context.Context, string, int64, map[string][]execprotocol.Capability, string) (Device, error)
	Revoke(context.Context, string, string) error
	AuthenticateDevice(context.Context, string) (Device, error)
	Connect(context.Context, string, string) (Device, error)
	Touch(context.Context, string, int64, bool) error
	Enqueue(context.Context, Device, Scope, execprotocol.Request, time.Duration, bool) (Operation, error)
	Operation(context.Context, string, string, int64, int) (Operation, error)
	Pending(context.Context, string, int) ([]Operation, error)
	Dispatch(context.Context, string, int64, string) (Operation, error)
	CancelOperation(context.Context, string, string) error
	CancelPreparedOperation(context.Context, Scope, string, string) (execprotocol.State, error)
	ExtendWait(context.Context, string, string, time.Duration) error
	Observe(context.Context, string, int64, execprotocol.Snapshot) error
	Settle(context.Context, string, int64, string, execprotocol.State, string) error
	Acknowledge(context.Context, string, int64, string) error
	Unsettled(context.Context, int) ([]Operation, error)
	ExpireWaiting(context.Context) error
	Events(context.Context, int) ([]execprotocol.Event, error)
	AcknowledgeEvents(context.Context, []string) error
	AcknowledgeOutput(context.Context, string, string, int64) error
	ExpirePresence(context.Context) error
}

func CanonicalHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return Digest(string(encoded)), nil
}
