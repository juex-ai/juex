package migration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

// RuntimeBindings are owner-verified migration inputs. Application bindings use
// source Thread/Input IDs, but target job IDs. Model origins use source message
// IDs and must be proven from request provenance, never from a display label.
type RuntimeBindings struct {
	SourceSHA256 string
	Artifacts    map[string]execution.Artifact
	Applications map[string]managedruntime.ImportedApplication
	ModelOrigins map[string]map[string]managedruntime.ModelConfig
}

type IdentityMap struct {
	Threads  map[string]string            `json:"threads"`
	Messages map[string]map[string]string `json:"messages"`
	Inputs   map[string]map[string]string `json:"inputs"`
	Turns    map[string]map[string]string `json:"turns"`
}

// SequenceSpan is closed at both ends. Evidence starts map to First; ends and
// consumption cursors map to Last, including every fact in a source Commit.
type SequenceSpan struct {
	First int64 `json:"first"`
	Last  int64 `json:"last"`
}

type RuntimeConversion struct {
	Import      managedruntime.AgentImport         `json:"import"`
	Identities  IdentityMap                        `json:"identities"`
	CommitSpans map[string]map[uint64]SequenceSpan `json:"commit_spans"`
}

// SourceCommit preserves original source identities and facts for inspection.
// It is an inert historical event, not a command, outbox item or provider call.
// Exact framed bytes remain in the independently verified private source bundle.
type SourceCommit struct {
	ThreadID     string        `json:"thread_id"`
	GenerationID string        `json:"generation_id"`
	Commit       legacy.Commit `json:"commit"`
}

// ConvertRuntime converts a snapshot returned by legacy.ReadAgent/ReadFleet.
// It has no I/O, live authority lookup or store recovery behavior.
func ConvertRuntime(scope managedruntime.Scope, source legacy.Agent, bindings RuntimeBindings) (RuntimeConversion, error) {
	c, err := newMessageConverter(scope, source, bindings.Artifacts)
	if err != nil {
		return RuntimeConversion{}, err
	}
	roles, err := c.applicationRoles(source)
	if err != nil {
		return RuntimeConversion{}, err
	}
	bindings.Applications = maps.Clone(bindings.Applications)
	if bindings.Applications == nil {
		bindings.Applications = map[string]managedruntime.ImportedApplication{}
	}
	for thread := range roles {
		purpose := managedruntime.ImportedApplication{Application: "memory"}
		if existing, ok := bindings.Applications[thread]; ok && existing != purpose {
			return RuntimeConversion{}, errors.New("source historical Worker purpose cannot be replaced with a job")
		}
		bindings.Applications[thread] = purpose
	}
	for thread := range bindings.Applications {
		if _, exists := c.threads[thread]; !exists {
			return RuntimeConversion{}, errors.New("application binding names an unknown source Thread")
		}
	}
	for thread := range bindings.ModelOrigins {
		if _, exists := c.threads[thread]; !exists {
			return RuntimeConversion{}, errors.New("model binding names an unknown source Thread")
		}
	}
	result := RuntimeConversion{
		Import:      managedruntime.AgentImport{Source: "juex/281889e5/agent/" + source.Definition.ID, SourceSHA256: bindings.SourceSHA256},
		Identities:  IdentityMap{Threads: map[string]string{}, Messages: map[string]map[string]string{}, Inputs: map[string]map[string]string{}, Turns: map[string]map[string]string{}},
		CommitSpans: map[string]map[uint64]SequenceSpan{},
	}
	for _, thread := range source.Threads {
		id := thread.Metadata.ThreadID
		tc := threadConverter{messages: c, originalID: id, canonical: map[string]json.RawMessage{}, identities: &result.Identities, spans: map[uint64]SequenceSpan{}}
		result.Identities.Threads[id] = c.threadID(id).String()
		result.Identities.Messages[id], result.Identities.Inputs[id], result.Identities.Turns[id] = map[string]string{}, map[string]string{}, map[string]string{}
		if err := tc.convert(thread, bindings); err != nil {
			return RuntimeConversion{}, fmt.Errorf("source Thread %s: %w", id, err)
		}
		if role, exists := roles[id]; exists {
			if err := tc.appendEvent("application", role.AssignmentID, "import.application", tc.value.Thread.Generation, tc.value.Thread.UpdatedAt, role); err != nil {
				return RuntimeConversion{}, err
			}
			tc.value.Thread.Sequence = int64(len(tc.value.Events))
		}
		result.Import.Threads = append(result.Import.Threads, tc.value)
		result.CommitSpans[id] = tc.spans
	}
	if err := result.Import.Validate(scope.AgentID); err != nil {
		return RuntimeConversion{}, fmt.Errorf("converted Runtime import: %w", err)
	}
	return result, nil
}

type threadConverter struct {
	messages   *messageConverter
	originalID string
	canonical  map[string]json.RawMessage
	identities *IdentityMap
	spans      map[uint64]SequenceSpan
	value      managedruntime.ImportedThread
}

func (c *threadConverter) convert(source legacy.Thread, bindings RuntimeBindings) error {
	m := source.Metadata
	created, err := time.Parse(time.RFC3339Nano, m.CreatedAt)
	if err != nil {
		return err
	}
	updated, err := time.Parse(time.RFC3339Nano, m.UpdatedAt)
	if err != nil {
		return err
	}
	c.value.Thread = managedruntime.Thread{ID: c.messages.threadID(c.originalID).String(), AgentID: c.messages.scope.AgentID, Kind: "worker", Name: m.Alias, Retention: m.RetentionState, State: "idle", Generation: int64(m.CurrentGeneration.Ordinal), CreatedAt: created, UpdatedAt: updated}
	if c.originalID == "0" {
		if m.ParentThreadID != "" {
			return errors.New("source Main has a parent")
		}
		c.value.Thread.Kind = "main"
	} else {
		if _, exists := c.messages.threads[m.ParentThreadID]; !exists {
			return errors.New("source Worker has no parent")
		}
		c.value.Thread.ParentID = c.messages.threadID(m.ParentThreadID).String()
	}
	if m.Counts.PendingInputCount != 0 || m.RetentionState == "active" && m.ExecutionState != "idle" {
		return errors.New("source Thread is not settled")
	}
	generations := map[string]int64{}
	for _, g := range m.Generations {
		generations[g.ID] = int64(g.Ordinal)
	}
	for i, commit := range source.Commits {
		generation := generations[commit.GenerationID]
		if commit.Seq != uint64(i+1) || generation == 0 || len(commit.Facts) == 0 {
			return errors.New("source Commit sequence or Generation is invalid")
		}
		at, err := time.Parse(time.RFC3339Nano, commit.At)
		if err != nil {
			return err
		}
		first := int64(len(c.value.Events) + 1)
		if err := c.appendEvent("commit", strconv.FormatUint(commit.Seq, 10), "import.commit", generation, at, SourceCommit{c.originalID, commit.GenerationID, commit}); err != nil {
			return err
		}
		for _, fact := range commit.Facts {
			if fact.TurnID != "" {
				c.turnIdentity(fact.TurnID)
			}
			if fact.Type == "event.recorded" {
				var event struct {
					TurnID string `json:"turn_id"`
				}
				if err := json.Unmarshal(fact.Event, &event); err != nil {
					return err
				}
				if event.TurnID != "" {
					c.turnIdentity(event.TurnID)
				}
			}
			for _, message := range []*llm.Message{fact.Message, fact.Summary} {
				if message != nil {
					if err := c.appendMessage(*message, generation, at); err != nil {
						return err
					}
				}
			}
			if fact.Seed != nil {
				for _, message := range fact.Seed.ProviderMessages {
					if err := c.appendMessage(message, generation, at); err != nil {
						return err
					}
				}
			}
		}
		c.spans[commit.Seq] = SequenceSpan{first, int64(len(c.value.Events))}
	}
	if len(source.Commits) == 0 {
		return errors.New("source Thread has no history")
	}
	for _, message := range source.Context {
		converted, err := c.messages.message(c.originalID, message)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(converted)
		if err != nil || !bytes.Equal(c.canonical[converted.ID], encoded) {
			return errors.New("current context differs from its canonical historical message")
		}
		// The source Generation retains audit-only messages. Its provider-visible
		// projection excluded both forms, including from compaction input.
		if converted.PolicyBlocked || converted.Kind == llm.MessageKindPolicyEvent {
			continue
		}
		if converted.Compaction != nil {
			for _, reference := range converted.Compaction.RetainedInputReferences {
				for _, block := range reference.Blocks {
					if block.Media != nil {
						return errors.New("current compaction contains media available only through metadata")
					}
				}
			}
		}
		c.value.Context = append(c.value.Context, converted.ID)
	}
	if err := c.validateReferences(); err != nil {
		return err
	}
	if err := c.convertInputs(source); err != nil {
		return err
	}
	if application, exists := bindings.Applications[c.originalID]; exists {
		if application.InputID != "" {
			id, exists := c.identities.Inputs[c.originalID][application.InputID]
			if !exists {
				return errors.New("application binding names a missing source Input")
			}
			application.InputID = id
		}
		c.value.Application, c.value.Thread.Application = &application, application.Application
	}
	for oldID, model := range bindings.ModelOrigins[c.originalID] {
		id, exists := c.identities.Messages[c.originalID][oldID]
		if !exists {
			return errors.New("model origin names a missing source message")
		}
		if c.value.ModelOrigins == nil {
			c.value.ModelOrigins = map[string]managedruntime.ModelConfig{}
		}
		c.value.ModelOrigins[id] = model
	}
	c.value.Thread.Sequence = int64(len(c.value.Events))
	return nil
}

func (c *threadConverter) turnIdentity(old string) string {
	id := mappedIdentity(c.messages.threadID(c.originalID), "turn", old).String()
	c.identities.Turns[c.originalID][old] = id
	return id
}

func (c *threadConverter) appendEvent(kind, old, eventKind string, generation int64, at time.Time, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	id := mappedIdentity(c.messages.threadID(c.originalID), "event:"+kind, old).String()
	c.value.Events = append(c.value.Events, managedruntime.Event{ID: id, ThreadID: c.value.Thread.ID, Sequence: int64(len(c.value.Events) + 1), Generation: generation, Kind: eventKind, Data: data, CreatedAt: at})
	return nil
}

func (c *threadConverter) appendMessage(source llm.Message, generation int64, at time.Time) error {
	message, err := c.messages.message(c.originalID, source)
	if err != nil {
		return err
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if prior, exists := c.canonical[message.ID]; exists {
		if !bytes.Equal(data, prior) {
			return errors.New("source message identity has conflicting canonical content")
		}
		return nil
	}
	c.identities.Messages[c.originalID][source.ID] = message.ID
	c.canonical[message.ID] = data
	if err := c.appendEvent("message", source.ID, "message.appended", generation, at, message); err != nil {
		return err
	}
	if source.Compaction != nil {
		for _, reference := range source.Compaction.RetainedInputReferences {
			if err := c.appendMessage(reference, generation, at); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *threadConverter) validateReferences() error {
	for _, encoded := range c.canonical {
		var message llm.Message
		if err := json.Unmarshal(encoded, &message); err != nil {
			return err
		}
		if meta := message.Compaction; meta != nil {
			ids := append([]string{meta.PreviousSummaryID, meta.FirstKeptMessageID, meta.TailStartMessageID}, meta.RetainedMessageIDs...)
			for _, id := range ids {
				if id != "" && c.canonical[id] == nil {
					return errors.New("compaction names a missing historical message")
				}
			}
		}
	}
	return nil
}

func (c *threadConverter) convertInputs(source legacy.Thread) error {
	states, err := inputOutcomes(source)
	if err != nil {
		return err
	}
	for _, original := range source.Inputs {
		id := mappedIdentity(c.messages.threadID(c.originalID), "input", original.ID).String()
		c.identities.Inputs[c.originalID][original.ID] = id
		message, err := c.messages.message(c.originalID, original.Message)
		if err != nil {
			return err
		}
		var text []string
		for _, block := range message.Blocks {
			if block.Type == llm.BlockText {
				text = append(text, block.Text)
			}
		}
		input := managedruntime.ImportedInput{InputReceipt: managedruntime.InputReceipt{ID: id, RequestID: "import:" + id, ThreadID: c.value.Thread.ID, State: states[original.ID], AcceptedAt: original.CreatedAt}, Text: strings.Join(text, "\n\n")}
		c.value.Inputs = append(c.value.Inputs, input)
		record := struct {
			Source  legacy.Input                `json:"source"`
			Receipt managedruntime.InputReceipt `json:"receipt"`
			Message llm.Message                 `json:"message"`
			TurnID  string                      `json:"turn_id"`
		}{original, input.InputReceipt, message, c.turnIdentity(original.TurnID)}
		if err := c.appendEvent("input", original.ID, "import.input", c.value.Thread.Generation, original.CreatedAt, record); err != nil {
			return err
		}
	}
	return nil
}
