package managedruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

type PeerAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AgentDirectory lists descriptive identities only, never another Agent's
// conversations. Message admission separately authorizes both participants.
type AgentDirectory interface {
	Peers(context.Context, Scope) ([]PeerAgent, error)
}

type ThreadAction struct {
	Kind      string `json:"kind"`
	ThreadID  string `json:"thread_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Query     string `json:"query,omitempty"`
	Subscribe bool   `json:"subscribe,omitempty"`
}

type ThreadDelivery struct {
	ID, ThreadID, SubscriptionID string
	Scope                        Scope
	Generation, LeaseEpoch       int64
	Source                       InputSource
	Text                         string
}

type CollaborationStore interface {
	ApplyThreadAction(context.Context, ToolWork, ThreadAction, Scope) (json.RawMessage, error)
	Threads(context.Context, Scope) ([]Thread, error)
	ClaimThreadDelivery(context.Context, string) (ThreadDelivery, error)
	FinishThreadDelivery(context.Context, ThreadDelivery, bool) error
}

func collaborationTools(peers bool) []llm.ToolSpec {
	str := map[string]any{"type": "string"}
	tool := func(name, description string, properties map[string]any, required ...string) llm.ToolSpec {
		if required == nil {
			required = []string{}
		}
		return llm.ToolSpec{Name: name, Description: description, Schema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
	}
	// Some schema adapters add a reason to otherwise empty objects. Give these
	// read-only calls an explicit, bounded purpose field instead of accepting
	// arbitrary additional parameters throughout the collaboration API.
	listProperties := map[string]any{"reason": map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "Brief purpose of this read-only lookup."}}
	result := []llm.ToolSpec{
		tool("thread_create", "Delegate an explicit task to an independent Worker with fresh context. Creation, initial input and optional result subscription are atomic. Stopping this Thread does not stop the Worker. Default maximum nesting is one Worker level.", map[string]any{"name": str, "query": str, "subscribe": map[string]any{"type": "boolean"}}, "name", "query"),
		tool("thread_list", "List this Agent's Thread identities, parents and states. Other Agents' raw conversations are private.", listProperties, "reason"),
		tool("thread_status", "Read this Agent's Thread state without waking it or polling a model.", map[string]any{"thread_id": str}, "thread_id"),
		tool("thread_send", "Send an explicit message to another active Thread of this Agent. Acceptance is durable and independent of the sender's later cancellation.", map[string]any{"thread_id": str, "query": str}, "thread_id", "query"),
		tool("thread_subscribe", "Subscribe this Thread to a Worker's future terminal Turn results. Delivers final text and identifiers, not thinking or raw tool history. Replaces the subscription generation without replaying past results.", map[string]any{"thread_id": str}, "thread_id"),
		tool("thread_unsubscribe", "Stop future Worker result deliveries to this Thread. Already accepted inputs remain independent.", map[string]any{"thread_id": str}, "thread_id"),
		tool("thread_stop", "Cancel another Worker of this Agent, including its outstanding tools. This never cancels its children.", map[string]any{"thread_id": str}, "thread_id"),
		tool("thread_archive", "Archive an idle Worker after its inputs, operations, child Workers and pending result deliveries are settled. History is retained; subscriptions stop.", map[string]any{"thread_id": str}, "thread_id"),
		tool("thread_restore", "Restore an archived Worker. Its parent must be active. Past tasks and subscriptions do not restart.", map[string]any{"thread_id": str}, "thread_id"),
	}
	if peers {
		result = append(result, tool("agent_list", "List other active Agents in this same Fleet. No conversation contents are exposed.", listProperties, "reason"), tool("agent_send", "Send an explicit message to another Agent's Main in this same Fleet. It continues independently once accepted; replies require a separate explicit message.", map[string]any{"agent_id": str, "query": str}, "agent_id", "query"))
	}
	return result
}

func (r toolRunner) collaborationTool(ctx context.Context, work ToolWork) (ToolOutcome, bool) {
	_, hasPeers := r.authority.(AgentDirectory)
	known := false
	for _, spec := range collaborationTools(hasPeers) {
		if spec.Name == work.Call.ToolName {
			known = true
			break
		}
	}
	if !known || r.collaboration == nil {
		return ToolOutcome{}, false
	}
	value, err := r.applyCollaboration(ctx, work)
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrDenied) || errors.Is(err, ErrConflict) {
			return toolResult(work.Call, map[string]string{"error": err.Error()}, true), true
		}
		return retryTool(), true
	}
	return toolResult(work.Call, value, false), true
}

func (r toolRunner) applyCollaboration(ctx context.Context, work ToolWork) (any, error) {
	var args struct {
		Reason    string `json:"reason"`
		ThreadID  string `json:"thread_id"`
		AgentID   string `json:"agent_id"`
		Name      string `json:"name"`
		Query     string `json:"query"`
		Subscribe bool   `json:"subscribe"`
	}
	encoded, err := json.Marshal(work.Call.Input)
	if err != nil {
		return nil, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, work.Call.ToolName, err)
	}
	if len(args.Reason) > 512 || args.Reason != "" && work.Call.ToolName != "agent_list" && work.Call.ToolName != "thread_list" {
		return nil, ErrInvalid
	}
	if (work.Call.ToolName == "agent_list" || work.Call.ToolName == "thread_list") && args.Reason == "" {
		return nil, fmt.Errorf("%w: reason must describe this lookup", ErrInvalid)
	}
	if work.Call.ToolName == "agent_list" {
		return r.authority.(AgentDirectory).Peers(ctx, work.Scope)
	}
	if work.Call.ToolName == "thread_list" || work.Call.ToolName == "thread_status" {
		threads, err := r.collaboration.Threads(ctx, work.Scope)
		if err != nil || work.Call.ToolName == "thread_list" {
			return threads, err
		}
		for _, thread := range threads {
			if thread.ID == args.ThreadID {
				return thread, nil
			}
		}
		return nil, ErrDenied
	}
	command := ThreadAction{Kind: work.Call.ToolName, ThreadID: args.ThreadID, Name: args.Name, Query: args.Query, Subscribe: args.Subscribe}
	target := work.Scope
	if command.Kind == "agent_send" {
		if args.AgentID == "" || args.AgentID == work.Scope.AgentID {
			return nil, ErrInvalid
		}
		var err error
		target, err = r.authority.Authorize(ctx, work.Scope.ActorID, work.Scope.TenantID, args.AgentID, true)
		if err != nil {
			return nil, err
		}
		if target.TenantID != work.Scope.TenantID || target.UserID != work.Scope.UserID || target.FleetID != work.Scope.FleetID {
			return nil, ErrDenied
		}
	}
	return r.collaboration.ApplyThreadAction(ctx, work, command, target)
}

func (r toolRunner) deliverThreadResults(ctx context.Context) {
	holder := rand.Text()
	observationLoop(ctx, "Worker result delivery delayed", func(ctx context.Context) error {
		delivery, err := r.collaboration.ClaimThreadDelivery(ctx, holder)
		if err != nil {
			return err
		}
		fresh, err := r.authority.Authorize(ctx, delivery.Scope.ActorID, delivery.Scope.TenantID, delivery.Scope.AgentID, true)
		if err != nil && !errors.Is(err, ErrDenied) {
			return err
		}
		return r.collaboration.FinishThreadDelivery(ctx, delivery, err == nil && fresh.SameAuthority(delivery.Scope))
	})
}
