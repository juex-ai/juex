package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/errorclass"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	SideSessionToolCreate    = "side_session_create"
	SideSessionToolList      = "side_session_list"
	SideSessionToolStatus    = "side_session_status"
	SideSessionToolSend      = "side_session_send"
	SideSessionToolSubscribe = "side_session_subscribe"
	SideSessionToolStop      = "side_session_stop"

	sideSessionPersistAttempts = 8
)

var (
	ErrSideSessionNotActive     = errors.New("side session is not active")
	ErrSideSessionManagerClosed = errors.New("side session manager is closed")
	ErrSideSessionStopped       = errorclass.WithKind(errorclass.KindTerminated, errors.New("side session stopped"))
)

type SideSessionState string

const (
	SideSessionStateRunning  SideSessionState = "running"
	SideSessionStateIdle     SideSessionState = "idle"
	SideSessionStateStopping SideSessionState = "stopping"
)

type SideSessionStatus struct {
	SessionID         string           `json:"session_id"`
	Alias             string           `json:"alias,omitempty"`
	State             SideSessionState `json:"state"`
	Model             string           `json:"model,omitempty"`
	Subscribed        bool             `json:"subscribed"`
	PendingCount      int              `json:"pending_count"`
	LastTurnID        string           `json:"last_turn_id,omitempty"`
	LastResult        string           `json:"last_result,omitempty"`
	LastError         string           `json:"last_error,omitempty"`
	NotificationError string           `json:"notification_error,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

type sideSessionChildOptions struct {
	Context           context.Context
	Config            config.Config
	Model             string
	UseParentProvider bool
	GoalState         *runtime.GoalStateStore
	Notes             *runtime.NotesStore
	Observables       *observable.Manager
}

type sideSessionFactory func(sideSessionChildOptions) (*App, error)

type managedSideSession struct {
	operationMu      sync.Mutex
	app              *App
	ctx              context.Context
	deliveryCtx      context.Context
	deliveryWait     *sync.WaitGroup
	cancel           context.CancelCauseFunc
	unsubscribeState func()
	done             sync.WaitGroup
	runGeneration    uint64
	resultHandoffs   int

	status SideSessionStatus
}

type sideSessionManager struct {
	parent  *App
	factory sideSessionFactory

	lifecycleMu     sync.RWMutex
	transitionMu    sync.Mutex
	mu              sync.Mutex
	sessions        map[string]*managedSideSession
	closed          bool
	transitioning   bool
	deliveryCtx     context.Context
	deliveryCancel  context.CancelFunc
	deliveryWait    *sync.WaitGroup
	deliveryWriters sync.WaitGroup
	deliveryDone    chan struct{}
	resultHandoffs  map[string]*managedSideSession
	deferred        sync.WaitGroup
	closeOnce       sync.Once
	closeStartErr   error
	cleanupErrMu    sync.Mutex
	cleanupErr      error
	turnSeq         atomic.Uint64
}

func newSideSessionManager(parent *App) *sideSessionManager {
	m := &sideSessionManager{
		parent:         parent,
		sessions:       map[string]*managedSideSession{},
		deliveryDone:   make(chan struct{}),
		resultHandoffs: map[string]*managedSideSession{},
	}
	baseCtx := context.Background()
	if parent != nil && parent.ctx != nil {
		baseCtx = parent.ctx
	}
	m.deliveryCtx, m.deliveryCancel = context.WithCancel(baseCtx)
	m.deliveryWait = &sync.WaitGroup{}
	if parent != nil && parent.sideFactory != nil {
		m.factory = parent.sideFactory
	} else {
		m.factory = m.newChildApp
	}
	return m
}

func (m *sideSessionManager) newChildApp(child sideSessionChildOptions) (*App, error) {
	if m == nil || m.parent == nil {
		return nil, ErrSideSessionManagerClosed
	}
	parent := m.parent
	opts := Options{
		Config:                  child.Config,
		ModelHealth:             parent.Engine.ModelHealth,
		SummaryProvider:         parent.Engine.SummaryProvider,
		SummaryProvenance:       parent.Engine.SummaryProvenance,
		Verbose:                 false,
		Debug:                   parent.debug,
		LogLevel:                parent.logLevel,
		Stderr:                  parent.stderr,
		WorkDir:                 child.Config.WorkDir,
		MCPManager:              parent.mcpManager,
		DisableMCP:              true,
		SessionMode:             SessionModeNewSide,
		AgentRuntime:            &parent.agentRuntime,
		disableSideSessionTools: true,
		disableObservables:      true,
		sharedGoalState:         child.GoalState,
		sharedNotes:             child.Notes,
		sharedObservables:       child.Observables,
		startupContext:          child.Context,
	}
	if child.UseParentProvider {
		opts.Provider = parent.Engine.Provider
		opts.ModelCandidates = append([]runtime.ModelCandidate(nil), parent.Engine.ModelCandidates...)
	}
	return New(opts)
}

func (m *sideSessionManager) Create(ctx context.Context, query, model string, subscribe bool) (SideSessionStatus, error) {
	createCtx, cancelCreate := sideSessionCreateContext(ctx, m.parent.ctx)
	defer cancelCreate()
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return SideSessionStatus{}, err
	}
	if err := createCtx.Err(); err != nil {
		return SideSessionStatus{}, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return SideSessionStatus{}, errors.New("side_session_create requires a non-empty query")
	}
	model = strings.TrimSpace(model)
	useParentProvider := model == ""
	cfg := m.parent.cfg
	if model != "" {
		if err := cfg.ApplyModelOverride(model); err != nil {
			return SideSessionStatus{}, fmt.Errorf("side session model: %w", err)
		}
	} else {
		model = config.ModelRef{ProviderID: cfg.ProviderID, ModelID: cfg.Model}.String()
	}
	state := m.parent.Engine.SessionRuntimeSnapshot()
	type factoryResult struct {
		child *App
		err   error
	}
	resultCh := make(chan factoryResult, 1)
	go func() {
		child, err := m.factory(sideSessionChildOptions{
			Context:           createCtx,
			Config:            cfg,
			Model:             model,
			UseParentProvider: useParentProvider,
			GoalState:         state.GoalState,
			Notes:             state.Notes,
			Observables:       m.parent.obsv,
		})
		resultCh <- factoryResult{child: child, err: err}
	}()
	var child *App
	var err error
	select {
	case result := <-resultCh:
		child, err = result.child, result.err
	case <-createCtx.Done():
		m.deferCleanup(func() {
			result := <-resultCh
			if result.child != nil {
				_ = result.child.CloseAndWait()
			}
		})
		return SideSessionStatus{}, createCtx.Err()
	}
	if err != nil {
		return SideSessionStatus{}, fmt.Errorf("create side session: %w", err)
	}
	if err := createCtx.Err(); err != nil {
		_ = child.CloseAndWait()
		return SideSessionStatus{}, err
	}
	identity, ok := child.SessionIdentity()
	if !ok || session.NormalizeKind(identity.Kind) != session.KindSide {
		_ = child.CloseAndWait()
		return SideSessionStatus{}, errors.New("create side session: child runtime is not a side session")
	}
	if err := createCtx.Err(); err != nil {
		_ = child.CloseAndWait()
		return SideSessionStatus{}, err
	}
	now := time.Now().UTC()
	managedCtx, cancel := context.WithCancelCause(child.ctx)
	managed := &managedSideSession{
		app:          child,
		ctx:          managedCtx,
		deliveryCtx:  m.deliveryCtx,
		deliveryWait: m.deliveryWait,
		cancel:       cancel,
		status: SideSessionStatus{
			SessionID:  identity.ID,
			Alias:      identity.Alias,
			State:      SideSessionStateRunning,
			Model:      model,
			Subscribed: subscribe,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
	}
	managed.unsubscribeState = child.Bus.Subscribe("*", func(event events.Event) {
		if event.Type == "goal.updated" || event.Type == "notes.updated" {
			_ = m.parent.Bus.Emit(event)
		}
	})
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel(ErrSideSessionStopped)
		_ = child.CloseAndWait()
		return SideSessionStatus{}, ErrSideSessionManagerClosed
	}
	m.sessions[identity.ID] = managed
	m.mu.Unlock()

	if err := createCtx.Err(); err != nil {
		m.removeIfCurrent(managed)
		_ = stopManagedSideSession(managed)
		return SideSessionStatus{}, err
	}
	result := child.admitUserTurn(createCtx, userTurnMessage(query, nil), TurnIDFunc(func(string) string { return m.nextTurnID() }))
	if result.Kind != TurnAdmissionStarted || result.Start == nil {
		m.removeIfCurrent(managed)
		_ = stopManagedSideSession(managed)
		if result.Err != nil {
			return SideSessionStatus{}, fmt.Errorf("start side session: %w", result.Err)
		}
		return SideSessionStatus{}, fmt.Errorf("start side session: unexpected admission %q", result.Kind)
	}
	if err := createCtx.Err(); err != nil {
		child.CompleteAdmittedTurn(result.Start.TurnID)
		m.removeIfCurrent(managed)
		_ = stopManagedSideSession(managed)
		return SideSessionStatus{}, err
	}
	if err := m.startRun(createCtx, managed, result.Start); err != nil {
		child.CompleteAdmittedTurn(result.Start.TurnID)
		m.removeIfCurrent(managed)
		_ = stopManagedSideSession(managed)
		return SideSessionStatus{}, err
	}
	return m.snapshot(managed), nil
}

func sideSessionCreateContext(callCtx, parentCtx context.Context) (context.Context, context.CancelFunc) {
	if callCtx == nil {
		callCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(callCtx)
	if parentCtx == nil {
		return ctx, cancel
	}
	stopParent := context.AfterFunc(parentCtx, cancel)
	if parentCtx.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stopParent()
		cancel()
	}
}

func (m *sideSessionManager) List() ([]SideSessionStatus, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	items := make([]SideSessionStatus, 0, len(m.sessions))
	for _, managed := range m.sessions {
		items = append(items, m.snapshotLocked(managed))
	}
	m.mu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].SessionID < items[j].SessionID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items, nil
}

func (m *sideSessionManager) shouldDeferGoalContinuation() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.transitioning {
		return false
	}
	for _, managed := range m.sessions {
		if managed.status.State == SideSessionStateStopping {
			continue
		}
		if managed.resultHandoffs > 0 || (managed.status.Subscribed && managed.status.State == SideSessionStateRunning) {
			return true
		}
	}
	return false
}

func (m *sideSessionManager) Status(id string) (SideSessionStatus, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return SideSessionStatus{}, err
	}
	m.mu.Lock()
	managed := m.sessions[strings.TrimSpace(id)]
	if managed == nil {
		m.mu.Unlock()
		return SideSessionStatus{}, ErrSideSessionNotActive
	}
	status := m.snapshotLocked(managed)
	m.mu.Unlock()
	return status, nil
}

func (m *sideSessionManager) Send(id, message string) (SideSessionStatus, bool, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return SideSessionStatus{}, false, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return SideSessionStatus{}, false, errors.New("side_session_send requires a non-empty message")
	}
	managed, unlock, err := m.lockActive(id)
	if err != nil {
		return SideSessionStatus{}, false, err
	}
	defer unlock()
	result := managed.app.admitUserTurn(
		managed.ctx,
		userTurnMessage(message, nil),
		TurnIDFunc(func(string) string { return m.nextTurnID() }),
	)
	switch result.Kind {
	case TurnAdmissionStarted:
		if err := m.startRun(managed.ctx, managed, result.Start); err != nil {
			managed.app.CompleteAdmittedTurn(result.Start.TurnID)
			return SideSessionStatus{}, false, err
		}
		return m.snapshot(managed), false, nil
	case TurnAdmissionQueued:
		return m.snapshot(managed), true, nil
	case TurnAdmissionRejected, TurnAdmissionConflict, TurnAdmissionError:
		if result.Err != nil {
			return SideSessionStatus{}, false, result.Err
		}
		return SideSessionStatus{}, false, errors.New(result.Error.Message)
	default:
		return SideSessionStatus{}, false, fmt.Errorf("side session send: unexpected admission %q", result.Kind)
	}
}

func (m *sideSessionManager) Subscribe(id string, subscribed bool) (SideSessionStatus, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return SideSessionStatus{}, err
	}
	managed, unlock, err := m.lockActive(id)
	if err != nil {
		return SideSessionStatus{}, err
	}
	defer unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	managed.status.Subscribed = subscribed
	managed.status.UpdatedAt = time.Now().UTC()
	return m.snapshotLocked(managed), nil
}

func (m *sideSessionManager) Stop(ctx context.Context, id string) error {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return err
	}
	managed, unlock, err := m.lockActive(id)
	if err != nil {
		return err
	}
	defer unlock()
	if !m.removeCurrent(managed) {
		return ErrSideSessionNotActive
	}
	return stopManagedSideSessionContext(ctx, managed, &m.deferred)
}

func (m *sideSessionManager) StopAll() error {
	if m == nil {
		return nil
	}
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	items, deliveries, err := m.beginTransition(false)
	if err != nil {
		return err
	}
	defer m.finishTransition()
	return m.drainTransitionContext(context.Background(), items, deliveries)
}

func (m *sideSessionManager) replacePrimary(ctx context.Context, replace func() error) error {
	if m == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return replace()
	}
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	items, deliveries, err := m.beginTransition(false)
	if err != nil {
		return err
	}
	defer m.finishTransition()
	if err := m.drainTransitionContext(ctx, items, deliveries); err != nil {
		return fmt.Errorf("close managed side sessions: %w", err)
	}
	if err := m.parent.ctx.Err(); err != nil {
		return ErrSideSessionManagerClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return replace()
}

func (m *sideSessionManager) beginTransition(closing bool) ([]*managedSideSession, *sync.WaitGroup, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	if m.closed && !closing {
		m.mu.Unlock()
		return nil, nil, ErrSideSessionManagerClosed
	}
	if closing {
		m.closed = true
	}
	m.transitioning = true
	if m.deliveryCancel != nil {
		m.deliveryCancel()
	}
	deliveries := m.deliveryWait
	items := make([]*managedSideSession, 0, len(m.sessions))
	for id, managed := range m.sessions {
		managed.status.State = SideSessionStateStopping
		managed.status.Subscribed = false
		managed.resultHandoffs = 0
		items = append(items, managed)
		delete(m.sessions, id)
	}
	clear(m.resultHandoffs)
	m.mu.Unlock()
	return items, deliveries, nil
}

func (m *sideSessionManager) drainTransitionContext(ctx context.Context, items []*managedSideSession, deliveries *sync.WaitGroup) error {
	var result error
	for _, managed := range items {
		result = errors.Join(result, stopManagedSideSessionContext(ctx, managed, &m.deferred))
	}
	if deliveries == nil {
		return result
	}
	done := make(chan struct{})
	go func() {
		deliveries.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		m.deferCleanup(func() { <-done })
		result = errors.Join(result, ctx.Err())
	}
	return result
}

func (m *sideSessionManager) finishTransition() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	if !m.closed {
		baseCtx := context.Background()
		if m.parent != nil && m.parent.ctx != nil {
			baseCtx = m.parent.ctx
		}
		m.deliveryCtx, m.deliveryCancel = context.WithCancel(baseCtx)
		m.deliveryWait = &sync.WaitGroup{}
	}
	m.transitioning = false
	m.mu.Unlock()
}

func (m *sideSessionManager) Close() error {
	if m == nil {
		return nil
	}
	return errors.Join(m.StartClose(), m.WaitClose())
}

// StartClose cancels owned work and schedules final child cleanup without
// waiting. App cleanup can therefore release the Primary session resources
// before a provider that ignores cancellation finally returns.
func (m *sideSessionManager) StartClose() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.transitionMu.Lock()
		defer m.transitionMu.Unlock()
		items, _, err := m.beginTransition(true)
		if err != nil {
			m.closeStartErr = err
			return
		}
		defer m.finishTransition()
		for _, managed := range items {
			cancelManagedSideSession(managed)
			m.deferCleanupError(func() error {
				managed.done.Wait()
				return managed.app.CloseAndWait()
			})
		}
		m.deferCleanup(func() {
			m.deliveryWriters.Wait()
			close(m.deliveryDone)
		})
	})
	return m.closeStartErr
}

// WaitDeliveryWriters waits until no Side Session result can write the owning
// Primary Session directory. Child runtimes may still be draining.
func (m *sideSessionManager) WaitDeliveryWriters(ctx context.Context) error {
	if m == nil || m.deliveryDone == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.deliveryDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitClose joins cleanup previously started by StartClose.
func (m *sideSessionManager) WaitClose() error {
	if m == nil {
		return nil
	}
	m.deferred.Wait()
	m.cleanupErrMu.Lock()
	defer m.cleanupErrMu.Unlock()
	return m.cleanupErr
}

func (m *sideSessionManager) startRun(ctx context.Context, managed *managedSideSession, start *AdmittedTurn) error {
	if start == nil {
		return errors.New("side session run: missing admitted turn")
	}
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return err
	}
	if current := m.sessions[managed.status.SessionID]; current != managed || managed.status.State == SideSessionStateStopping {
		m.mu.Unlock()
		return ErrSideSessionNotActive
	}
	managed.runGeneration++
	generation := managed.runGeneration
	managed.status.State = SideSessionStateRunning
	managed.status.UpdatedAt = time.Now().UTC()
	managed.done.Add(1)
	m.mu.Unlock()
	m.run(managed, generation, start.TurnID, start.Message)
	return nil
}

func (m *sideSessionManager) run(managed *managedSideSession, generation uint64, turnID string, message llm.Message) {
	go func() {
		defer managed.done.Done()
		out, err := managed.app.RunAdmittedTurn(managed.ctx, turnID, message)
		managed.app.CompleteAdmittedTurn(turnID)

		m.mu.Lock()
		current := m.sessions[managed.status.SessionID]
		if current != managed || managed.status.State == SideSessionStateStopping || managed.runGeneration != generation {
			m.mu.Unlock()
			return
		}
		managed.status.State = SideSessionStateIdle
		managed.status.LastTurnID = turnID
		managed.status.LastResult = out
		managed.status.LastError = ""
		managed.status.NotificationError = ""
		if err != nil {
			managed.status.LastError = err.Error()
		}
		managed.status.PendingCount = managed.app.PendingInputStatus().PendingCount
		managed.status.UpdatedAt = time.Now().UTC()
		subscribed := managed.status.Subscribed
		status := managed.status
		deliveryWait := managed.deliveryWait
		handoffID := "side-session-result:" + status.SessionID + ":" + status.LastTurnID
		if subscribed && deliveryWait != nil {
			deliveryWait.Add(1)
			m.deliveryWriters.Add(1)
			managed.resultHandoffs++
			m.resultHandoffs[handoffID] = managed
		}
		m.mu.Unlock()
		if subscribed && deliveryWait != nil {
			go func() {
				defer deliveryWait.Done()
				defer m.deliveryWriters.Done()
				m.deliverResult(managed.deliveryCtx, managed, status, handoffID)
			}()
		}
	}()
}

func (m *sideSessionManager) finishResultHandoffs(ids []string) {
	if m == nil || len(ids) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		managed := m.resultHandoffs[id]
		if managed == nil {
			continue
		}
		delete(m.resultHandoffs, id)
		if managed.resultHandoffs > 0 {
			managed.resultHandoffs--
		}
	}
}

func (m *sideSessionManager) deliverResult(ctx context.Context, managed *managedSideSession, status SideSessionStatus, handoffID string) {
	finishOnReturn := true
	defer func() {
		if finishOnReturn {
			m.finishResultHandoffs([]string{handoffID})
		}
	}()
	if err := m.ensureParentActive(); err != nil {
		return
	}
	payload := map[string]any{
		"session_id": status.SessionID,
		"turn_id":    status.LastTurnID,
		"model":      status.Model,
		"status":     "completed",
		"output":     status.LastResult,
	}
	if status.LastError != "" {
		payload["status"] = "failed"
		payload["error"] = status.LastError
		delete(payload, "output")
	}
	data, _ := json.Marshal(payload)
	msg := llm.TextMessage(llm.RoleUser, "Side Session result:\n"+string(data))
	msg.Kind = llm.MessageKindSideSession
	var record runtime.PendingInputRecord
	delay := 50 * time.Millisecond
	var persistErr error
	for attempt := 0; attempt < sideSessionPersistAttempts; attempt++ {
		var err error
		record, err = m.parent.Engine.PersistPendingMessageWithOptions(ctx, msg, runtime.PendingInputOptions{
			ID:  handoffID,
			TTL: m.parent.Engine.ExternalEventTTL,
		})
		if err == nil {
			persistErr = nil
			break
		}
		persistErr = err
		if attempt+1 == sideSessionPersistAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
			if delay < time.Second {
				delay *= 2
				if delay > time.Second {
					delay = time.Second
				}
			}
		}
	}
	if persistErr != nil {
		m.recordNotificationFailure(managed, status, persistErr)
		return
	}
	if record.State == runtime.PendingInputStateProcessed || record.State == runtime.PendingInputStateExpired || record.State == runtime.PendingInputStateDropped {
		return
	}

	admissionErrorAttempts := 0
	admissionErrorDelay := 50 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			m.dropStaleNotification(managed, status, record.ID)
			return
		}
		if err := m.ensureParentActive(); err != nil {
			m.dropStaleNotification(managed, status, record.ID)
			return
		}
		result := m.parent.admitPersistedUserTurn(ctx, record, TurnIDFunc(func(string) string { return m.nextTurnID() }))
		switch result.Kind {
		case TurnAdmissionQueued:
			finishOnReturn = false
			return
		case TurnAdmissionStarted:
			m.finishResultHandoffs([]string{handoffID})
			_, runErr := m.parent.RunAdmittedTurn(ctx, result.Start.TurnID, result.Start.Message)
			m.parent.CompleteAdmittedTurn(result.Start.TurnID)
			if runErr != nil {
				current, ok, stateErr := m.parent.Engine.PersistedPendingMessage(record.ID)
				if stateErr != nil {
					dropErr := m.dropPersistedNotification(record.ID)
					m.recordNotificationFailure(managed, status, errors.Join(runErr, stateErr, dropErr))
					return
				}
				if ok && (current.State == runtime.PendingInputStatePending || current.State == runtime.PendingInputStateAdmitted) {
					dropErr := m.dropPersistedNotification(record.ID)
					m.recordNotificationFailure(managed, status, errors.Join(runErr, dropErr))
				}
			}
			return
		case TurnAdmissionRejected:
			if !errors.Is(result.Err, runtime.ErrPendingInputQueueFull) {
				return
			}
		case TurnAdmissionConflict:
			// A turn can close between admission snapshots. Retry while the
			// subscription and owning App are still active.
		case TurnAdmissionError:
			if errors.Is(result.Err, runtime.ErrPendingInputExpired) {
				m.recordNotificationFailure(managed, status, result.Err)
				return
			}
			if errors.Is(result.Err, runtime.ErrPendingInputHandled) {
				return
			}
			admissionErr := result.Err
			if admissionErr == nil {
				message := strings.TrimSpace(result.Error.Message)
				if message == "" {
					message = "unknown persisted admission error"
				}
				admissionErr = errors.New(message)
			}
			admissionErrorAttempts++
			if admissionErrorAttempts >= sideSessionPersistAttempts {
				m.recordNotificationFailure(managed, status, fmt.Errorf("admit persisted side session notification: %w", admissionErr))
				return
			}
		default:
			return
		}
		delay := 100 * time.Millisecond
		if result.Kind == TurnAdmissionError {
			delay = admissionErrorDelay
			if admissionErrorDelay < time.Second {
				admissionErrorDelay *= 2
				if admissionErrorDelay > time.Second {
					admissionErrorDelay = time.Second
				}
			}
		}
		select {
		case <-ctx.Done():
			m.dropStaleNotification(managed, status, record.ID)
			return
		case <-time.After(delay):
		}
	}
}

func (m *sideSessionManager) dropStaleNotification(managed *managedSideSession, status SideSessionStatus, id string) {
	if err := m.dropPersistedNotification(id); err != nil {
		m.recordNotificationFailure(managed, status, fmt.Errorf("drop stale side session notification: %w", err))
	}
}

func (m *sideSessionManager) dropPersistedNotification(id string) error {
	delay := 50 * time.Millisecond
	var result error
	for attempt := 0; attempt < sideSessionPersistAttempts; attempt++ {
		result = m.parent.Engine.DropPersistedPendingMessage(id)
		if result == nil {
			return nil
		}
		if attempt+1 < sideSessionPersistAttempts {
			time.Sleep(delay)
			if delay < time.Second {
				delay *= 2
				if delay > time.Second {
					delay = time.Second
				}
			}
		}
	}
	return result
}

func (m *sideSessionManager) recordNotificationFailure(managed *managedSideSession, status SideSessionStatus, err error) {
	m.mu.Lock()
	if current := m.sessions[status.SessionID]; current == managed && managed.status.LastTurnID == status.LastTurnID {
		managed.status.NotificationError = err.Error()
		managed.status.UpdatedAt = time.Now().UTC()
	}
	m.mu.Unlock()
	_ = m.parent.Bus.Emit(events.Event{
		Type:          "side_session.notification_failed",
		SchemaVersion: 1,
		ReplayPolicy:  events.ReplayIgnorable,
		TurnID:        status.LastTurnID,
		Payload: map[string]any{
			"session_id": status.SessionID,
			"error":      err.Error(),
		},
	})
}

func (m *sideSessionManager) lockActive(id string) (*managedSideSession, func(), error) {
	m.mu.Lock()
	managed := m.sessions[strings.TrimSpace(id)]
	if managed == nil || managed.status.State == SideSessionStateStopping {
		m.mu.Unlock()
		return nil, nil, ErrSideSessionNotActive
	}
	m.mu.Unlock()
	managed.operationMu.Lock()
	m.mu.Lock()
	current := m.sessions[managed.status.SessionID]
	active := current == managed && managed.status.State != SideSessionStateStopping
	m.mu.Unlock()
	if !active {
		managed.operationMu.Unlock()
		return nil, nil, ErrSideSessionNotActive
	}
	return managed, managed.operationMu.Unlock, nil
}

func (m *sideSessionManager) removeCurrent(managed *managedSideSession) bool {
	if managed == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := managed.status.SessionID
	if m.sessions[id] != managed || managed.status.State == SideSessionStateStopping {
		return false
	}
	managed.status.State = SideSessionStateStopping
	managed.status.Subscribed = false
	managed.status.UpdatedAt = time.Now().UTC()
	for handoffID, owner := range m.resultHandoffs {
		if owner == managed {
			delete(m.resultHandoffs, handoffID)
		}
	}
	managed.resultHandoffs = 0
	delete(m.sessions, id)
	return true
}

func (m *sideSessionManager) removeIfCurrent(managed *managedSideSession) {
	if managed == nil {
		return
	}
	m.mu.Lock()
	if m.sessions[managed.status.SessionID] == managed {
		delete(m.sessions, managed.status.SessionID)
	}
	m.mu.Unlock()
}

func stopManagedSideSession(managed *managedSideSession) error {
	return stopManagedSideSessionContext(context.Background(), managed, nil)
}

func stopManagedSideSessionContext(ctx context.Context, managed *managedSideSession, deferred *sync.WaitGroup) error {
	if managed == nil || managed.app == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cancelManagedSideSession(managed)
	done := make(chan struct{})
	go func() {
		managed.done.Wait()
		close(done)
	}()
	select {
	case <-done:
		return managed.app.CloseAndWait()
	case <-ctx.Done():
		if deferred != nil {
			deferred.Add(1)
		}
		go func() {
			if deferred != nil {
				defer deferred.Done()
			}
			<-done
			_ = managed.app.CloseAndWait()
		}()
		return ctx.Err()
	}
}

func cancelManagedSideSession(managed *managedSideSession) {
	if managed == nil || managed.app == nil {
		return
	}
	managed.cancel(ErrSideSessionStopped)
	if managed.unsubscribeState != nil {
		managed.unsubscribeState()
		managed.unsubscribeState = nil
	}
	managed.app.CancelActiveTurn(ErrSideSessionStopped)
}

func (m *sideSessionManager) deferCleanup(cleanup func()) {
	if cleanup == nil {
		return
	}
	m.deferred.Add(1)
	go func() {
		defer m.deferred.Done()
		cleanup()
	}()
}

func (m *sideSessionManager) deferCleanupError(cleanup func() error) {
	if cleanup == nil {
		return
	}
	m.deferCleanup(func() {
		if err := cleanup(); err != nil {
			m.cleanupErrMu.Lock()
			m.cleanupErr = errors.Join(m.cleanupErr, err)
			m.cleanupErrMu.Unlock()
		}
	})
}

func (m *sideSessionManager) snapshot(managed *managedSideSession) SideSessionStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked(managed)
}

func (m *sideSessionManager) snapshotLocked(managed *managedSideSession) SideSessionStatus {
	status := managed.status
	if managed.app != nil {
		status.PendingCount = managed.app.PendingInputStatus().PendingCount
	}
	return status
}

func (m *sideSessionManager) ensureParentActive() error {
	if m == nil || m.parent == nil {
		return ErrSideSessionManagerClosed
	}
	m.mu.Lock()
	closed := m.closed
	transitioning := m.transitioning
	m.mu.Unlock()
	if closed {
		return ErrSideSessionManagerClosed
	}
	if transitioning {
		return errors.New("side session manager is changing primary session")
	}
	identity, ok := m.parent.SessionIdentity()
	if !ok || session.NormalizeKind(identity.Kind) != session.KindPrimary || !identity.Active {
		return errors.New("side session tools require an active primary session")
	}
	activeID, ok, err := ActivePrimarySessionID(m.parent.cfg)
	if err != nil {
		return err
	}
	if !ok || activeID != identity.ID {
		return errors.New("side session tools require the workspace active primary session")
	}
	return nil
}

func (m *sideSessionManager) nextTurnID() string {
	event := events.Normalize(events.Event{Type: "side_session.turn"})
	return fmt.Sprintf("side-%s-%d", event.ID, m.turnSeq.Add(1))
}

func RegisterSideSessionTools(reg *tools.Registry, manager *sideSessionManager) error {
	if reg == nil || manager == nil {
		return nil
	}
	definitions := SideSessionToolDefinitions()
	handlers := []tools.Handler{
		func(ctx context.Context, input map[string]any) (string, error) {
			subscribe := true
			if raw, ok := input["subscribe"]; ok {
				value, valid := raw.(bool)
				if !valid {
					return "", errors.New("subscribe must be a boolean")
				}
				subscribe = value
			}
			status, err := manager.Create(ctx, toolString(input, "query"), toolString(input, "model"), subscribe)
			return marshalSideToolResult(status, err)
		},
		func(_ context.Context, _ map[string]any) (string, error) {
			items, err := manager.List()
			return marshalSideToolResult(map[string]any{"sessions": items}, err)
		},
		func(_ context.Context, input map[string]any) (string, error) {
			status, err := manager.Status(toolString(input, "session_id"))
			return marshalSideToolResult(status, err)
		},
		func(_ context.Context, input map[string]any) (string, error) {
			status, queued, err := manager.Send(toolString(input, "session_id"), toolString(input, "message"))
			if err != nil {
				return "", err
			}
			return marshalSideToolResult(map[string]any{
				"session_id":    status.SessionID,
				"state":         status.State,
				"queued":        queued,
				"pending_count": status.PendingCount,
			}, nil)
		},
		func(_ context.Context, input map[string]any) (string, error) {
			subscribed, ok := input["subscribed"].(bool)
			if !ok {
				return "", errors.New("subscribed must be a boolean")
			}
			status, err := manager.Subscribe(toolString(input, "session_id"), subscribed)
			return marshalSideToolResult(status, err)
		},
		func(ctx context.Context, input map[string]any) (string, error) {
			id := toolString(input, "session_id")
			if err := manager.Stop(ctx, id); err != nil {
				return "", err
			}
			return marshalSideToolResult(map[string]any{"session_id": id, "stopped": true}, nil)
		},
	}
	for i, definition := range definitions {
		if err := reg.Register(definition.Bind(handlers[i])); err != nil {
			return err
		}
	}
	return nil
}

func SideSessionToolDefinitions() []tools.ToolDefinition {
	id := map[string]any{"type": "string"}
	return []tools.ToolDefinition{
		{
			Name:        SideSessionToolCreate,
			Group:       tools.ToolGroupSideSession,
			Description: "Create a managed Side Session and start its query asynchronously. Results are subscribed by default.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":     map[string]any{"type": "string"},
					"model":     map[string]any{"type": "string"},
					"subscribe": map[string]any{"type": "boolean"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        SideSessionToolList,
			Group:       tools.ToolGroupSideSession,
			Description: "List Side Sessions currently managed by this Primary Session.",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        SideSessionToolStatus,
			Group:       tools.ToolGroupSideSession,
			Description: "Read the runtime status and latest result of an active managed Side Session.",
			Schema: map[string]any{
				"type": "object", "properties": map[string]any{"session_id": id}, "required": []string{"session_id"},
			},
		},
		{
			Name:        SideSessionToolSend,
			Group:       tools.ToolGroupSideSession,
			Description: "Send a message to a managed Side Session; busy Sessions queue it as durable pending input.",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"session_id": id, "message": map[string]any{"type": "string"}},
				"required":   []string{"session_id", "message"},
			},
		},
		{
			Name:        SideSessionToolSubscribe,
			Group:       tools.ToolGroupSideSession,
			Description: "Enable or disable terminal result notifications for a managed Side Session.",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"session_id": id, "subscribed": map[string]any{"type": "boolean"}},
				"required":   []string{"session_id", "subscribed"},
			},
		},
		{
			Name:        SideSessionToolStop,
			Group:       tools.ToolGroupSideSession,
			Description: "Stop and close an active managed Side Session while preserving its durable history.",
			Schema: map[string]any{
				"type": "object", "properties": map[string]any{"session_id": id}, "required": []string{"session_id"},
			},
		},
	}
}

func toolString(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return strings.TrimSpace(value)
}

func marshalSideToolResult(value any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
