package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/memory"
)

type RuntimeMemory interface {
	Status(context.Context, application.Access) (memory.Status, error)
	Search(context.Context, application.Access, mc.Query) (mc.Page, error)
	Facts(context.Context, application.Access, mc.Query) (mc.FactPage, error)
	Read(context.Context, application.Access, mc.ReadRequest) (mc.Entry, error)
	Domains(context.Context, application.Access, mc.DomainRequest) ([]mc.Domain, error)
	Propose(context.Context, application.Scope, string, mc.Proposal, bool, string) (mc.Receipt, error)
	Review(context.Context, application.Scope, memory.Binding) (memory.Review, error)
	Decide(context.Context, application.Scope, memory.Binding, mc.Decision, string) (mc.Receipt, error)
	CancelCommand(context.Context, application.Scope, string) error
}

type RuntimeApplications struct {
	Memory   RuntimeMemory
	Evidence managedruntime.EvidenceStore
}

func appScope(s managedruntime.Scope) application.Scope {
	return application.Scope{Access: application.Access{ActorID: s.ActorID, TenantID: s.TenantID, UserID: s.UserID, AgentID: s.AgentID}, FleetID: s.FleetID, ActorEpoch: s.ActorAuthorizationEpoch, MemberEpoch: s.MembershipExecutionEpoch, AgentEpoch: s.AgentExecutionEpoch, MemberVersion: s.MembershipVersion}
}
func workerScope(s application.Scope) managedruntime.Scope {
	return managedruntime.Scope{ActorID: s.ActorID, TenantID: s.TenantID, UserID: s.UserID, AgentID: s.AgentID, FleetID: s.FleetID, ActorAuthorizationEpoch: s.ActorEpoch, MembershipExecutionEpoch: s.MemberEpoch, AgentExecutionEpoch: s.AgentEpoch, MembershipVersion: s.MemberVersion}
}
func appRuntimeError(err error) error {
	switch {
	case errors.Is(err, application.ErrDenied), errors.Is(err, application.ErrDisabled):
		return managedruntime.ErrDenied
	case errors.Is(err, application.ErrInvalid):
		return managedruntime.ErrInvalid
	case errors.Is(err, application.ErrConflict):
		return managedruntime.ErrConflict
	default:
		return err
	}
}
func binding(j managedruntime.ApplicationJob) memory.Binding {
	return memory.Binding{ReviewID: j.ID, Epoch: j.Epoch, Fence: j.Fence}
}

func (a RuntimeApplications) Check(ctx context.Context, scope managedruntime.Scope, job managedruntime.ApplicationJob) error {
	if job.Application != "memory" || a.Memory == nil {
		return managedruntime.ErrDenied
	}
	_, err := a.Memory.Review(ctx, appScope(scope), binding(job))
	if errors.Is(err, application.ErrConflict) {
		return managedruntime.ErrDenied
	}
	return appRuntimeError(err)
}
func (a RuntimeApplications) Tools(ctx context.Context, scope managedruntime.Scope, job *managedruntime.ApplicationJob) ([]llm.ToolSpec, error) {
	if a.Memory == nil {
		return nil, nil
	}
	if job != nil && job.Application != "memory" {
		return nil, nil
	}
	_, err := a.Memory.Status(ctx, appScope(scope).Access)
	if errors.Is(err, application.ErrDisabled) {
		return nil, nil
	}
	if err != nil {
		return nil, appRuntimeError(err)
	}
	return memory.Tools(job != nil), nil
}
func decodeAppTool(input map[string]any, target any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return managedruntime.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return managedruntime.ErrInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return managedruntime.ErrInvalid
	}
	return nil
}
func (a RuntimeApplications) Call(ctx context.Context, work managedruntime.ToolWork, job *managedruntime.ApplicationJob) (value any, err error) {
	defer func() { err = appRuntimeError(err) }()
	if a.Memory == nil {
		return nil, managedruntime.ErrDenied
	}
	scope := appScope(work.Scope)
	switch work.Call.ToolName {
	case "memory_search", "memory_facts":
		var q mc.Query
		if err = decodeAppTool(work.Call.Input, &q); err != nil {
			return nil, err
		}
		if work.Call.ToolName == "memory_search" {
			return a.Memory.Search(ctx, scope.Access, q)
		}
		return a.Memory.Facts(ctx, scope.Access, q)
	case "memory_read":
		var q mc.ReadRequest
		if err = decodeAppTool(work.Call.Input, &q); err != nil {
			return nil, err
		}
		return a.Memory.Read(ctx, scope.Access, q)
	case "memory_domains":
		var q mc.DomainRequest
		if err = decodeAppTool(work.Call.Input, &q); err != nil {
			return nil, err
		}
		return a.Memory.Domains(ctx, scope.Access, q)
	case "memory_propose":
		if job != nil || a.Evidence == nil {
			return nil, managedruntime.ErrDenied
		}
		var q struct {
			Key    string `json:"key"`
			Text   string `json:"text"`
			Reason string `json:"reason"`
		}
		if err = decodeAppTool(work.Call.Input, &q); err != nil {
			return nil, err
		}
		evidence, err := a.Evidence.ToolEvidence(ctx, work)
		if err != nil {
			return nil, err
		}
		ref := mc.Source{FleetID: scope.FleetID, AgentID: scope.AgentID, ThreadID: work.ThreadID, GenerationID: strconv.FormatInt(evidence.Generation, 10), From: uint64(evidence.Sequence), Through: uint64(evidence.Sequence)}
		proposal := mc.Proposal{Key: q.Key, Text: q.Text, Reason: q.Reason, Sources: []mc.Source{ref}, Evidence: []mc.Evidence{{Source: ref, Kind: "user", Text: evidence.Text, RecordedAt: evidence.RecordedAt}}}
		return a.Memory.Propose(ctx, scope, work.ThreadID, proposal, false, work.ID)
	case "memory_decide":
		if job == nil || job.Application != "memory" {
			return nil, managedruntime.ErrDenied
		}
		var q mc.Decision
		if err = decodeAppTool(work.Call.Input, &q); err != nil {
			return nil, err
		}
		return a.Memory.Decide(ctx, scope, binding(*job), q, work.ID)
	default:
		return nil, managedruntime.ErrInvalid
	}
}
func (a RuntimeApplications) Cancel(ctx context.Context, work managedruntime.ToolWork) error {
	switch work.Call.ToolName {
	case "memory_propose", "memory_decide":
		if a.Memory == nil {
			return managedruntime.ErrDenied
		}
		return appRuntimeError(a.Memory.CancelCommand(ctx, appScope(work.Scope), work.ID))
	default:
		return nil
	}
}
