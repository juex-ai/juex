package managedruntime

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

// AgentImport is an offline, already-converted owner record. Runtime never
// reads a legacy directory or runs recovery code to interpret this value.
type AgentImport struct {
	Source       string           `json:"source"`
	SourceSHA256 string           `json:"source_sha256"`
	Threads      []ImportedThread `json:"threads"`
}

type ImportedThread struct {
	WorkingFiles *WorkingFiles          `json:"working_files,omitempty"`
	State        ThreadState            `json:"state"`
	Thread       Thread                 `json:"thread"`
	Events       []Event                `json:"events"`
	Inputs       []ImportedInput        `json:"inputs"`
	Context      []string               `json:"context"`
	Application  *ImportedApplication   `json:"application,omitempty"`
	ModelOrigins map[string]ModelConfig `json:"model_origins,omitempty"`
}

type ImportedInput struct {
	InputReceipt
	Text string `json:"text"`
}

// Imported application work retains its purpose without carrying execution
// authority. A purpose-only historical Worker has no JobID, InputID or State;
// several such Workers may belong to one source application's business review.
// A proven one-to-one job can additionally retain its terminal outcome.
type ImportedApplication struct {
	Application string `json:"application"`
	JobID       string `json:"job_id,omitempty"`
	InputID     string `json:"input_id,omitempty"`
	State       string `json:"state,omitempty"`
}

func (v AgentImport) Validate(agent string) error {
	digest, err := hex.DecodeString(v.SourceSHA256)
	if !importUUID(agent) || strings.TrimSpace(v.Source) == "" || len(v.Source) > 512 || err != nil || len(digest) != 32 || len(v.Threads) == 0 {
		return ErrInvalid
	}
	threads := make(map[string]Thread, len(v.Threads))
	events, inputs, messages, jobs := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	mains := 0
	for _, imported := range v.Threads {
		if imported.WorkingFiles != nil {
			if err := imported.WorkingFiles.Validate(); err != nil {
				return err
			}
		}
		if err := imported.State.Validate(); err != nil {
			return err
		}
		t := imported.Thread
		if !importUUID(t.ID) || t.AgentID != agent || t.CreatedAt.IsZero() || t.UpdatedAt.Before(t.CreatedAt) || strings.TrimSpace(t.Name) == "" || len([]rune(t.Name)) > 100 || t.PendingInputs != 0 || t.HeldInputs != 0 || t.Generation < 1 || t.Sequence != int64(len(imported.Events)) {
			return ErrInvalid
		}
		if _, exists := threads[t.ID]; exists || (t.Retention != "active" && t.Retention != "archived") || (t.State != "idle" && t.State != "failed") {
			return ErrInvalid
		}
		if t.Kind == "main" {
			mains++
			if t.ParentID != "" || t.Retention != "active" || imported.Application != nil {
				return ErrInvalid
			}
		} else if t.Kind != "worker" || !importUUID(t.ParentID) {
			return ErrInvalid
		}
		threads[t.ID] = t
		threadMessages := map[string]llm.Message{}
		for i, event := range imported.Events {
			if !importUUID(event.ID) || events[event.ID] || event.ThreadID != t.ID || event.Sequence != int64(i+1) || event.Generation < 1 || event.Generation > t.Generation || event.Kind == "" || event.CreatedAt.IsZero() || !json.Valid(event.Data) {
				return ErrInvalid
			}
			events[event.ID] = true
			if event.Kind == "message.appended" {
				var message llm.Message
				if json.Unmarshal(event.Data, &message) != nil || !importUUID(message.ID) || messages[message.ID] || len(message.Blocks) == 0 || (message.Role != llm.RoleUser && message.Role != llm.RoleAssistant && message.Role != llm.RoleSystem) {
					return ErrInvalid
				}
				messages[message.ID] = true
				threadMessages[message.ID] = message
			}
		}
		seen := map[string]bool{}
		context := make([]llm.Message, 0, len(imported.Context))
		for _, id := range imported.Context {
			message, exists := threadMessages[id]
			if !exists || seen[id] {
				return ErrInvalid
			}
			seen[id] = true
			context = append(context, message)
		}
		if llm.ValidateToolTranscript(context) != nil {
			return ErrInvalid
		}
		for id, model := range imported.ModelOrigins {
			message, exists := threadMessages[id]
			if !exists || message.Role != llm.RoleAssistant || !importUUID(model.ModelID) || model.Provider == "" || model.Model == "" || model.Protocol == "" || model.Endpoint == "" || model.ModelAuthorizationEpoch < 1 {
				return ErrInvalid
			}
		}
		requests := map[string]bool{}
		threadInputs := map[string]ImportedInput{}
		for _, input := range imported.Inputs {
			if !importUUID(input.ID) || inputs[input.ID] || input.ThreadID != t.ID || input.RequestID == "" || len(input.RequestID) > 200 || requests[input.RequestID] || !importTerminal(input.State) || input.AcceptedAt.IsZero() {
				return ErrInvalid
			}
			inputs[input.ID], requests[input.RequestID] = true, true
			threadInputs[input.ID] = input
		}
		if app := imported.Application; app != nil {
			if (app.Application != "memory" && app.Application != "calendar") || t.Application != app.Application {
				return ErrInvalid
			}
			if app.JobID == "" {
				if app.InputID != "" || app.State != "" {
					return ErrInvalid
				}
			} else {
				key := app.Application + "/" + app.JobID
				if len(app.JobID) > 128 || jobs[key] || !importTerminal(app.State) || (app.InputID != "" && threadInputs[app.InputID].State != app.State) {
					return ErrInvalid
				}
				jobs[key] = true
			}
		} else if t.Application != "" {
			return ErrInvalid
		}
	}
	if mains != 1 {
		return ErrInvalid
	}
	for id, thread := range threads {
		seen := map[string]bool{id: true}
		for thread.ParentID != "" {
			parent, exists := threads[thread.ParentID]
			if !exists || seen[parent.ID] {
				return ErrInvalid
			}
			seen[parent.ID] = true
			thread = parent
		}
	}
	return nil
}

func importUUID(id string) bool {
	v, err := uuid.Parse(id)
	return err == nil && v != uuid.Nil && v.String() == id
}

func importTerminal(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}
