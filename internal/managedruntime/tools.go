package managedruntime

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/agentcontrol"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

// ToolWork is a durable delivery, independent of an Agent Activation. Its ID
// becomes the external operation ID before any dispatch can occur.
type ToolWork struct {
	FrozenAgentManagement                      bool
	AgentControl                               *agentcontrol.Action
	AgentLifecycle                             *AgentLifecycleReceipt
	InputScopeID                               string
	FrozenCapabilities                         agentpolicy.Policy
	Extensions                                 []extensionpolicy.Binding
	DeferredResult                             *ToolOutcome
	HookContext                                string
	ID, TurnID, ThreadID, State, EnvironmentID string
	Scope                                      Scope
	Call                                       llm.Block
	Request                                    execprotocol.Request
	LeaseEpoch, WakeVersion                    int64
	Cancelled, OperationLive                   bool
}

type ToolOperation struct {
	State           string
	CancelRequested bool
	WaitUntil       time.Time
	Snapshot        execprotocol.Snapshot
}

type ToolGateway interface {
	Environments(context.Context, Scope) ([]execprotocol.Environment, error)
	Submit(context.Context, Scope, string, execprotocol.Request) (ToolOperation, error)
	Operation(context.Context, Scope, string, string, int64) (ToolOperation, error)
	Cancel(context.Context, Scope, string, string) error
	CancelPrepared(context.Context, Scope, string, string) (execprotocol.State, error)
	Events(context.Context, int) ([]execprotocol.Event, error)
	AcknowledgeEvents(context.Context, []string) error
	AcknowledgeOutput(context.Context, Scope, string, string, int64) error
}

type ToolStore interface {
	ClaimTool(context.Context, string) (ToolWork, error)
	ReleaseToolClaims(context.Context, string) error
	PrepareTool(context.Context, ToolWork, string, execprotocol.Request) error
	FinishTool(context.Context, ToolWork, ToolOutcome) error
	ReceiveExecutionEvents(context.Context, []execprotocol.Event) error
}

type ToolOutcome struct {
	ResultFact             *llm.ResultFact
	WaitReason             string
	State                  string
	Content                string
	IsError, OperationLive bool
	RetryAfter             time.Duration
}

func (s Scope) ExecutionFence() execprotocol.AuthorityFence {
	return execprotocol.AuthorityFence{ActorEpoch: s.ActorAuthorizationEpoch, MembershipEpoch: s.MembershipExecutionEpoch, AgentEpoch: s.AgentExecutionEpoch}
}

func toolResult(call llm.Block, value any, failed bool) ToolOutcome {
	encoded, _ := json.Marshal(value)
	return ToolOutcome{State: "ready", Content: string(encoded), IsError: failed}
}
