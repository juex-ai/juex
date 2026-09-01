package thread

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/homestore"
	"github.com/juex-ai/juex/internal/llm"
)

type ProjectionPersistError struct {
	Commit Commit
	Err    error
}

func (e *ProjectionPersistError) Error() string {
	return fmt.Sprintf("%v at sequence %d: %v", ErrProjectionStale, e.Commit.Seq, e.Err)
}

func (e *ProjectionPersistError) Unwrap() error {
	return errors.Join(ErrProjectionStale, e.Err)
}

type Thread struct {
	ID             string
	Dir            string
	Alias          string
	ParentThreadID string
	History        []llm.Message
	TokenUsage     llm.Usage
	ContextUsage   *llm.ContextUsage

	mu      sync.Mutex
	journal *Journal
	state   ReplayState
	store   *Store
	closed  bool
}

func (t *Thread) ScratchpadDir() string {
	if t == nil {
		return ""
	}
	return filepath.Join(t.Dir, "scratchpad")
}

func (t *Thread) SpoolDir() string {
	if t == nil {
		return ""
	}
	return filepath.Join(t.Dir, "spool")
}

func (t *Thread) Projection() Projection {
	if t == nil {
		return Projection{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneProjection(t.state.Projection)
}

func (t *Thread) Info() Info {
	if t == nil {
		return Info{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.infoLocked()
}

func (t *Thread) Snapshot() (Projection, []llm.Message) {
	if t == nil {
		return Projection{}, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneProjection(t.state.Projection), append([]llm.Message(nil), t.History...)
}

func (t *Thread) ReplaySnapshot() ReplayState {
	if t == nil {
		return ReplayState{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneReplayState(t.state)
}

func (t *Thread) LatestEventCursor() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.state.Events) == 0 {
		return ""
	}
	return t.state.Events[len(t.state.Events)-1].ID
}

func (t *Thread) Timeline(cursor string, limit int) (TimelinePage, error) {
	if t == nil {
		return TimelinePage{}, fmt.Errorf("thread: nil Thread")
	}
	return LoadTimelinePage(t.Dir, cursor, limit)
}

func (t *Thread) AppendFacts(facts ...Fact) (Commit, error) {
	if t == nil {
		return Commit{}, fmt.Errorf("thread: nil Thread")
	}
	if t.store != nil {
		t.store.mu.Lock()
		defer t.store.mu.Unlock()
	}
	return t.appendFactsStoreLocked(facts...)
}

// appendFactsStoreLocked commits facts while the Agent-wide Store lock is
// held. Keeping journal, per-Thread projection, and the list index under the
// same lock prevents independent Store handles from overwriting each other.
func (t *Thread) appendFactsStoreLocked(facts ...Fact) (Commit, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.journal == nil {
		return Commit{}, fmt.Errorf("thread: closed")
	}
	candidate := cloneReplayState(t.state)
	commit, _, _, err := t.journal.appendValidated(facts, func(scanned scannedCommit) error {
		return applyCommit(t.ID, &candidate, scanned)
	})
	if err != nil {
		return Commit{}, err
	}
	t.state = candidate
	t.refreshPublicLocked()
	if err := t.persistProjectionLocked(); err != nil {
		return commit, &ProjectionPersistError{Commit: commit, Err: err}
	}
	if t.store != nil {
		if err := t.store.updateProjectionLocked(t.state.Projection); err != nil {
			return commit, &ProjectionPersistError{Commit: commit, Err: err}
		}
	}
	if shouldAppendCheckpoint(t.state, commit) {
		checkpoint := checkpointFromState(t.state)
		candidate := cloneReplayState(t.state)
		checkpointCommit, _, _, checkpointErr := t.journal.appendValidated(
			[]Fact{{Type: FactProjectionCheck, Checkpoint: &checkpoint}},
			func(scanned scannedCommit) error { return applyCommit(t.ID, &candidate, scanned) },
		)
		if checkpointErr != nil {
			return commit, fmt.Errorf("thread: primary sequence %d committed but checkpoint failed: %w", commit.Seq, checkpointErr)
		}
		t.state = candidate
		t.refreshPublicLocked()
		if err := t.persistProjectionLocked(); err != nil {
			return checkpointCommit, &ProjectionPersistError{Commit: checkpointCommit, Err: err}
		}
		if t.store != nil {
			if err := t.store.updateProjectionLocked(t.state.Projection); err != nil {
				return checkpointCommit, &ProjectionPersistError{Commit: checkpointCommit, Err: err}
			}
		}
	}
	return commit, nil
}

func (t *Thread) Append(message llm.Message) error {
	_, err := t.AppendAssigned(message)
	return err
}

func (t *Thread) AppendAssigned(message llm.Message) (llm.Message, error) {
	messages, err := t.AppendBatchAssigned([]llm.Message{message})
	if len(messages) == 0 {
		return llm.Message{}, err
	}
	return messages[0], err
}

func (t *Thread) AppendBatch(messages []llm.Message) error {
	_, err := t.AppendBatchAssigned(messages)
	return err
}

func (t *Thread) AppendBatchAssigned(messages []llm.Message) ([]llm.Message, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, fmt.Errorf("thread: closed")
	}
	generationID := t.state.Projection.CurrentGeneration.ID
	t.mu.Unlock()
	prepared := make([]llm.Message, len(messages))
	facts := make([]Fact, len(messages))
	for i, message := range messages {
		prepared[i] = prepareMessage(message)
		facts[i] = Fact{Type: FactMessageAppended, GenerationID: generationID, Message: &prepared[i]}
	}
	_, err := t.AppendFacts(facts...)
	return prepared, err
}

func (t *Thread) AppendEvent(event events.Event) error {
	if event.Transient {
		return nil
	}
	event = events.Normalize(event)
	t.mu.Lock()
	for _, recorded := range t.state.Events {
		if recorded.ID == event.ID {
			t.mu.Unlock()
			return nil
		}
	}
	t.mu.Unlock()
	facts := []Fact{{Type: FactEventRecorded, Event: &event}}
	switch event.Type {
	case "turn.started":
		facts = append(facts, Fact{Type: FactTurnStarted, TurnID: event.TurnID})
	case "turn.completed":
		facts = append(facts, Fact{Type: FactTurnCompleted, TurnID: event.TurnID})
		facts = append(facts, t.inputSettlementFacts(event.TurnID, FactInputAttemptDone, FactInputCompleted, "")...)
		facts = append(facts, Fact{Type: FactThreadSettled, TurnID: event.TurnID})
	case "turn.errored":
		errorText := eventPayloadError(event.Payload)
		facts = append(facts, Fact{Type: FactTurnFailed, TurnID: event.TurnID})
		facts = append(facts, t.inputSettlementFacts(event.TurnID, FactInputAttemptFailed, FactInputDeadLettered, errorText)...)
	case "turn.cancelled":
		facts = append(facts, Fact{Type: FactTurnCancelled, TurnID: event.TurnID})
		facts = append(facts, t.inputSettlementFacts(event.TurnID, FactInputAttemptCancel, FactInputCancelled, eventPayloadError(event.Payload))...)
		facts = append(facts, Fact{Type: FactThreadSettled, TurnID: event.TurnID})
	}
	_, err := t.AppendFacts(facts...)
	return err
}

func (t *Thread) inputSettlementFacts(turnID, attemptFact, terminalFact, errorText string) []Fact {
	if t == nil || turnID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var facts []Fact
	for _, inputID := range t.state.InputOrder {
		input := t.state.Inputs[inputID]
		if input == nil || input.State != InputRunning || len(input.Attempts) == 0 {
			continue
		}
		attempt := input.Attempts[len(input.Attempts)-1]
		if attempt.State != "running" || attempt.TurnID != turnID {
			continue
		}
		facts = append(facts,
			Fact{Type: attemptFact, InputID: inputID, AttemptID: attempt.ID, Error: errorText},
			Fact{Type: terminalFact, InputID: inputID, Error: errorText},
		)
	}
	return facts
}

func eventPayloadError(payload any) string {
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	var value struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &value)
	return value.Error
}

func (t *Thread) BeginNewGeneration() (Commit, error) {
	t.mu.Lock()
	current := t.state.Projection.CurrentGeneration
	t.mu.Unlock()
	return t.AppendFacts(Fact{
		Type:             FactContextRenewed,
		FromGenerationID: current.ID,
		ToGenerationID:   generationID(current.Ordinal + 1),
	})
}

func (t *Thread) BeginCompactedGeneration(summary llm.Message, automatic bool) (Commit, error) {
	t.mu.Lock()
	current := t.state.Projection.CurrentGeneration
	t.mu.Unlock()
	summary = prepareMessage(summary)
	return t.AppendFacts(Fact{
		Type:             FactContextCompacted,
		FromGenerationID: current.ID,
		ToGenerationID:   generationID(current.Ordinal + 1),
		Summary:          &summary,
		Automatic:        automatic,
	})
}

func (t *Thread) ApplyAlias(alias string) error {
	if alias == "" {
		return fmt.Errorf("thread: alias is required")
	}
	if t.store == nil {
		_, err := t.AppendFacts(Fact{Type: FactThreadRenamed, Alias: alias})
		return err
	}
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	index, err := t.store.loadOrRebuildIndexLocked()
	if err != nil {
		return err
	}
	if err := validateAliasAvailable(index, alias, t.ID); err != nil {
		return err
	}
	_, err = t.appendFactsStoreLocked(Fact{Type: FactThreadRenamed, Alias: alias})
	return err
}

func (t *Thread) RecordResponseUsage(usage llm.Usage, contextUsage *llm.ContextUsage) llm.Usage {
	if usage.IsZero() && contextUsage == nil {
		return t.TokenUsageSnapshot()
	}
	_, _ = t.AppendFacts(Fact{Type: FactUsageRecorded, Usage: &usage, ContextUsage: contextUsage})
	return t.TokenUsageSnapshot()
}

func (t *Thread) TokenUsageSnapshot() llm.Usage {
	if t == nil {
		return llm.Usage{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.TokenUsage
}

func (t *Thread) ContextUsageSnapshot() *llm.ContextUsage {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneContextUsage(t.ContextUsage)
}

func (t *Thread) HasMessageID(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, message := range t.state.Messages {
		if message.ID == id {
			return true
		}
	}
	return false
}

func (t *Thread) ReplayEvents(visit func(events.Event)) {
	if t == nil || visit == nil {
		return
	}
	t.mu.Lock()
	eventsSnapshot := append([]events.Event(nil), t.state.Events...)
	t.mu.Unlock()
	for _, event := range eventsSnapshot {
		visit(event)
	}
}

func (t *Thread) SubscribeBus(bus *events.Bus) func() {
	if t == nil || bus == nil {
		return func() {}
	}
	bus.SetCommitter(threadEventCommitter{target: t})
	return func() { bus.SetCommitter(nil) }
}

type threadEventCommitter struct {
	target *Thread
}

func (c threadEventCommitter) Commit(event events.Event) (events.Event, error) {
	event = events.Normalize(event)
	if c.target == nil || event.Transient {
		return event, nil
	}
	if err := c.target.AppendEvent(event); err != nil {
		return events.Event{}, err
	}
	return event, nil
}

func (t *Thread) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.journal == nil {
		return nil
	}
	err := t.journal.Close()
	t.journal = nil
	return err
}

func (t *Thread) persistProjectionLocked() error {
	data, err := json.MarshalIndent(t.state.Projection, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return homestore.WriteFileAtomic(filepath.Join(t.Dir, projectionFile), data, 0o600, 0o755)
}

func (t *Thread) infoLocked() Info {
	projection := t.state.Projection
	return Info{
		ID:             projection.ThreadID,
		Alias:          projection.Alias,
		ParentThreadID: projection.ParentThreadID,
		Dir:            t.Dir,
		CreatedAt:      projection.CreatedAt,
		LastActivityAt: projection.LastActivityAt,
		ArchivedAt:     projection.ArchivedAt,
		State:          projection.State,
		Revision:       projection.Revision,
		GenerationID:   projection.CurrentGeneration.ID,
		TurnCount:      projection.Counts.TurnCount,
		PendingInputs:  projection.Counts.PendingInputCount,
		TokenUsage:     t.TokenUsage,
		ContextUsage:   cloneContextUsage(t.ContextUsage),
	}
}

func (t *Thread) refreshPublicLocked() {
	t.Alias = t.state.Projection.Alias
	t.ParentThreadID = t.state.Projection.ParentThreadID
	t.History = append(t.History[:0], t.state.ProviderMessages...)
	t.TokenUsage = t.state.Projection.TokenUsage
	t.ContextUsage = cloneContextUsage(t.state.ContextUsage)
}

func cloneReplayState(source ReplayState) ReplayState {
	clone := ReplayState{
		Projection:       cloneProjection(source.Projection),
		Messages:         append([]llm.Message(nil), source.Messages...),
		ProviderMessages: append([]llm.Message(nil), source.ProviderMessages...),
		Events:           append([]events.Event(nil), source.Events...),
		Activities:       append([]Activity(nil), source.Activities...),
		Inputs:           make(map[string]*InputProjection, len(source.Inputs)),
		InputOrder:       append([]string(nil), source.InputOrder...),
		InputRecords:     make(map[string]json.RawMessage, len(source.InputRecords)),
		ContextUsage:     cloneContextUsage(source.ContextUsage),
	}
	for id, input := range source.Inputs {
		copyInput := *input
		copyInput.Attempts = append([]AttemptProjection(nil), input.Attempts...)
		clone.Inputs[id] = &copyInput
	}
	for id, record := range source.InputRecords {
		clone.InputRecords[id] = append(json.RawMessage(nil), record...)
	}
	return clone
}

func cloneProjection(source Projection) Projection {
	clone := source
	clone.Goal = append(json.RawMessage(nil), source.Goal...)
	if source.ArchivedAt != nil {
		archivedAt := *source.ArchivedAt
		clone.ArchivedAt = &archivedAt
	}
	if source.NotesUpdatedAt != nil {
		updatedAt := *source.NotesUpdatedAt
		clone.NotesUpdatedAt = &updatedAt
	}
	if source.ContextUsage != nil {
		context := *source.ContextUsage
		if source.ContextUsage.CalibratedAt != nil {
			calibratedAt := *source.ContextUsage.CalibratedAt
			context.CalibratedAt = &calibratedAt
		}
		clone.ContextUsage = &context
	}
	return clone
}

func cloneContextUsage(source *llm.ContextUsage) *llm.ContextUsage {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Breakdown = append([]llm.ContextUsagePart(nil), source.Breakdown...)
	return &clone
}
