package observable

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/config"
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
	Deliver       func(context.Context, ObservationRecord) error
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
	id     string
	runID  string
	cancel context.CancelFunc
	state  ObservableStatus
	done   chan struct{}
}

type StatusSnapshot struct {
	Observables []ObservableStatus `json:"observables"`
}

type ObservableStatus struct {
	ID              string            `json:"id"`
	Name            string            `json:"name,omitempty"`
	Command         string            `json:"command"`
	Args            []string          `json:"args,omitempty"`
	Streams         []string          `json:"streams,omitempty"`
	Batch           BatchSpec         `json:"batch"`
	State           string            `json:"state"`
	RunID           string            `json:"run_id,omitempty"`
	PID             int               `json:"pid,omitempty"`
	StartedAt       time.Time         `json:"started_at,omitempty"`
	ExitedAt        time.Time         `json:"exited_at,omitempty"`
	ExitCode        *int              `json:"exit_code,omitempty"`
	LastError       string            `json:"last_error,omitempty"`
	LastObservation ObservationRecord `json:"last_observation,omitempty"`
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
		store:      NewStore(opts.StateDir, StoreOptions{}),
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
	spec, err := m.specForStart(id)
	if err != nil {
		return err
	}
	if spec.Command == "" {
		return nil
	}
	runID := newRunID()
	runCtx, cancel := context.WithCancel(context.Background())
	run := &observableRun{
		id:     id,
		runID:  runID,
		cancel: cancel,
		done:   make(chan struct{}),
		state:  statusFromSpec(spec, RunStateStarting),
	}
	run.state.RunID = runID
	run.state.StartedAt = time.Now().UTC()
	if err := m.recordRun(RunRecord{ObservableID: id, RunID: runID, State: RunStateStarting, StartedAt: run.state.StartedAt}); err != nil {
		cancel()
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return fmt.Errorf("observable: manager closed")
	}
	m.runs[id] = run
	m.lastStatus[id] = run.state
	m.mu.Unlock()

	r := newRunner(runnerOptions{
		spec:          spec,
		runID:         runID,
		workDir:       m.opts.WorkDir,
		sandboxPolicy: m.opts.Sandbox,
		sandboxRunner: m.opts.SandboxRunner,
		store:         m.store,
		deliver:       m.deliverObservation,
	})
	cmd, err := r.start(ctx, runCtx)
	if err != nil {
		cancel()
		status := statusFromSpec(spec, RunStateErrored)
		status.RunID = runID
		status.StartedAt = run.state.StartedAt
		status.ExitedAt = time.Now().UTC()
		status.LastError = err.Error()
		m.setStatus(id, status)
		_ = m.recordRun(RunRecord{ObservableID: id, RunID: runID, State: RunStateErrored, StartedAt: status.StartedAt, ExitedAt: status.ExitedAt, Error: err.Error()})
		m.mu.Lock()
		delete(m.runs, id)
		m.mu.Unlock()
		m.emitObservable(EventObservableErrored, status)
		return err
	}
	run.state.State = RunStateRunning
	run.state.PID = cmd.Process.Pid
	m.setStatus(id, run.state)
	if err := m.recordRun(RunRecord{ObservableID: id, RunID: runID, State: RunStateRunning, PID: cmd.Process.Pid, StartedAt: run.state.StartedAt}); err != nil {
		cancel()
		return err
	}
	m.emitObservable(EventObservableStarted, run.state)
	go m.waitRun(run, r)
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
	status.State = RunStateStopped
	status.ExitedAt = time.Now().UTC()
	status.LastError = ""
	m.setStatus(id, status)
	m.mu.Lock()
	delete(m.runs, id)
	m.mu.Unlock()
	if err := m.recordRun(RunRecord{ObservableID: id, RunID: status.RunID, State: RunStateStopped, PID: status.PID, StartedAt: status.StartedAt, ExitedAt: status.ExitedAt}); err != nil {
		return err
	}
	m.emitObservable(EventObservableStopped, status)
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
		if run.done != nil {
			select {
			case <-run.done:
			case <-time.After(2 * time.Second):
			}
		}
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

func (m *Manager) specForStart(id string) (Spec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Spec{}, fmt.Errorf("observable: manager closed")
	}
	if run := m.runs[id]; run != nil {
		return Spec{}, nil
	}
	spec, ok := m.specs[id]
	if !ok {
		return Spec{}, fmt.Errorf("observable: unknown id %q", id)
	}
	return spec, nil
}

func (m *Manager) configEditableLocked() error {
	if len(m.issues) == 0 {
		return nil
	}
	return fmt.Errorf("observable config has %d issue(s); fix invalid entries before editing", len(m.issues))
}

func (m *Manager) waitRun(run *observableRun, r *runner) {
	defer close(run.done)
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
	m.emitObservation(EventObservationRecorded, record, "")
	if m.opts.Deliver != nil {
		if err := m.opts.Deliver(ctx, record); err != nil {
			updated, updateErr := m.updateObservation(record.ID, func(record ObservationRecord) ObservationRecord {
				record.State = ObservationStateDropped
				record.Error = err.Error()
				return record
			})
			if updateErr == nil {
				m.emitObservation(EventObservationDropped, updated, err.Error())
			} else {
				m.emitObservation(EventObservationDropped, record, err.Error())
			}
			return err
		}
	}
	return nil
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
	return ObservableStatus{
		ID:      spec.ID,
		Name:    spec.Name,
		Command: spec.Command,
		Args:    append([]string(nil), spec.Args...),
		Streams: append([]string(nil), spec.Streams...),
		Batch:   spec.Batch,
		State:   state,
	}
}

func newRunID() string {
	return "run-" + time.Now().UTC().Format("20060102T150405.000000000")
}
