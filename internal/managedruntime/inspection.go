package managedruntime

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

// ThreadInspection reads current working state and the last frozen request in
// this generation. A request estimate is not a provider usage measurement.
type ThreadInspection struct {
	InputChecklist  InputChecklist     `json:"input_checklist"`
	WorkingFiles    *WorkingFiles      `json:"working_files"`
	Thread          Thread             `json:"thread"`
	State           ThreadState        `json:"state"`
	Capabilities    agentpolicy.Policy `json:"capabilities"`
	LatestRequest   *RequestInspection `json:"latest_request"`
	Usage           UsageCounts        `json:"usage"`
	ImportedHistory bool               `json:"imported_history"`
}

type RequestInspection struct {
	AttemptID       string                 `json:"attempt_id"`
	TurnID          string                 `json:"turn_id"`
	RecordedAt      time.Time              `json:"recorded_at"`
	Generation      int64                  `json:"generation"`
	Purpose         string                 `json:"purpose"`
	Model           string                 `json:"model"`
	Provider        string                 `json:"provider"`
	ContextWindow   int                    `json:"context_window"`
	OutputReserve   int                    `json:"output_reserve"`
	EstimatedTokens int                    `json:"estimated_tokens"`
	Breakdown       []llm.ContextUsagePart `json:"breakdown"`
	System          string                 `json:"system"`
	Tools           []llm.ToolSpec         `json:"tools"`
	MessageCount    int                    `json:"message_count"`
}

func (r ModelRequest) Inspection() RequestInspection {
	parts := []llm.ContextUsagePart{
		{Key: "system", Label: "System", Tokens: llm.EstimateTextTokens(r.System)},
		{Key: "tools", Label: "Tools", Tokens: llm.EstimateToolTokens(r.Tools)},
		{Key: "messages", Label: "Messages", Tokens: llm.EstimateMessageTokens(r.Messages)},
	}
	model := r.ContextModel()
	return RequestInspection{Generation: r.Generation, Purpose: r.Purpose, Model: model.Model, Provider: model.Provider,
		ContextWindow: model.ContextWindow, OutputReserve: outputBudget(r),
		EstimatedTokens: parts[0].Tokens + parts[1].Tokens + parts[2].Tokens, Breakdown: parts,
		System: r.System, Tools: append([]llm.ToolSpec{}, r.Tools...), MessageCount: len(r.Messages)}
}

type InspectionStore interface {
	Inspection(context.Context, Scope, string) (ThreadInspection, error)
}

func (s *Service) Inspection(ctx context.Context, actor, tenant, agent, thread string) (ThreadInspection, error) {
	if _, err := uuid.Parse(thread); err != nil {
		return ThreadInspection{}, ErrInvalid
	}
	// scope() initializes Main. Inspection must never create Runtime state.
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, false)
	if err != nil {
		return ThreadInspection{}, err
	}
	store, ok := s.Store.(InspectionStore)
	if !ok {
		return ThreadInspection{}, ErrInvalid
	}
	return store.Inspection(ctx, scope, thread)
}
