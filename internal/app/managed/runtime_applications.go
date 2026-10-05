package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/memory"
)

type RuntimeMemory interface {
	Recall(context.Context, application.Access, string) (memory.Recall, error)
	Maintain(context.Context, application.Scope, string, string, string) (mc.Receipt, error)
	Contribute(context.Context, application.Scope, memory.Contribution) error
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

func (a RuntimeApplications) NoticeValid(ctx context.Context, event application.Event) error {
	if event.Application == "calendar" && a.Calendar != nil {
		status, err := a.Calendar.Status(ctx, event.Scope.Access)
		if err != nil {
			return appRuntimeError(err)
		}
		if !status.Enabled || status.Epoch != event.Epoch {
			return managedruntime.ErrDenied
		}
		return nil
	}
	if event.Application != "memory" || a.Memory == nil {
		return managedruntime.ErrDenied
	}
	status, err := a.Memory.Status(ctx, event.Scope.Access)
	if err != nil {
		return appRuntimeError(err)
	}
	if !status.Enabled || status.Epoch != event.Epoch || status.Fence != event.Fence {
		return managedruntime.ErrDenied
	}
	return nil
}

func (a RuntimeApplications) Recall(ctx context.Context, scope managedruntime.Scope, text string) (managedruntime.RecallSnapshot, error) {
	if a.Memory == nil || !scope.Capabilities.Allows(agentpolicy.Memory) {
		return managedruntime.RecallSnapshot{}, nil
	}
	v, err := a.Memory.Recall(ctx, appScope(scope).Access, text)
	if errors.Is(err, application.ErrDisabled) {
		return managedruntime.RecallSnapshot{}, nil
	}
	return managedruntime.RecallSnapshot{Epoch: v.Epoch, Fence: v.Fence, Text: v.Text}, appRuntimeError(err)
}
func (a RuntimeApplications) RecallValid(ctx context.Context, scope managedruntime.Scope, snapshot managedruntime.RecallSnapshot) bool {
	if a.Memory == nil || !scope.Capabilities.Allows(agentpolicy.Memory) {
		return false
	}
	call, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	status, err := a.Memory.Status(call, appScope(scope).Access)
	return err == nil && status.Enabled && status.Strategy == mc.Advanced && status.Epoch == snapshot.Epoch && status.Fence == snapshot.Fence
}

func (a RuntimeApplications) Contribute(ctx context.Context, item managedruntime.EvidenceDelivery) error {
	if a.Memory == nil || !item.Scope.Capabilities.Allows(agentpolicy.Memory) {
		return managedruntime.ErrDenied
	}
	scope := appScope(item.Scope)
	status, err := a.Memory.Status(ctx, scope.Access)
	if err != nil {
		return appRuntimeError(err)
	}
	if status.Strategy != mc.Advanced {
		return managedruntime.ErrDenied
	}
	ref := mc.Source{FleetID: scope.FleetID, AgentID: scope.AgentID, ThreadID: item.ThreadID, GenerationID: strconv.FormatInt(item.Generation, 10), From: uint64(item.Sequence), Through: uint64(item.Sequence)}
	return appRuntimeError(a.Memory.Contribute(ctx, scope, memory.Contribution{Epoch: status.Epoch, Evidence: mc.Evidence{Source: ref, Kind: "user", Text: item.Text, RecordedAt: item.RecordedAt}}))
}

type RuntimeApplications struct {
	Memory   RuntimeMemory
	Calendar RuntimeCalendar
	Evidence managedruntime.EvidenceStore
}

func appScope(s managedruntime.Scope) application.Scope {
	return application.Scope{Capabilities: s.Capabilities, Access: application.Access{ActorID: s.ActorID, TenantID: s.TenantID, UserID: s.UserID, AgentID: s.AgentID}, FleetID: s.FleetID, ActorEpoch: s.ActorAuthorizationEpoch, MemberEpoch: s.MembershipExecutionEpoch, AgentEpoch: s.AgentExecutionEpoch, MemberVersion: s.MembershipVersion}
}
func workerScope(s application.Scope) managedruntime.Scope {
	return managedruntime.Scope{Capabilities: s.Capabilities, ActorID: s.ActorID, TenantID: s.TenantID, UserID: s.UserID, AgentID: s.AgentID, FleetID: s.FleetID, ActorAuthorizationEpoch: s.ActorEpoch, MembershipExecutionEpoch: s.MemberEpoch, AgentExecutionEpoch: s.AgentEpoch, MembershipVersion: s.MemberVersion}
}
func appRuntimeError(err error) error {
	switch {
	case errors.Is(err, application.ErrDenied), errors.Is(err, application.ErrDisabled):
		return managedruntime.ErrDenied
	case errors.Is(err, application.ErrInvalid):
		return fmt.Errorf("%w: %s", managedruntime.ErrInvalid, err.Error())
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
	if job.Application == "calendar" && a.Calendar != nil {
		_, err := a.Calendar.Assignment(ctx, appScope(scope), job.ID, job.Epoch)
		return appRuntimeError(err)
	}
	if job.Application != "memory" || a.Memory == nil {
		return managedruntime.ErrDenied
	}
	_, err := a.Memory.Review(ctx, appScope(scope), binding(job))
	if errors.Is(err, application.ErrConflict) {
		return managedruntime.ErrDenied
	}
	return appRuntimeError(err)
}
func (a RuntimeApplications) memoryTools(ctx context.Context, scope managedruntime.Scope, job *managedruntime.ApplicationJob) (managedruntime.ApplicationTools, error) {
	if a.Memory == nil || !scope.Capabilities.Allows(agentpolicy.Memory) {
		return managedruntime.ApplicationTools{}, nil
	}
	call, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	status, err := a.Memory.Status(call, appScope(scope).Access)
	if errors.Is(err, application.ErrDisabled) {
		return managedruntime.ApplicationTools{}, nil
	}
	if err != nil && (job != nil && job.Application == "memory" || errors.Is(err, application.ErrDenied)) {
		return managedruntime.ApplicationTools{}, appRuntimeError(err)
	}
	catalog := managedruntime.ApplicationTools{Tools: memory.Tools(job != nil)}
	if job != nil && job.Application != "memory" {
		catalog.Tools = catalog.Tools[:len(catalog.Tools)-1]
	}
	if job == nil {
		catalog.Instructions = memory.AgentGuidance
		if status.Strategy == mc.Advanced {
			catalog.Tools = append(catalog.Tools, memory.MaintainTool())
		}
	}
	return catalog, nil
}
func decodeAppTool(input map[string]any, target any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return managedruntime.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		message := err.Error()
		if len(message) > 256 {
			message = "invalid tool JSON"
		}
		return fmt.Errorf("%w: %s", managedruntime.ErrInvalid, message)
	}
	if d.Decode(new(any)) != io.EOF {
		return managedruntime.ErrInvalid
	}
	return nil
}
func (a RuntimeApplications) Call(ctx context.Context, work managedruntime.ToolWork, job *managedruntime.ApplicationJob) (value any, err error) {
	defer func() { err = appRuntimeError(err) }()
	if strings.HasPrefix(work.Call.ToolName, "calendar_") {
		if job != nil && job.Application == "memory" {
			return nil, managedruntime.ErrDenied
		}
		return a.calendarCall(ctx, work)
	}
	if a.Memory == nil {
		return nil, managedruntime.ErrDenied
	}
	scope := appScope(work.Scope)
	switch work.Call.ToolName {
	case "memory_maintain":
		if job != nil || a.Evidence == nil {
			return nil, managedruntime.ErrDenied
		}
		var q struct {
			Reason string `json:"reason"`
		}
		if err = decodeAppTool(work.Call.Input, &q); err != nil {
			return nil, err
		}
		if _, err = a.Evidence.ToolEvidence(ctx, work); err != nil {
			return nil, err
		}
		return a.Memory.Maintain(ctx, scope, work.ThreadID, q.Reason, work.ID)
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
		var input memory.DecisionInput
		if err = decodeAppTool(work.Call.Input, &input); err != nil {
			return nil, err
		}
		q, err := input.Decision()
		if err != nil {
			return nil, err
		}
		return a.Memory.Decide(ctx, scope, binding(*job), q, work.ID)
	default:
		return nil, managedruntime.ErrInvalid
	}
}
func (a RuntimeApplications) Cancel(ctx context.Context, work managedruntime.ToolWork) error {
	switch work.Call.ToolName {
	case "calendar_change":
		if a.Calendar == nil {
			return managedruntime.ErrDenied
		}
		return appRuntimeError(a.Calendar.CancelCommand(ctx, appScope(work.Scope), work.ID))
	case "memory_propose", "memory_decide", "memory_maintain":
		if a.Memory == nil {
			return managedruntime.ErrDenied
		}
		return appRuntimeError(a.Memory.CancelCommand(ctx, appScope(work.Scope), work.ID))
	default:
		return nil
	}
}
