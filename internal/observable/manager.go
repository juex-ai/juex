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
	opts       ManagerOptions
	cfg        FileConfig
	issues     []ConfigIssue
	store      *Store
	specs      map[string]Spec
	runs       map[string]*observableRun
	lastStatus map[string]ObservableStatus
	mu         sync.Mutex
	closed     bool
}

type observableRun struct {
	id       string
	runID    string
	spec     Spec
	ctx      context.Context
	cancel   context.CancelFunc
	state    ObservableStatus
	done     chan struct{}
	doneOnce sync.Once
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
		runs:       map[string]*observableRun{},
		lastStatus: map[string]ObservableStatus{},
	}
	for _, spec := range cfg.Observables {
		m.specs[spec.ID] = spec
		m.lastStatus[spec.ID] = statusFromSpec(spec, "stopped")
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
		m.mu.Unlock()
		cancel()
		return nil
	}
	spec, ok := m.specs[id]
	if !ok {
		m.mu.Unlock()
		cancel()
		return fmt.Errorf("observable: unknown id %q", id)
	}
	run := &observableRun{
		id:     id,
		runID:  runID,
		spec:   spec,
		ctx:    runCtx,
		cancel: cancel,
		done:   make(chan struct{}),
		state:  statusFromSpec(spec, RunStateStarting),
	}
	run.state.RunID = runID
	run.state.StartedAt = time.Now().UTC()
	m.runs[id] = run
	m.lastStatus[id] = run.state
	m.mu.Unlock()
	if err := m.recordRun(RunRecord{ObservableID: id, RunID: runID, State: RunStateStarting, StartedAt: run.state.StartedAt}); err != nil {
		m.failReservedRun(run, err)
		return err
	}
	switch spec.SourceType() {
	case SourceTypeSchedule:
		return m.startScheduleRun(ctx, run)
	default:
		return m.startCommandRun(ctx, runCtx, run)
	}
}

func (m *Manager) startCommandRun(ctx context.Context, runCtx context.Context, run *observableRun) error {
	commandSpec, _ := run.spec.commandRuntime()
	r := newRunner(runnerOptions{
		spec:          commandSpec,
		runID:         run.runID,
		workDir:       m.opts.WorkDir,
		sandboxPolicy: m.opts.Sandbox,
		sandboxRunner: m.opts.SandboxRunner,
		store:         m.store,
		deliver: func(ctx context.Context, record ObservationRecord) (DeliveryOutcome, error) {
			return DeliveryOutcome{}, m.deliverObservation(ctx, record)
		},
	})
	cmd, err := r.start(ctx, runCtx)
	if err != nil {
		run.cancel()
		status := statusFromSpec(run.spec, RunStateErrored)
		status.RunID = run.runID
		status.StartedAt = run.state.StartedAt
		status.ExitedAt = time.Now().UTC()
		status.LastError = err.Error()
		m.setStatus(run.id, status)
		_ = m.recordRun(RunRecord{ObservableID: run.id, RunID: run.runID, State: RunStateErrored, StartedAt: status.StartedAt, ExitedAt: status.ExitedAt, Error: err.Error()})
		m.mu.Lock()
		delete(m.runs, run.id)
		m.mu.Unlock()
		m.emitObservable(EventObservableErrored, status)
		run.closeDone()
		return err
	}
	run.state.State = RunStateRunning
	run.state.PID = cmd.Process.Pid
	m.setStatus(run.id, run.state)
	if err := m.recordRun(RunRecord{ObservableID: run.id, RunID: run.runID, State: RunStateRunning, PID: cmd.Process.Pid, StartedAt: run.state.StartedAt}); err != nil {
		m.cleanupStartedRun(run, r, err)
		return err
	}
	m.emitObservable(EventObservableStarted, run.state)
	go m.waitRun(run, r)
	return nil
}

func (m *Manager) startScheduleRun(ctx context.Context, run *observableRun) error {
	run.state.State = RunStateRunning
	m.setStatus(run.id, run.state)
	if err := m.recordRun(RunRecord{ObservableID: run.id, RunID: run.runID, State: RunStateRunning, StartedAt: run.state.StartedAt}); err != nil {
		m.failReservedRun(run, err)
		return err
	}
	if err := m.evaluateScheduleStartup(ctx, run); err != nil {
		m.failReservedRun(run, err)
		return err
	}
	m.emitObservable(EventObservableStarted, run.state)
	go m.scheduleLoop(run)
	return nil
}

func (m *Manager) Create(ctx context.Context, spec Spec) (ObservableStatus, error) {
	normalized, err := ValidateSpec(spec)
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
	status := statusFromSpec(normalized, RunStateStopped)
	m.lastStatus[normalized.ID] = status
	m.mu.Unlock()
	if err := m.Start(ctx, normalized.ID); err != nil {
		return m.StatusByID(normalized.ID)
	}
	return m.StatusByID(normalized.ID)
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	_ = ctx
	if m == nil {
		return nil
	}
	m.mu.Lock()
	run := m.runs[id]
	status, ok := m.lastStatus[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("observable: unknown id %q", id)
	}
	if run != nil {
		run.cancel()
	}
	stoppedAt := m.now()
	status.State = RunStateStopped
	status.ExitedAt = stoppedAt
	status.LastError = ""
	m.setStatus(id, status)
	m.mu.Lock()
	delete(m.runs, id)
	m.mu.Unlock()
	if run != nil && run.spec.SourceType() == SourceTypeSchedule {
		if err := m.recordPausedScheduleState(id, stoppedAt); err != nil {
			return err
		}
	}
	if err := m.recordRun(RunRecord{ObservableID: id, RunID: status.RunID, State: RunStateStopped, PID: status.PID, StartedAt: status.StartedAt, ExitedAt: status.ExitedAt}); err != nil {
		return err
	}
	m.emitObservable(EventObservableStopped, status)
	waitRunDone(run, 2*time.Second)
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
	if _, ok := m.specs[id]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("observable: unknown id %q", id)
	}
	m.mu.Unlock()
	_ = m.Stop(ctx, id)
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
	if m.store != nil {
		if err := m.store.ClearScheduleState(id); err != nil {
			m.mu.Unlock()
			return err
		}
		if err := m.store.DropRecordedScheduleObservations(id, "observable deleted"); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	delete(m.specs, id)
	delete(m.runs, id)
	delete(m.lastStatus, id)
	m.cfg = cfg
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
		status = m.statusWithScheduleSnapshot(status)
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
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	runs := make([]*observableRun, 0, len(m.runs))
	for _, run := range m.runs {
		runs = append(runs, run)
	}
	m.mu.Unlock()
	for _, run := range runs {
		if run.cancel != nil {
			run.cancel()
		}
	}
	for _, run := range runs {
		waitRunDone(run, 2*time.Second)
	}
	return nil
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

func (m *Manager) waitRun(run *observableRun, r *runner) {
	defer run.closeDone()
	exitCode, err := r.wait()
	flushed, flushErr := r.flush("exit")
	if flushErr != nil && err == nil {
		err = flushErr
	}
	for _, record := range flushed {
		_ = m.deliverObservation(context.Background(), record)
	}
	m.mu.Lock()
	current := m.runs[run.id]
	status := m.lastStatus[run.id]
	if current != run || status.State == RunStateStopped {
		m.mu.Unlock()
		return
	}
	delete(m.runs, run.id)
	status.State = RunStateExited
	status.ExitedAt = time.Now().UTC()
	status.ExitCode = exitCode
	if err != nil {
		status.LastError = err.Error()
	}
	m.lastStatus[run.id] = status
	m.mu.Unlock()
	_ = m.recordRun(RunRecord{ObservableID: run.id, RunID: run.runID, State: RunStateExited, PID: status.PID, StartedAt: status.StartedAt, ExitedAt: status.ExitedAt, ExitCode: exitCode, Error: status.LastError})
	m.emitObservable(EventObservableExited, status)
	m.notifyOnExit(run, status, err)
}

func (m *Manager) notifyOnExit(run *observableRun, status ObservableStatus, err error) {
	if m == nil || run == nil {
		return
	}
	commandSpec, ok := run.spec.commandRuntime()
	if !ok {
		return
	}
	notify := commandSpec.OnExit.Notify
	if notify == "" || notify == "never" {
		return
	}
	nonzero := err != nil
	if status.ExitCode != nil && *status.ExitCode != 0 {
		nonzero = true
	}
	if notify == "nonzero" && !nonzero {
		return
	}
	severity := "info"
	if nonzero {
		severity = "error"
	}
	when := status.ExitedAt
	if when.IsZero() {
		when = time.Now().UTC()
	}
	content := fmt.Sprintf("observable %s exited", run.id)
	if status.ExitCode != nil {
		content = fmt.Sprintf("%s with code %d", content, *status.ExitCode)
	}
	if err != nil {
		content = fmt.Sprintf("%s: %s", content, err.Error())
	}
	record, recordErr := m.RecordObservation(ObservationRecord{
		ObservableID: run.id,
		RunID:        run.runID,
		Kind:         "observable_exit",
		Severity:     severity,
		WindowStart:  when,
		WindowEnd:    when,
		Content:      content,
		State:        ObservationStateRecorded,
	})
	if recordErr == nil {
		_ = m.deliverObservation(context.Background(), record)
	}
}

func (m *Manager) evaluateScheduleStartup(ctx context.Context, run *observableRun) error {
	if m == nil || run == nil || m.store == nil {
		return nil
	}
	now := m.now()
	state, ok, err := m.store.ScheduleState(run.id)
	if err != nil {
		return err
	}
	if ok && state.Paused {
		return m.recordScheduleState(run.id, now, state.LastEmittedScheduledAt)
	}
	if ok {
		if err := m.recoverRecordedScheduleObservations(ctx, run); err != nil {
			return err
		}
	}
	if !ok || state.LastEvaluatedAt.IsZero() {
		return m.recordScheduleState(run.id, now, time.Time{})
	}
	scheduleSpec, _ := run.spec.scheduleRuntime()
	latest, missed, err := latestMissedScheduledOccurrence(scheduleSpec, state, now)
	if err != nil {
		return err
	}
	if missed && catchUpAllows(scheduleSpec, latest, now) {
		_, emitted, err := m.emitScheduledOccurrence(ctx, run, latest, now)
		if err != nil {
			return err
		}
		if emitted {
			return nil
		}
		return m.recordScheduleState(run.id, now, latest.ScheduledAt)
	}
	if !missed && shouldPreserveIntervalStartupBaseline(scheduleSpec, state) {
		return m.recordScheduleState(run.id, state.LastEvaluatedAt, state.LastEmittedScheduledAt)
	}
	return m.recordScheduleState(run.id, now, state.LastEmittedScheduledAt)
}

func shouldPreserveIntervalStartupBaseline(spec scheduleRuntimeSpec, state ScheduleStateRecord) bool {
	return spec.Interval != nil && !state.LastEvaluatedAt.IsZero() && state.LastEmittedScheduledAt.IsZero()
}

func (m *Manager) recoverRecordedScheduleObservations(ctx context.Context, run *observableRun) error {
	if m == nil || run == nil || m.store == nil || m.opts.Deliver == nil {
		return nil
	}
	records, err := m.store.ListObservations(ObservationFilter{ObservableID: run.id})
	if err != nil {
		return err
	}
	deliverCtx := context.Background()
	if ctx != nil {
		deliverCtx = context.WithoutCancel(ctx)
	}
	prefix := scheduleSourceEventPrefix(run.id)
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		if record.State != ObservationStateRecorded || !strings.HasPrefix(record.SourceEventID, prefix) {
			continue
		}
		_ = m.deliverObservation(deliverCtx, record)
	}
	return nil
}

func (m *Manager) scheduleLoop(run *observableRun) {
	defer run.closeDone()
	scheduleSpec, _ := run.spec.scheduleRuntime()
	for {
		if m.isClosed() {
			return
		}
		state, _, err := m.store.ScheduleState(run.id)
		if err != nil {
			m.finishScheduleRun(run, RunStateErrored, err)
			return
		}
		next, ok, err := nextScheduledOccurrence(scheduleSpec, state, m.now())
		if err != nil {
			m.finishScheduleRun(run, RunStateErrored, err)
			return
		}
		if !ok {
			m.finishScheduleRun(run, RunStateExited, nil)
			return
		}
		delay := time.Until(next.ScheduledAt)
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-run.ctx.Done():
			stopScheduleTimer(timer)
			return
		case <-timer.C:
			if _, _, err := m.emitScheduledOccurrence(context.Background(), run, next, m.now()); err != nil {
				m.finishScheduleRun(run, RunStateErrored, err)
				return
			}
		}
	}
}

func stopScheduleTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (m *Manager) emitScheduledOccurrence(ctx context.Context, run *observableRun, occurrence ScheduledOccurrence, observedAt time.Time) (ObservationRecord, bool, error) {
	if m == nil || run == nil || m.store == nil {
		return ObservationRecord{}, false, nil
	}
	observedAt = normalizeNow(observedAt)
	scheduleSpec, _ := run.spec.scheduleRuntime()
	if existing, ok, err := m.store.FindObservationBySourceEventID(occurrence.SourceEventID); err != nil {
		return ObservationRecord{}, false, err
	} else if ok {
		if err := m.recordScheduleState(run.id, observedAt, occurrence.ScheduledAt); err != nil {
			return existing, false, err
		}
		return existing, false, nil
	}
	record, err := m.RecordObservation(ObservationRecord{
		ObservableID:  run.id,
		RunID:         run.runID,
		SourceEventID: occurrence.SourceEventID,
		Kind:          resolvedKind(scheduleSpec.Observation.Kind),
		Severity:      resolvedSeverity(scheduleSpec.Observation.Severity),
		WindowStart:   occurrence.ScheduledAt,
		WindowEnd:     observedAt,
		Content:       scheduleSpec.Observation.Content,
		Attachments:   append([]eventmedia.AttachmentRef(nil), scheduleSpec.Observation.Attachments...),
		State:         ObservationStateRecorded,
	})
	if err != nil {
		return ObservationRecord{}, false, err
	}
	if err := m.recordScheduleState(run.id, observedAt, occurrence.ScheduledAt); err != nil {
		return record, false, err
	}
	m.deliverScheduledObservation(ctx, record)
	return record, true, nil
}

func (m *Manager) deliverScheduledObservation(ctx context.Context, record ObservationRecord) {
	deliverCtx := context.Background()
	if ctx != nil {
		deliverCtx = context.WithoutCancel(ctx)
	}
	go func() {
		_ = m.deliverObservation(deliverCtx, record)
	}()
}

func (m *Manager) recordScheduleState(id string, evaluatedAt time.Time, emittedAt time.Time) error {
	if m == nil || m.store == nil {
		return nil
	}
	if evaluatedAt.IsZero() {
		evaluatedAt = m.now()
	}
	return m.store.RecordScheduleState(ScheduleStateRecord{
		ObservableID:           id,
		LastEvaluatedAt:        evaluatedAt.UTC(),
		LastEmittedScheduledAt: emittedAt.UTC(),
		UpdatedAt:              m.now(),
	})
}

func (m *Manager) recordPausedScheduleState(id string, pausedAt time.Time) error {
	if m == nil || m.store == nil {
		return nil
	}
	if pausedAt.IsZero() {
		pausedAt = m.now()
	}
	var emittedAt time.Time
	if state, ok, err := m.store.ScheduleState(id); err != nil {
		return err
	} else if ok {
		emittedAt = state.LastEmittedScheduledAt
	}
	return m.store.RecordScheduleState(ScheduleStateRecord{
		ObservableID:           id,
		Paused:                 true,
		LastEvaluatedAt:        pausedAt.UTC(),
		LastEmittedScheduledAt: emittedAt.UTC(),
		UpdatedAt:              m.now(),
	})
}

func (m *Manager) finishScheduleRun(run *observableRun, state string, cause error) {
	if m == nil || run == nil {
		return
	}
	status := run.state
	status.State = state
	status.ExitedAt = m.now()
	if cause != nil {
		status.LastError = cause.Error()
	}
	m.mu.Lock()
	if m.runs[run.id] == run {
		delete(m.runs, run.id)
		m.lastStatus[run.id] = status
	}
	m.mu.Unlock()
	_ = m.recordRun(RunRecord{ObservableID: run.id, RunID: run.runID, State: state, StartedAt: status.StartedAt, ExitedAt: status.ExitedAt, Error: status.LastError})
	if state == RunStateErrored {
		m.emitObservable(EventObservableErrored, status)
	} else {
		m.emitObservable(EventObservableExited, status)
	}
}

func (m *Manager) recordRun(record RunRecord) error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.AppendRun(record)
}

func (m *Manager) setStatus(id string, status ObservableStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastStatus[id] = status
	if run := m.runs[id]; run != nil {
		run.state = status
	}
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
	m.emitObservation(EventObservationRecorded, current, "")
	if m.isClosed() {
		return nil
	}
	if m.opts.Deliver != nil {
		outcome, err := m.opts.Deliver(ctx, current)
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
		m.emitObservation(observationEventType(updated.State), updated, updated.Error)
		if err != nil {
			return err
		}
	}
	return nil
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

func statusFromSpec(spec Spec, state string) ObservableStatus {
	status := ObservableStatus{
		ID:         spec.ID,
		Name:       spec.Name,
		SourceType: spec.SourceType(),
		State:      state,
	}
	if commandSpec, ok := spec.commandRuntime(); ok {
		status.Command = commandSpec.Command
		status.Args = append([]string(nil), commandSpec.Args...)
		status.Streams = append([]string(nil), commandSpec.Streams...)
		status.Batch = commandSpec.Batch
	}
	if scheduleSpec, ok := spec.scheduleRuntime(); ok {
		status.Schedule = &ScheduleStatus{
			Summary:     scheduleSummary(scheduleSpec),
			Timezone:    scheduleSpec.Timezone,
			CatchUpMode: scheduleSpec.CatchUp.Mode,
		}
	}
	return status
}

func (m *Manager) statusWithScheduleSnapshot(status ObservableStatus) ObservableStatus {
	if status.SourceType != SourceTypeSchedule || m == nil || m.store == nil {
		return status
	}
	spec, ok := m.specs[status.ID]
	if !ok || spec.SourceType() != SourceTypeSchedule {
		return status
	}
	scheduleSpec, _ := spec.scheduleRuntime()
	schedule := &ScheduleStatus{
		Summary:     scheduleSummary(scheduleSpec),
		Timezone:    scheduleSpec.Timezone,
		CatchUpMode: scheduleSpec.CatchUp.Mode,
	}
	if state, ok, err := m.store.ScheduleState(status.ID); err == nil && ok {
		if !state.LastEvaluatedAt.IsZero() {
			value := state.LastEvaluatedAt
			schedule.LastEvaluatedAt = &value
		}
		if !state.LastEmittedScheduledAt.IsZero() {
			value := state.LastEmittedScheduledAt
			schedule.LastEmittedScheduledAt = &value
		}
		if next, ok, err := nextScheduledOccurrence(scheduleSpec, state, m.now()); err == nil && ok {
			value := next.ScheduledAt
			schedule.NextOccurrence = &value
		}
	} else if next, ok, err := nextScheduledOccurrence(scheduleSpec, ScheduleStateRecord{}, m.now()); err == nil && ok {
		value := next.ScheduledAt
		schedule.NextOccurrence = &value
	}
	status.Schedule = schedule
	return status
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
	r.doneOnce.Do(func() { close(r.done) })
}

func waitRunDone(run *observableRun, timeout time.Duration) {
	if run == nil || run.done == nil {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-run.done:
	case <-timer.C:
	}
}

func (m *Manager) failReservedRun(run *observableRun, err error) {
	if run == nil {
		return
	}
	if run.cancel != nil {
		run.cancel()
	}
	status := run.state
	status.State = RunStateErrored
	status.ExitedAt = time.Now().UTC()
	if err != nil {
		status.LastError = err.Error()
	}
	m.mu.Lock()
	if m.runs[run.id] == run {
		delete(m.runs, run.id)
		m.lastStatus[run.id] = status
	}
	m.mu.Unlock()
	_ = m.recordRun(RunRecord{ObservableID: run.id, RunID: run.runID, State: RunStateErrored, StartedAt: status.StartedAt, ExitedAt: status.ExitedAt, Error: status.LastError})
	m.emitObservable(EventObservableErrored, status)
	run.closeDone()
}

func (m *Manager) cleanupStartedRun(run *observableRun, r *runner, cause error) {
	if run == nil {
		return
	}
	if run.cancel != nil {
		run.cancel()
	}
	exitCode, waitErr := r.wait()
	flushed, flushErr := r.flush("start_failed")
	for _, record := range flushed {
		_ = m.deliverObservation(context.Background(), record)
	}
	status := run.state
	status.State = RunStateErrored
	status.ExitedAt = time.Now().UTC()
	status.ExitCode = exitCode
	status.LastError = firstErrorText(cause, waitErr, flushErr)
	m.mu.Lock()
	if m.runs[run.id] == run {
		delete(m.runs, run.id)
		m.lastStatus[run.id] = status
	}
	m.mu.Unlock()
	_ = m.recordRun(RunRecord{ObservableID: run.id, RunID: run.runID, State: RunStateErrored, PID: status.PID, StartedAt: status.StartedAt, ExitedAt: status.ExitedAt, ExitCode: exitCode, Error: status.LastError})
	m.emitObservable(EventObservableErrored, status)
	run.closeDone()
}

func firstErrorText(errs ...error) string {
	for _, err := range errs {
		if err != nil {
			return err.Error()
		}
	}
	return ""
}
