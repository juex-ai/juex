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

	mu              sync.Mutex
	eventStore      *EventStore
	state           ReplayState
	store           *Store
	writeProjection func(string, []byte) error
	closed          bool
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

func (t *Thread) CurrentGenerationJournalPath() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.eventStore == nil {
		return ""
	}
	return t.eventStore.CurrentGenerationJournalPath()
}

func (t *Thread) GenerationJournalPaths() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.eventStore == nil {
		return nil
	}
	return t.eventStore.GenerationJournalPaths(t.state.Projection.Generations)
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
// held. Keeping EventStore, per-Thread metadata, and the list index under the
// same lock prevents independent Store handles from overwriting each other.
func (t *Thread) appendFactsStoreLocked(facts ...Fact) (Commit, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.eventStore == nil {
		return Commit{}, fmt.Errorf("thread: closed")
	}
	if err := t.ensureProjectionCurrentLocked(); err != nil {
		return Commit{}, err
	}
	if t.state.Projection.RetentionState != RetentionActive {
		return Commit{}, fmt.Errorf("%w: archived Thread is read-only", ErrInvalidTransition)
	}
	for _, fact := range facts {
		if fact.Type == FactContextRenewed || fact.Type == FactContextCompacted {
			return Commit{}, fmt.Errorf("%w: context boundaries require Generation rollover", ErrInvalidTransition)
		}
	}
	commit, candidate, err := t.prepareAppendLocked(facts)
	if err != nil {
		return Commit{}, err
	}
	scanned, err := t.eventStore.appendCurrent(commit)
	if err != nil {
		return Commit{}, err
	}
	candidate.Projection.EventCursor.Offset = scanned.EndOffset
	if err := validateProjectionMetadata(candidate.Projection, t.ID); err != nil {
		return commit, fmt.Errorf("thread: committed invalid EventStore projection: %w", err)
	}
	t.state = candidate
	t.refreshPublicLocked()
	if err := t.persistProjectionLocked(); err != nil {
		return commit, &ProjectionPersistError{Commit: commit, Err: err}
	}
	if t.store != nil {
		if err := t.store.updateProjectionLocked(); err != nil {
			return commit, &ProjectionPersistError{Commit: commit, Err: err}
		}
	}
	return commit, nil
}

func (t *Thread) prepareAppendLocked(facts []Fact) (Commit, ReplayState, error) {
	commit, err := t.eventStore.prepareCommit(facts)
	if err != nil {
		return Commit{}, ReplayState{}, err
	}
	candidate := cloneReplayState(t.state)
	provisional := scannedCommit{
		Commit: commit, GenerationID: t.eventStore.currentID,
		StartOffset: t.eventStore.size, EndOffset: t.eventStore.size + 1,
	}
	if err := applyCommit(t.ID, &candidate, provisional); err != nil {
		return Commit{}, ReplayState{}, err
	}
	advanceProjectionRevision(&candidate.Projection, provisional.At)
	if err := validateProjectionMetadata(candidate.Projection, t.ID); err != nil {
		return Commit{}, ReplayState{}, err
	}
	return commit, candidate, nil
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
		facts = append(facts, Fact{Type: FactThreadSettled, TurnID: event.TurnID})
	case "turn.errored":
		facts = append(facts, Fact{Type: FactTurnFailed, TurnID: event.TurnID})
	case "turn.cancelled":
		facts = append(facts, Fact{Type: FactTurnCancelled, TurnID: event.TurnID})
		facts = append(facts, Fact{Type: FactThreadSettled, TurnID: event.TurnID})
	}
	_, err := t.AppendFacts(facts...)
	return err
}

func (t *Thread) BeginNewGeneration() (Commit, error) {
	return t.beginGeneration(false, llm.Message{}, false, nil)
}

func (t *Thread) BeginCompactedGeneration(summary llm.Message, automatic bool, contextUsage *llm.ContextUsage) (Commit, error) {
	summary = prepareMessage(summary)
	return t.beginGeneration(true, summary, automatic, contextUsage)
}

func (t *Thread) beginGeneration(compacted bool, summary llm.Message, automatic bool, contextUsage *llm.ContextUsage) (Commit, error) {
	if t == nil {
		return Commit{}, fmt.Errorf("thread: nil Thread")
	}
	if t.store != nil {
		t.store.mu.Lock()
		defer t.store.mu.Unlock()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.eventStore == nil {
		return Commit{}, fmt.Errorf("thread: closed")
	}
	if err := t.ensureProjectionCurrentLocked(); err != nil {
		return Commit{}, err
	}
	if t.state.Projection.RetentionState != RetentionActive {
		return Commit{}, fmt.Errorf("%w: archived Thread is read-only", ErrInvalidTransition)
	}
	current := t.state.Projection.CurrentGeneration
	nextID := generationID(current.Ordinal + 1)
	providerMessages := []llm.Message(nil)
	factType := FactContextRenewed
	var summaryPointer *llm.Message
	if compacted {
		factType = FactContextCompacted
		providerMessages = compactProviderMessages(summary, t.state.ProviderMessages)
		summaryPointer = &summary
	}
	seed := generationSeedFromState(t.state, providerMessages, contextUsage)
	fact := Fact{
		Type: factType, FromGenerationID: current.ID, ToGenerationID: nextID,
		Summary: summaryPointer, Automatic: automatic, Seed: &seed,
	}
	commit, err := t.eventStore.prepareCommit([]Fact{fact})
	if err != nil {
		return Commit{}, err
	}
	candidate := cloneReplayState(t.state)
	provisional := scannedCommit{Commit: commit, GenerationID: nextID, StartOffset: 0, EndOffset: 1}
	if err := applyCommit(t.ID, &candidate, provisional); err != nil {
		return Commit{}, err
	}
	advanceProjectionRevision(&candidate.Projection, provisional.At)
	if err := validateProjectionMetadata(candidate.Projection, t.ID); err != nil {
		return Commit{}, err
	}
	staged, err := t.eventStore.stageNextGeneration(commit, nextID)
	if err != nil {
		return Commit{}, err
	}
	candidate.Projection.EventCursor.Offset = staged.commit.EndOffset
	if err := validateProjectionMetadata(candidate.Projection, t.ID); err != nil {
		return Commit{}, errors.Join(err, t.eventStore.discard(staged))
	}
	if err := t.persistProjectionValueLocked(candidate.Projection); err != nil {
		return Commit{}, errors.Join(err, t.eventStore.discard(staged))
	}
	t.state = candidate
	t.refreshPublicLocked()
	activateErr := t.eventStore.activate(staged)
	if t.store != nil {
		if err := t.store.updateProjectionLocked(); err != nil {
			activateErr = errors.Join(activateErr, err)
		}
	}
	if activateErr != nil {
		return commit, &ProjectionPersistError{Commit: commit, Err: activateErr}
	}
	return commit, nil
}

func (t *Thread) ApplyAlias(alias string) error {
	if alias == "" {
		return fmt.Errorf("thread: alias is required")
	}
	if t.store == nil {
		return t.mutateProjectionLocked(func(projection *Projection, _ Timestamp) {
			projection.Alias = alias
		})
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
	if err := t.mutateProjectionLocked(func(projection *Projection, _ Timestamp) {
		projection.Alias = alias
	}); err != nil {
		return err
	}
	if err := t.store.updateProjectionLocked(); err != nil {
		return fmt.Errorf("thread: metadata committed but index refresh failed: %w", err)
	}
	return nil
}

// SetPendingInputCount materializes the bounded pending_inputs.json state in
// Thread metadata and the Agent index. The pending document remains the
// authority; loading it repairs this value after an interrupted refresh.
func (t *Thread) SetPendingInputCount(count int) error {
	if t == nil {
		return fmt.Errorf("thread: nil Thread")
	}
	if count < 0 {
		return fmt.Errorf("thread: negative pending input count %d", count)
	}
	if t.store != nil {
		t.store.mu.Lock()
		defer t.store.mu.Unlock()
	}
	t.mu.Lock()
	if t.closed || t.eventStore == nil {
		t.mu.Unlock()
		return fmt.Errorf("thread: closed")
	}
	if err := t.ensureProjectionCurrentLocked(); err != nil {
		t.mu.Unlock()
		return err
	}
	if t.state.Projection.Counts.PendingInputCount == count {
		t.mu.Unlock()
		if t.store != nil {
			if _, err := t.store.loadOrRebuildIndexLocked(); err != nil {
				return fmt.Errorf("thread: metadata current but index refresh failed: %w", err)
			}
		}
		return nil
	}
	candidate := cloneProjection(t.state.Projection)
	at := NewTimestamp(t.eventStore.now())
	candidate.Counts.PendingInputCount = count
	advanceProjectionRevision(&candidate, at)
	if err := validateProjectionMetadata(candidate, t.ID); err != nil {
		t.mu.Unlock()
		return err
	}
	if err := t.persistProjectionValueLocked(candidate); err != nil {
		t.mu.Unlock()
		return err
	}
	t.state.Projection = candidate
	t.refreshPublicLocked()
	t.mu.Unlock()
	if t.store != nil {
		if err := t.store.updateProjectionLocked(); err != nil {
			return fmt.Errorf("thread: metadata committed but index refresh failed: %w", err)
		}
	}
	return nil
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
	for _, message := range t.state.ProviderMessages {
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
	if t.eventStore == nil {
		return nil
	}
	err := t.eventStore.Close()
	return err
}

func (t *Thread) persistProjectionLocked() error {
	return t.persistProjectionValueLocked(t.state.Projection)
}

func (t *Thread) persistProjectionValueLocked(projection Projection) error {
	if err := validateProjectionMetadata(projection, t.ID); err != nil {
		return err
	}
	data, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if t.writeProjection != nil {
		return t.writeProjection(filepath.Join(t.Dir, projectionFile), data)
	}
	return homestore.WriteFileAtomic(filepath.Join(t.Dir, projectionFile), data, 0o600, 0o755)
}

func (t *Thread) mutateProjectionLocked(mutate func(*Projection, Timestamp)) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.eventStore == nil {
		return fmt.Errorf("thread: closed")
	}
	if err := t.ensureProjectionCurrentLocked(); err != nil {
		return err
	}
	candidate := cloneProjection(t.state.Projection)
	at := NewTimestamp(t.eventStore.now())
	mutate(&candidate, at)
	advanceProjectionRevision(&candidate, at)
	if err := validateProjectionMetadata(candidate, t.ID); err != nil {
		return err
	}
	if err := t.persistProjectionValueLocked(candidate); err != nil {
		return err
	}
	t.state.Projection = candidate
	t.refreshPublicLocked()
	return nil
}

func (t *Thread) ensureProjectionCurrentLocked() error {
	metadata, err := readProjectionFile(t.Dir, t.ID)
	if err != nil {
		return err
	}
	current := t.state.Projection
	if metadata.Revision != current.Revision || metadata.EventCursor != current.EventCursor {
		return fmt.Errorf(
			"%w for %s: metadata revision/cursor changed from %d/%d to %d/%d",
			ErrStaleHandle,
			t.ID,
			current.Revision,
			current.EventCursor.Seq,
			metadata.Revision,
			metadata.EventCursor.Seq,
		)
	}
	return nil
}

func advanceProjectionRevision(projection *Projection, at Timestamp) {
	projection.Revision++
	projection.UpdatedAt = at
}

func (t *Thread) infoLocked() Info {
	projection := t.state.Projection
	generationJournalPath := ""
	if t.eventStore != nil {
		generationJournalPath = t.eventStore.CurrentGenerationJournalPath()
	}
	return Info{
		ID:                    projection.ThreadID,
		Alias:                 projection.Alias,
		ParentThreadID:        projection.ParentThreadID,
		Dir:                   t.Dir,
		CreatedAt:             projection.CreatedAt,
		LastActivityAt:        projection.LastActivityAt,
		ArchivedAt:            projection.ArchivedAt,
		RetentionState:        projection.RetentionState,
		ExecutionState:        projection.ExecutionState,
		Revision:              projection.Revision,
		GenerationID:          projection.CurrentGeneration.ID,
		GenerationJournalPath: generationJournalPath,
		TurnCount:             projection.Counts.TurnCount,
		PendingInputs:         projection.Counts.PendingInputCount,
		TokenUsage:            t.TokenUsage,
		ContextUsage:          cloneContextUsage(t.ContextUsage),
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
		CompactionCount:  source.CompactionCount,
		ContextUsage:     cloneContextUsage(source.ContextUsage),
	}
	return clone
}

func cloneProjection(source Projection) Projection {
	clone := source
	clone.Generations = append([]GenerationProjection(nil), source.Generations...)
	if source.ArchivedAt != nil {
		archivedAt := *source.ArchivedAt
		clone.ArchivedAt = &archivedAt
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
