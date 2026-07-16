package observable

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/eventmedia"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/sandbox"
)

type ManagerOptions struct {
	ConfigPath    string
	StateDir      string
	WorkDir       string
	Shell         config.ShellProfile
	Sandbox       sandbox.Policy
	SandboxRunner sandbox.Runner
	Bus           *events.Bus
	Deliver       DeliveryFunc
	Now           func() time.Time
}

type Manager struct {
	opts           ManagerOptions
	cfg            FileConfig
	issues         []ConfigIssue
	store          *Store
	specs          map[string]Spec
	sources        map[string]sourceRuntime
	deleting       map[string]bool
	runs           map[string]*observableRun
	slots          map[string]*observableRun
	workers        map[*observableRun]struct{}
	lastStatus     map[string]ObservableStatus
	mu             sync.Mutex
	closed         bool
	closeMu        sync.Mutex
	closeCompleted bool
	closeErr       error
	closeAttempt   *closeAttempt
	callbackActive int
	deliveryMu     sync.Mutex
	deliveryWG     sync.WaitGroup
	deliveryClosed bool
}

type closeAttempt struct {
	done     chan struct{}
	err      error
	complete bool
}

// CloseDeferredError reports that Close is continuing in the background
// because a synchronous delivery callback is active.
type CloseDeferredError struct{}

func (*CloseDeferredError) Error() string {
	return "observable: close deferred while a delivery callback is active"
}

type observableRun struct {
	id                 string
	runID              string
	spec               Spec
	source             sourceRuntime
	sourceState        any
	manager            *Manager
	ctx                context.Context
	cancel             context.CancelFunc
	state              ObservableStatus
	claim              *terminalClaim
	terminalPending    bool
	pendingOutcome     terminalOutcome
	terminalDurable    bool
	shutdown           bool
	workerCompleted    bool
	completionErr      error
	completionReported bool
	terminalEvent      string
	terminalStatus     ObservableStatus
	startPublished     chan struct{}
	startPublishOnce   sync.Once
	workerReady        chan struct{}
	workerReadyOnce    sync.Once
	quiesced           chan struct{}
	quiescedOnce       sync.Once
	done               chan struct{}
	doneOnce           sync.Once
	lifecycleDoneOnce  sync.Once
	sourceDone         chan struct{}
	sourceDoneOnce     sync.Once
	finalizeOnce       sync.Once
}

type StatusSnapshot struct {
	Observables []ObservableStatus `json:"observables"`
}

type StatusCounts struct {
	Configured int
	Running    int
	Errors     int
}

type ObservableStatus struct {
	ID              string            `json:"id"`
	Name            string            `json:"name,omitempty"`
	SourceType      string            `json:"source_type,omitempty"`
	Command         string            `json:"command"`
	Args            []string          `json:"args,omitempty"`
	Streams         []string          `json:"streams,omitempty"`
	Batch           BatchSpec         `json:"batch"`
	Schedule        *ScheduleStatus   `json:"schedule,omitempty"`
	State           string            `json:"state"`
	RunID           string            `json:"run_id,omitempty"`
	PID             int               `json:"pid,omitempty"`
	StartedAt       time.Time         `json:"started_at,omitempty"`
	ExitedAt        time.Time         `json:"exited_at,omitempty"`
	ExitCode        *int              `json:"exit_code,omitempty"`
	LastError       string            `json:"last_error,omitempty"`
	LastObservation ObservationRecord `json:"last_observation,omitempty"`
}

type ScheduleStatus struct {
	Summary                string     `json:"summary,omitempty"`
	Timezone               string     `json:"timezone,omitempty"`
	CatchUpMode            string     `json:"catch_up_mode,omitempty"`
	NextOccurrence         *time.Time `json:"next_occurrence,omitempty"`
	LastEvaluatedAt        *time.Time `json:"last_evaluated_at,omitempty"`
	LastEmittedScheduledAt *time.Time `json:"last_emitted_scheduled_at,omitempty"`
}

func NewManager(opts ManagerOptions) (*Manager, error) {
	cfg, issues, err := LoadConfigLenient(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	m := &Manager{
		opts:       opts,
		cfg:        cfg,
		issues:     issues,
		store:      NewStore(opts.StateDir, StoreOptions{Now: opts.Now}),
		specs:      map[string]Spec{},
		sources:    map[string]sourceRuntime{},
		deleting:   map[string]bool{},
		runs:       map[string]*observableRun{},
		slots:      map[string]*observableRun{},
		workers:    map[*observableRun]struct{}{},
		lastStatus: map[string]ObservableStatus{},
	}
	for _, spec := range cfg.Observables {
		source, sourceErr := newSourceRuntime(spec, m, sourceDependencies{opts: opts, store: m.store})
		if sourceErr != nil {
			return nil, sourceErr
		}
		m.specs[spec.ID] = spec
		m.sources[spec.ID] = source
		m.lastStatus[spec.ID] = source.statusSnapshot(baseStatusFromSpec(spec, RunStateStopped))
	}
	for _, issue := range issues {
		status := statusFromSpec(issue.Spec, RunStateErrored)
		status.ID = issue.ID
		status.LastError = issue.Error.Error()
		m.lastStatus[status.ID] = status
	}
	return m, nil
}

func (m *Manager) StartAll(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	ids := make([]string, 0, len(m.specs))
	for id := range m.specs {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	sort.Strings(ids)
	var firstErr error
	for _, id := range ids {
		if err := m.Start(ctx, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) Start(ctx context.Context, id string) error {
	if m == nil {
		return fmt.Errorf("observable: nil manager")
	}
	runID := newRunID()
	runCtx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return fmt.Errorf("observable: manager closed")
	}
	if run := m.runs[id]; run != nil {
		if run.terminalPending {
			err := run.completionErr
			m.mu.Unlock()
			cancel()
			return fmt.Errorf("observable: run %q is awaiting terminal persistence: %w", run.runID, err)
		}
		m.mu.Unlock()
		cancel()
		return nil
	}
	if slot := m.slots[id]; slot != nil {
		err := slot.completionErr
		m.mu.Unlock()
		cancel()
		if err != nil {
			return fmt.Errorf("observable: previous run %q is awaiting terminal persistence: %w", slot.runID, err)
		}
		return fmt.Errorf("observable: previous run %q is still completing", slot.runID)
	}
	if m.deleting[id] {
		m.mu.Unlock()
		cancel()
		return fmt.Errorf("observable: %q is being deleted", id)
	}
	spec, ok := m.specs[id]
	if !ok {
		m.mu.Unlock()
		cancel()
		return fmt.Errorf("observable: unknown id %q", id)
	}
	source := m.sources[id]
	if source == nil {
		m.mu.Unlock()
		cancel()
		return fmt.Errorf("observable: source runtime missing for %q", id)
	}
	run := &observableRun{
		id:             id,
		runID:          runID,
		spec:           spec,
		source:         source,
		manager:        m,
		ctx:            runCtx,
		cancel:         cancel,
		quiesced:       make(chan struct{}),
		done:           make(chan struct{}),
		sourceDone:     make(chan struct{}),
		startPublished: make(chan struct{}),
		workerReady:    make(chan struct{}),
		state:          source.statusSnapshot(baseStatusFromSpec(spec, RunStateStarting)),
	}
	run.state.RunID = runID
	run.state.StartedAt = m.now()
	m.runs[id] = run
	if m.slots == nil {
		m.slots = map[string]*observableRun{}
	}
	m.slots[id] = run
	if m.workers == nil {
		m.workers = map[*observableRun]struct{}{}
	}
	m.workers[run] = struct{}{}
	m.lastStatus[id] = run.state
	m.mu.Unlock()
	if err := m.recordRun(RunRecord{ObservableID: id, RunID: runID, State: RunStateStarting, StartedAt: run.state.StartedAt}); err != nil {
		run.closeQuiesced()
		_, _ = m.finishRun(run, terminalOutcome{State: RunStateErrored, Err: err})
		run.closeDone()
		return err
	}
	return source.start(ctx, run)
}

func (m *Manager) Create(ctx context.Context, spec Spec) (ObservableStatus, error) {
	normalized, err := ValidateSpec(spec)
	if err != nil {
		return ObservableStatus{}, err
	}
	source, err := newSourceRuntime(normalized, m, sourceDependencies{opts: m.opts, store: m.store})
	if err != nil {
		return ObservableStatus{}, err
	}
	m.mu.Lock()
	if err := m.configEditableLocked(); err != nil {
		m.mu.Unlock()
		return ObservableStatus{}, err
	}
	if _, ok := m.specs[normalized.ID]; ok {
		m.mu.Unlock()
		return ObservableStatus{}, fmt.Errorf("observable: duplicate id %q", normalized.ID)
	}
	cfg := FileConfig{Observables: append(append([]Spec(nil), m.cfg.Observables...), normalized)}
	if err := SaveConfig(m.opts.ConfigPath, cfg); err != nil {
		m.mu.Unlock()
		return ObservableStatus{}, err
	}
	m.cfg = cfg
	m.specs[normalized.ID] = normalized
	m.sources[normalized.ID] = source
	status := source.statusSnapshot(baseStatusFromSpec(normalized, RunStateStopped))
	m.lastStatus[normalized.ID] = status
	m.mu.Unlock()
	if err := m.Start(ctx, normalized.ID); err != nil {
		return m.StatusByID(normalized.ID)
	}
	return m.StatusByID(normalized.ID)
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	if m == nil {
		return nil
	}
	run, claim, err := m.claimTerminal(ctx, id)
	if err != nil || run == nil {
		return err
	}
	result, stopErr := run.source.stop(ctx, run, sourceStopUser)
	if !result.Quiesced {
		m.rollbackTerminal(run, claim)
		return sourceNotQuiescedError(run.id, stopErr)
	}
	if stopErr != nil {
		commitErr := m.commitTerminal(run, claim, terminalOutcome{State: RunStateErrored, Err: stopErr}, true)
		_ = waitRunDone(ctx, run)
		if commitErr != nil {
			return fmt.Errorf("%w; record terminal state: %v", stopErr, commitErr)
		}
		return stopErr
	}
	if err := m.commitTerminal(run, claim, terminalOutcome{State: RunStateStopped}, true); err != nil {
		_ = waitRunDone(ctx, run)
		return err
	}
	if err := waitRunDone(ctx, run); err != nil {
		return err
	}
	return nil
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if err := m.configEditableLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	source := m.sources[id]
	_, exists := m.specs[id]
	if exists && source != nil {
		if m.deleting[id] {
			m.mu.Unlock()
			return fmt.Errorf("observable: %q is already being deleted", id)
		}
		m.deleting[id] = true
	}
	m.mu.Unlock()
	if !exists || source == nil {
		return fmt.Errorf("observable: unknown id %q", id)
	}
	deleted := false
	defer func() {
		if deleted {
			return
		}
		m.mu.Lock()
		delete(m.deleting, id)
		m.mu.Unlock()
	}()
	run, claim, err := m.claimTerminal(ctx, id)
	if err != nil {
		return err
	}
	if run != nil {
		result, stopErr := source.stop(ctx, run, sourceStopDelete)
		if !result.Quiesced {
			m.rollbackTerminal(run, claim)
			return sourceNotQuiescedError(run.id, stopErr)
		}
		if stopErr != nil {
			commitErr := m.commitTerminal(run, claim, terminalOutcome{State: RunStateErrored, Err: stopErr}, true)
			_ = waitRunDone(ctx, run)
			if commitErr != nil {
				return fmt.Errorf("%w; record terminal state: %v", stopErr, commitErr)
			}
			return stopErr
		}
		if err := m.commitTerminal(run, claim, terminalOutcome{State: RunStateStopped}, true); err != nil {
			return err
		}
		if err := waitRunDone(ctx, run); err != nil {
			return err
		}
	}
	if err := source.deleteState(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	if err := m.configEditableLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	if _, ok := m.specs[id]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("observable: unknown id %q", id)
	}
	cfg := FileConfig{Observables: make([]Spec, 0, len(m.specs))}
	for _, spec := range m.cfg.Observables {
		if spec.ID != id {
			cfg.Observables = append(cfg.Observables, spec)
		}
	}
	if err := SaveConfig(m.opts.ConfigPath, cfg); err != nil {
		m.mu.Unlock()
		return err
	}
	delete(m.specs, id)
	delete(m.sources, id)
	delete(m.deleting, id)
	delete(m.runs, id)
	delete(m.lastStatus, id)
	m.cfg = cfg
	deleted = true
	m.mu.Unlock()
	return nil
}

func (m *Manager) Status() StatusSnapshot {
	if m == nil {
		return StatusSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := StatusSnapshot{Observables: make([]ObservableStatus, 0, len(m.lastStatus))}
	for id, status := range m.lastStatus {
		if source := m.sources[id]; source != nil {
			status = source.statusSnapshot(status)
		}
		if latest, ok := m.latestObservationLocked(id); ok {
			status.LastObservation = latest
		}
		out.Observables = append(out.Observables, status)
	}
	sort.Slice(out.Observables, func(i, j int) bool {
		return out.Observables[i].ID < out.Observables[j].ID
	})
	return out
}

func (m *Manager) Counts() StatusCounts {
	if m == nil {
		return StatusCounts{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := StatusCounts{Configured: len(m.lastStatus)}
	for _, status := range m.lastStatus {
		switch status.State {
		case RunStateRunning:
			counts.Running++
		case RunStateErrored:
			counts.Errors++
		}
	}
	return counts
}

func (m *Manager) StatusByID(id string) (ObservableStatus, error) {
	status, ok := m.Status().ByID(id)
	if !ok {
		return ObservableStatus{}, fmt.Errorf("observable: unknown id %q", id)
	}
	return status, nil
}

func (m *Manager) Observations(filter ObservationFilter) ([]ObservationRecord, error) {
	if m == nil || m.store == nil {
		return nil, nil
	}
	return m.store.ListObservations(filter)
}

func (m *Manager) RecordObservation(record ObservationRecord) (ObservationRecord, error) {
	if m == nil || m.store == nil {
		return ObservationRecord{}, nil
	}
	snapshot := snapshotAttachmentRefs(m.opts.WorkDir, record.Attachments, eventmedia.DefaultMaxEventBytes)
	record.Attachments = snapshot.refs
	record.AttachmentErrors = append(record.AttachmentErrors, snapshot.errors...)
	if len(record.AttachmentErrors) > 0 {
		record.AttachmentState = ObservationAttachmentStateError
	}
	return m.store.RecordObservation(record)
}

func (m *Manager) UpdateObservation(id string, update func(ObservationRecord) ObservationRecord) error {
	if m == nil || m.store == nil {
		return nil
	}
	updated, err := m.updateObservation(id, update)
	if err != nil {
		return err
	}
	m.emitObservation(observationEventType(updated.State), updated, updated.Error)
	return nil
}

func (m *Manager) MarkObservationAttachmentError(id string, messages []string) error {
	if m == nil || m.store == nil || len(messages) == 0 {
		return nil
	}
	updated, err := m.updateObservation(id, func(record ObservationRecord) ObservationRecord {
		record.AttachmentState = ObservationAttachmentStateError
		record.AttachmentErrors = append([]string(nil), messages...)
		record.Error = strings.Join(messages, "; ")
		return record
	})
	if err != nil {
		return err
	}
	m.emitObservation(EventObservationErrored, updated, updated.Error)
	return nil
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.closeMu.Lock()
	if m.closeCompleted {
		err := m.closeErr
		m.closeMu.Unlock()
		return err
	}
	attempt := m.closeAttempt
	if attempt == nil {
		attempt = &closeAttempt{done: make(chan struct{})}
		m.closeAttempt = attempt
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		go m.runCloseAttempt(attempt)
	}
	deferred := m.callbackActive > 0
	m.closeMu.Unlock()
	if deferred {
		return &CloseDeferredError{}
	}
	<-attempt.done
	return attempt.err
}

func (m *Manager) runCloseAttempt(attempt *closeAttempt) {
	complete, err := m.closeSourcesAndDeliveries()
	m.closeMu.Lock()
	attempt.err = err
	attempt.complete = complete
	if complete {
		m.closeCompleted = true
		m.closeErr = err
	} else if m.closeAttempt == attempt {
		m.closeAttempt = nil
	}
	close(attempt.done)
	m.closeMu.Unlock()
}

func (m *Manager) closeSourcesAndDeliveries() (bool, error) {
	var firstErr error
	for {
		m.mu.Lock()
		type claimedRun struct {
			run            *observableRun
			claim          *terminalClaim
			pending        bool
			pendingOutcome terminalOutcome
		}
		claimed := make([]claimedRun, 0, len(m.runs))
		waiting := make([]<-chan struct{}, 0)
		for _, run := range m.runs {
			if run.claim != nil {
				waiting = append(waiting, run.claim.resolved)
				continue
			}
			claim := newTerminalClaim()
			run.claim = claim
			claimed = append(claimed, claimedRun{
				run:            run,
				claim:          claim,
				pending:        run.terminalPending,
				pendingOutcome: run.pendingOutcome,
			})
		}
		m.mu.Unlock()
		if len(claimed) == 0 && len(waiting) == 0 {
			break
		}
		incomplete := false
		for _, item := range claimed {
			result, stopErr := item.run.source.stop(context.Background(), item.run, sourceStopShutdown)
			if !result.Quiesced {
				m.rollbackTerminal(item.run, item.claim)
				incomplete = true
				if firstErr == nil {
					if stopErr != nil {
						firstErr = stopErr
					} else {
						firstErr = fmt.Errorf("observable: source %q did not quiesce", item.run.id)
					}
				}
				continue
			}
			if item.pending {
				if commitErr := m.commitTerminal(item.run, item.claim, item.pendingOutcome, true); commitErr != nil {
					incomplete = true
					if firstErr == nil {
						firstErr = commitErr
					}
					continue
				}
				if stopErr != nil && firstErr == nil {
					firstErr = stopErr
				}
				continue
			}
			m.commitShutdown(item.run, item.claim)
			if stopErr != nil && firstErr == nil {
				firstErr = stopErr
			}
		}
		if incomplete {
			return false, firstErr
		}
		for _, resolved := range waiting {
			<-resolved
		}
	}
	m.mu.Lock()
	workerDone := make([]<-chan struct{}, 0, len(m.workers))
	for run := range m.workers {
		workerDone = append(workerDone, run.sourceDone)
	}
	m.mu.Unlock()
	for _, done := range workerDone {
		<-done
	}
	m.deliveryMu.Lock()
	m.deliveryClosed = true
	m.deliveryMu.Unlock()
	m.deliveryWG.Wait()
	return true, firstErr
}

func (s StatusSnapshot) ByID(id string) (ObservableStatus, bool) {
	for _, status := range s.Observables {
		if status.ID == id {
			return status, true
		}
	}
	return ObservableStatus{}, false
}

func (m *Manager) configEditableLocked() error {
	if len(m.issues) == 0 {
		return nil
	}
	return fmt.Errorf("observable config has %d issue(s); fix invalid entries before editing", len(m.issues))
}

func (m *Manager) recordRun(record RunRecord) error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.AppendRun(record)
}

func (m *Manager) activateRun(run *observableRun, status ObservableStatus) error {
	if m == nil || run == nil {
		return fmt.Errorf("observable: invalid run activation")
	}
	m.mu.Lock()
	if m.runs[run.id] != run || run.claim != nil {
		m.mu.Unlock()
		return fmt.Errorf("observable: run %q is no longer activatable", run.runID)
	}
	if err := m.recordRun(RunRecord{ObservableID: run.id, RunID: run.runID, State: RunStateRunning, PID: status.PID, StartedAt: status.StartedAt}); err != nil {
		m.mu.Unlock()
		return err
	}
	run.state = status
	m.lastStatus[run.id] = status
	m.mu.Unlock()
	return nil
}

func (m *Manager) publishStarted(run *observableRun) error {
	if m == nil || run == nil {
		return fmt.Errorf("observable: invalid started publication")
	}
	m.mu.Lock()
	if m.runs[run.id] != run || run.state.State != RunStateRunning || run.terminalPending {
		m.mu.Unlock()
		return fmt.Errorf("observable: run %q is no longer publishable", run.runID)
	}
	status := run.state
	m.mu.Unlock()
	m.emitObservable(EventObservableStarted, status)
	return nil
}

func (m *Manager) finishRun(run *observableRun, outcome terminalOutcome) (bool, error) {
	if m == nil || run == nil {
		return false, nil
	}
	for {
		m.mu.Lock()
		if m.runs[run.id] != run {
			m.mu.Unlock()
			return false, nil
		}
		if run.terminalPending {
			err := run.completionErr
			m.mu.Unlock()
			return false, err
		}
		if claim := run.claim; claim != nil {
			resolved := claim.resolved
			m.mu.Unlock()
			<-resolved
			continue
		}
		claim := newTerminalClaim()
		run.claim = claim
		m.mu.Unlock()
		err := m.commitTerminal(run, claim, outcome, true)
		return true, err
	}
}

func (m *Manager) claimTerminal(ctx context.Context, id string) (*observableRun, *terminalClaim, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		m.mu.Lock()
		if _, ok := m.lastStatus[id]; !ok {
			m.mu.Unlock()
			return nil, nil, fmt.Errorf("observable: unknown id %q", id)
		}
		run := m.runs[id]
		if run == nil {
			m.mu.Unlock()
			return nil, nil, nil
		}
		if run.claim == nil {
			claim := newTerminalClaim()
			run.claim = claim
			m.mu.Unlock()
			return run, claim, nil
		}
		resolved := run.claim.resolved
		m.mu.Unlock()
		select {
		case <-resolved:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

func (m *Manager) rollbackTerminal(run *observableRun, claim *terminalClaim) {
	m.mu.Lock()
	if m.runs[run.id] == run && run.claim == claim {
		run.claim = nil
	}
	m.mu.Unlock()
	claim.resolve()
}

func (m *Manager) commitTerminal(run *observableRun, claim *terminalClaim, outcome terminalOutcome, emit bool) error {
	status := run.state
	status.State = outcome.State
	status.ExitedAt = m.now()
	status.ExitCode = outcome.ExitCode
	status.LastError = ""
	if outcome.Err != nil {
		status.LastError = outcome.Err.Error()
	}
	m.mu.Lock()
	if m.runs[run.id] != run || run.claim != claim {
		m.mu.Unlock()
		claim.resolve()
		return nil
	}
	m.mu.Unlock()
	persistErr := m.recordRun(RunRecord{ObservableID: run.id, RunID: run.runID, State: status.State, PID: status.PID, StartedAt: status.StartedAt, ExitedAt: status.ExitedAt, ExitCode: status.ExitCode, Error: status.LastError})
	m.mu.Lock()
	if m.runs[run.id] != run || run.claim != claim {
		m.mu.Unlock()
		claim.resolve()
		return persistErr
	}
	if persistErr != nil {
		wrapped := fmt.Errorf("observable: persist terminal run %q: %w", run.runID, persistErr)
		run.terminalPending = true
		run.pendingOutcome = outcome
		run.completionErr = wrapped
		run.claim = nil
		errorStatus := status
		errorStatus.State = RunStateErrored
		errorStatus.LastError = wrapped.Error()
		m.lastStatus[run.id] = errorStatus
		m.mu.Unlock()
		claim.resolve()
		return wrapped
	}
	delete(m.runs, run.id)
	m.lastStatus[run.id] = status
	run.terminalPending = false
	run.pendingOutcome = terminalOutcome{}
	run.terminalDurable = true
	run.completionErr = nil
	if emit {
		run.terminalEvent = observableTerminalEventType(status.State)
		run.terminalStatus = status
	}
	workerCompleted := run.workerCompleted
	m.mu.Unlock()
	claim.resolve()
	if workerCompleted {
		m.finalizeLifecycle(run)
	}
	return nil
}

func observableTerminalEventType(state string) string {
	switch state {
	case RunStateStopped:
		return EventObservableStopped
	case RunStateErrored:
		return EventObservableErrored
	default:
		return EventObservableExited
	}
}

func (m *Manager) commitShutdown(run *observableRun, claim *terminalClaim) {
	m.mu.Lock()
	if m.runs[run.id] == run && run.claim == claim {
		delete(m.runs, run.id)
		run.shutdown = true
	}
	workerCompleted := run.workerCompleted
	m.mu.Unlock()
	claim.resolve()
	if workerCompleted {
		m.finalizeLifecycle(run)
	}
}

func (m *Manager) reportWorkerError(run *observableRun, err error) {
	if m == nil || run == nil || err == nil {
		return
	}
	m.mu.Lock()
	if run.completionReported {
		m.mu.Unlock()
		return
	}
	run.completionReported = true
	run.completionErr = err
	status := m.lastStatus[run.id]
	status.State = RunStateErrored
	status.LastError = err.Error()
	m.lastStatus[run.id] = status
	m.mu.Unlock()
	m.emitObservable(EventObservableErrored, status)
}

func sourceNotQuiescedError(id string, err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("observable: source %q did not quiesce", id)
}

func (m *Manager) recordObservation(record ObservationRecord) (ObservationRecord, bool, error) {
	if m == nil || m.store == nil {
		return ObservationRecord{}, false, nil
	}
	snapshot := snapshotAttachmentRefs(m.opts.WorkDir, record.Attachments, eventmedia.DefaultMaxEventBytes)
	record.Attachments = snapshot.refs
	record.AttachmentErrors = append(record.AttachmentErrors, snapshot.errors...)
	if len(record.AttachmentErrors) > 0 {
		record.AttachmentState = ObservationAttachmentStateError
	}
	return m.store.RecordObservationOnce(record)
}

func (m *Manager) recordedObservations(id, sourceEventPrefix string, limit int) ([]ObservationRecord, error) {
	if m == nil || m.store == nil {
		return nil, nil
	}
	return m.store.RecordedObservationsBySourceEvent(id, sourceEventPrefix, limit)
}

func (m *Manager) submitDelivery(ctx context.Context, record ObservationRecord) bool {
	if m == nil {
		return false
	}
	m.deliveryMu.Lock()
	if m.deliveryClosed {
		m.deliveryMu.Unlock()
		return false
	}
	m.deliveryWG.Add(1)
	m.deliveryMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	deliveryCtx := context.WithoutCancel(ctx)
	go func() {
		defer m.deliveryWG.Done()
		_ = m.deliverObservation(deliveryCtx, record)
	}()
	return true
}

func (m *Manager) latestObservationLocked(id string) (ObservationRecord, bool) {
	if m.store == nil {
		return ObservationRecord{}, false
	}
	records, err := m.store.ListObservations(ObservationFilter{ObservableID: id, Limit: 1})
	if err != nil || len(records) == 0 {
		return ObservationRecord{}, false
	}
	return records[0], true
}

func (m *Manager) deliverObservation(ctx context.Context, record ObservationRecord) error {
	if m == nil {
		return nil
	}
	current := record
	if m.store != nil && record.ID != "" {
		latest, ok, err := m.store.Observation(record.ID)
		if err != nil {
			return err
		}
		if ok {
			current = latest
		}
	}
	if current.State != "" && current.State != ObservationStateRecorded {
		return nil
	}
	m.emitDeliveryObservation(EventObservationRecorded, current, "")
	if m.opts.Deliver != nil {
		var outcome DeliveryOutcome
		var err error
		m.withDeliveryCallback(func() {
			outcome, err = m.opts.Deliver(ctx, current)
		})
		if err != nil {
			outcome = DeliveryOutcome{
				State: ObservationStateDropped,
				Error: err.Error(),
			}
		}
		if outcome.State == "" {
			return err
		}
		outcome, outcomeErr := outcome.normalized(m.now)
		if outcomeErr != nil {
			return outcomeErr
		}
		updated := outcome.apply(current)
		if m.store != nil {
			var updateErr error
			updated, updateErr = m.updateObservation(current.ID, outcome.apply)
			if updateErr != nil {
				return updateErr
			}
		}
		m.emitDeliveryObservation(observationEventType(updated.State), updated, updated.Error)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) withDeliveryCallback(callback func()) {
	if m == nil || callback == nil {
		return
	}
	m.closeMu.Lock()
	m.callbackActive++
	m.closeMu.Unlock()
	defer func() {
		m.closeMu.Lock()
		m.callbackActive--
		m.closeMu.Unlock()
	}()
	callback()
}

func (m *Manager) emitDeliveryObservation(eventType string, record ObservationRecord, errText string) {
	if m == nil || m.opts.Bus == nil {
		return
	}
	m.withDeliveryCallback(func() {
		m.emitObservation(eventType, record, errText)
	})
}

func (m *Manager) isClosed() bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *Manager) updateObservation(id string, update func(ObservationRecord) ObservationRecord) (ObservationRecord, error) {
	if m == nil || m.store == nil {
		return ObservationRecord{}, nil
	}
	var updated ObservationRecord
	err := m.store.UpdateObservation(id, func(record ObservationRecord) ObservationRecord {
		updated = update(record)
		return updated
	})
	return updated, err
}

func (m *Manager) emitObservable(eventType string, status ObservableStatus) {
	if m == nil || m.opts.Bus == nil {
		return
	}
	m.opts.Bus.Emit(events.Event{
		Type: eventType,
		Payload: ObservableEventPayload{
			ID:       status.ID,
			Name:     status.Name,
			State:    status.State,
			RunID:    status.RunID,
			PID:      status.PID,
			ExitCode: status.ExitCode,
			Error:    status.LastError,
		},
	})
}

func (m *Manager) emitObservation(eventType string, record ObservationRecord, errText string) {
	if m == nil || m.opts.Bus == nil {
		return
	}
	m.opts.Bus.Emit(events.Event{
		Type:    eventType,
		Payload: ObservationEventPayload{Observation: record, Error: errText},
	})
}

func observationEventType(state string) string {
	switch state {
	case ObservationStateQueued:
		return EventObservationQueued
	case ObservationStateDelivered:
		return EventObservationDelivered
	case ObservationStateDropped:
		return EventObservationDropped
	default:
		return EventObservationRecorded
	}
}

func (m *Manager) now() time.Time {
	if m != nil && m.opts.Now != nil {
		return m.opts.Now().UTC()
	}
	return time.Now().UTC()
}

func newRunID() string {
	return "run-" + time.Now().UTC().Format("20060102T150405.000000000")
}

func (r *observableRun) closeDone() {
	if r == nil || r.done == nil {
		return
	}
	r.doneOnce.Do(func() {
		if r.manager != nil {
			r.manager.completeWorker(r)
			return
		}
		r.sourceDoneOnce.Do(func() {
			if r.sourceDone != nil {
				close(r.sourceDone)
			}
		})
		r.closeLifecycleDone()
	})
}

func (m *Manager) completeWorker(run *observableRun) {
	if m == nil || run == nil {
		return
	}
	m.mu.Lock()
	run.workerCompleted = true
	delete(m.workers, run)
	run.sourceDoneOnce.Do(func() { close(run.sourceDone) })
	finalizable := run.terminalDurable || run.shutdown
	m.mu.Unlock()
	if finalizable {
		m.finalizeLifecycle(run)
		return
	}
	run.closeLifecycleDone()
}

func (m *Manager) finalizeLifecycle(run *observableRun) {
	if m == nil || run == nil {
		return
	}
	run.finalizeOnce.Do(func() {
		m.mu.Lock()
		eventType := run.terminalEvent
		status := run.terminalStatus
		m.mu.Unlock()
		if eventType != "" {
			m.emitObservable(eventType, status)
		}
		m.mu.Lock()
		run.terminalEvent = ""
		run.terminalStatus = ObservableStatus{}
		if m.slots[run.id] == run {
			delete(m.slots, run.id)
		}
		m.mu.Unlock()
		run.closeLifecycleDone()
	})
}

func (r *observableRun) closeLifecycleDone() {
	if r == nil || r.done == nil {
		return
	}
	r.lifecycleDoneOnce.Do(func() { close(r.done) })
}

func waitRunDone(ctx context.Context, run *observableRun) error {
	if run == nil || run.done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
