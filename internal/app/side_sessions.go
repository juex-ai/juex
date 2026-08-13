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
	app              *App
	ctx              context.Context
	deliveryCtx      context.Context
	cancel           context.CancelCauseFunc
	unsubscribeState func()
	done             sync.WaitGroup
	runGeneration    uint64

	status SideSessionStatus
}

type sideSessionManager struct {
	parent  *App
	factory sideSessionFactory

	lifecycleMu    sync.RWMutex
	transitionMu   sync.Mutex
	mu             sync.Mutex
	sessions       map[string]*managedSideSession
	closed         bool
	transitioning  bool
	deliveryCtx    context.Context
	deliveryCancel context.CancelFunc
	turnSeq        atomic.Uint64
	deliveries     sync.WaitGroup
}

func newSideSessionManager(parent *App) *sideSessionManager {
	m := &sideSessionManager{
		parent:   parent,
		sessions: map[string]*managedSideSession{},
	}
	baseCtx := context.Background()
	if parent != nil && parent.ctx != nil {
		baseCtx = parent.ctx
	}
	m.deliveryCtx, m.deliveryCancel = context.WithCancel(baseCtx)
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
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return SideSessionStatus{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
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
			Context:           ctx,
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
	case <-ctx.Done():
		go func() {
			result := <-resultCh
			if result.child != nil {
				_ = result.child.CloseAndWait()
			}
		}()
		return SideSessionStatus{}, ctx.Err()
	}
	if err != nil {
		return SideSessionStatus{}, fmt.Errorf("create side session: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = child.CloseAndWait()
		return SideSessionStatus{}, err
	}
	identity, ok := child.SessionIdentity()
	if !ok || session.NormalizeKind(identity.Kind) != session.KindSide {
		_ = child.CloseAndWait()
		return SideSessionStatus{}, errors.New("create side session: child runtime is not a side session")
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithCancelCause(child.ctx)
	managed := &managedSideSession{
		app:         child,
		ctx:         ctx,
		deliveryCtx: m.deliveryCtx,
		cancel:      cancel,
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
			m.parent.Bus.Emit(event)
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

	if err := ctx.Err(); err != nil {
		m.removeIfCurrent(managed)
		_ = stopManagedSideSession(managed)
		return SideSessionStatus{}, err
	}
	result := child.admitUserTurn(ctx, userTurnMessage(query, nil), TurnIDFunc(func(string) string { return m.nextTurnID() }))
	if result.Kind != TurnAdmissionStarted || result.Start == nil {
		m.removeIfCurrent(managed)
		_ = stopManagedSideSession(managed)
		if result.Err != nil {
			return SideSessionStatus{}, fmt.Errorf("start side session: %w", result.Err)
		}
		return SideSessionStatus{}, fmt.Errorf("start side session: unexpected admission %q", result.Kind)
	}
	if err := ctx.Err(); err != nil {
		child.CompleteAdmittedTurn(result.Start.TurnID)
		m.removeIfCurrent(managed)
		_ = stopManagedSideSession(managed)
		return SideSessionStatus{}, err
	}
	if err := m.startRun(ctx, managed, result.Start); err != nil {
		child.CompleteAdmittedTurn(result.Start.TurnID)
		m.removeIfCurrent(managed)
		_ = stopManagedSideSession(managed)
		return SideSessionStatus{}, err
	}
	return m.snapshot(managed), nil
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
	managed, err := m.active(id)
	if err != nil {
		return SideSessionStatus{}, false, err
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	managed := m.sessions[strings.TrimSpace(id)]
	if managed == nil || managed.status.State == SideSessionStateStopping {
		return SideSessionStatus{}, ErrSideSessionNotActive
	}
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
	managed, err := m.remove(id)
	if err != nil {
		return err
	}
	return stopManagedSideSessionContext(ctx, managed)
}

func (m *sideSessionManager) StopAll() error {
	if m == nil {
		return nil
	}
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	items, err := m.beginTransition(false)
	if err != nil {
		return err
	}
	defer m.finishTransition()
	return m.drainTransition(items)
}

func (m *sideSessionManager) replacePrimary(replace func() error) error {
	if m == nil {
		return replace()
	}
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	items, err := m.beginTransition(false)
	if err != nil {
		return err
	}
	defer m.finishTransition()
	if err := m.drainTransition(items); err != nil {
		return fmt.Errorf("close managed side sessions: %w", err)
	}
	if err := m.parent.ctx.Err(); err != nil {
		return ErrSideSessionManagerClosed
	}
	return replace()
}

func (m *sideSessionManager) beginTransition(closing bool) ([]*managedSideSession, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	if m.closed && !closing {
		m.mu.Unlock()
		return nil, ErrSideSessionManagerClosed
	}
	if closing {
		m.closed = true
	}
	m.transitioning = true
	if m.deliveryCancel != nil {
		m.deliveryCancel()
	}
	items := make([]*managedSideSession, 0, len(m.sessions))
	for id, managed := range m.sessions {
		managed.status.State = SideSessionStateStopping
		managed.status.Subscribed = false
		items = append(items, managed)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	return items, nil
}

func (m *sideSessionManager) drainTransition(items []*managedSideSession) error {
	var result error
	for _, managed := range items {
		result = errors.Join(result, stopManagedSideSession(managed))
	}
	m.deliveries.Wait()
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
	}
	m.transitioning = false
	m.mu.Unlock()
}

func (m *sideSessionManager) Close() error {
	if m == nil {
		return nil
	}
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	items, err := m.beginTransition(true)
	if err != nil {
		return err
	}
	defer m.finishTransition()
	return m.drainTransition(items)
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
		if subscribed {
			m.deliveries.Add(1)
		}
		m.mu.Unlock()
		if subscribed {
			go func() {
				defer m.deliveries.Done()
				m.deliverResult(managed.deliveryCtx, managed, status)
			}()
		}
	}()
}

func (m *sideSessionManager) deliverResult(ctx context.Context, managed *managedSideSession, status SideSessionStatus) {
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
	pendingID := "side-session-result:" + status.SessionID + ":" + status.LastTurnID
	var record runtime.PendingInputRecord
	delay := 50 * time.Millisecond
	var persistErr error
	for attempt := 0; attempt < sideSessionPersistAttempts; attempt++ {
		var err error
		record, err = m.parent.Engine.PersistPendingMessageWithOptions(ctx, msg, runtime.PendingInputOptions{
			ID:  pendingID,
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

	for {
		result := m.parent.admitPersistedUserTurn(ctx, record, TurnIDFunc(func(string) string { return m.nextTurnID() }))
		switch result.Kind {
		case TurnAdmissionQueued:
			return
		case TurnAdmissionStarted:
			_, _ = m.parent.RunAdmittedTurn(ctx, result.Start.TurnID, result.Start.Message)
			m.parent.CompleteAdmittedTurn(result.Start.TurnID)
			return
		case TurnAdmissionRejected:
			if !errors.Is(result.Err, runtime.ErrPendingInputQueueFull) {
				return
			}
		case TurnAdmissionConflict:
			// A turn can close between admission snapshots. Retry while the
			// subscription and owning App are still active.
		case TurnAdmissionError:
			if errors.Is(result.Err, runtime.ErrPendingInputExpired) || errors.Is(result.Err, runtime.ErrPendingInputHandled) {
				return
			}
		default:
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (m *sideSessionManager) recordNotificationFailure(managed *managedSideSession, status SideSessionStatus, err error) {
	m.mu.Lock()
	if current := m.sessions[status.SessionID]; current == managed && managed.status.LastTurnID == status.LastTurnID {
		managed.status.NotificationError = err.Error()
		managed.status.UpdatedAt = time.Now().UTC()
	}
	m.mu.Unlock()
	m.parent.Bus.Emit(events.Event{Type: "side_session.notification_failed", TurnID: status.LastTurnID, Payload: map[string]any{
		"session_id": status.SessionID,
		"error":      err.Error(),
	}})
}

func (m *sideSessionManager) active(id string) (*managedSideSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	managed := m.sessions[strings.TrimSpace(id)]
	if managed == nil || managed.status.State == SideSessionStateStopping {
		return nil, ErrSideSessionNotActive
	}
	return managed, nil
}

func (m *sideSessionManager) remove(id string) (*managedSideSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id = strings.TrimSpace(id)
	managed := m.sessions[id]
	if managed == nil {
		return nil, ErrSideSessionNotActive
	}
	managed.status.State = SideSessionStateStopping
	managed.status.Subscribed = false
	managed.status.UpdatedAt = time.Now().UTC()
	delete(m.sessions, id)
	return managed, nil
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
	return stopManagedSideSessionContext(context.Background(), managed)
}

func stopManagedSideSessionContext(ctx context.Context, managed *managedSideSession) error {
	if managed == nil || managed.app == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	managed.cancel(ErrSideSessionStopped)
	if managed.unsubscribeState != nil {
		managed.unsubscribeState()
		managed.unsubscribeState = nil
	}
	managed.app.CancelActiveTurn(ErrSideSessionStopped)
	done := make(chan struct{})
	go func() {
		managed.done.Wait()
		close(done)
	}()
	select {
	case <-done:
		return managed.app.CloseAndWait()
	case <-ctx.Done():
		go func() {
			<-done
			_ = managed.app.CloseAndWait()
		}()
		return ctx.Err()
	}
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
