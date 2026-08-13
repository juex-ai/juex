package fleetweb

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/processmetrics"
)

const (
	fleetReconcileInterval  = 3 * time.Second
	fleetStatusHistoryLimit = 512
)

type upstreamAgentStatusEvent struct {
	Type     string         `json:"type"`
	Activity *agentActivity `json:"activity"`
}

type fleetStatusEvent struct {
	Type     string                 `json:"type"`
	AgentID  string                 `json:"agent_id,omitempty"`
	Activity *agentActivity         `json:"activity,omitempty"`
	Agents   *[]fleet.AgentStatus   `json:"agents,omitempty"`
	Process  **processmetrics.Usage `json:"process,omitempty"`
	Cursor   string                 `json:"-"`
	Sequence uint64                 `json:"-"`
}

type fleetStatusSubscription struct {
	endpoint string
	cancel   context.CancelFunc
}

type fleetStatusHub struct {
	manager        backend
	clients        *activityClientPool
	processMetrics processmetrics.Provider
	processPID     int

	mu          sync.Mutex
	current     map[string]fleetStatusEvent
	history     []fleetStatusEvent
	subscribers map[uint64]*fleetStatusSubscriber
	nextID      uint64
	streamID    string
	generation  uint64
	sequence    uint64
	running     bool
	runCancel   context.CancelFunc
	runReady    chan struct{}
	reconcile   chan struct{}
	roster      []fleet.AgentStatus
	rosterReady bool
}

type fleetStatusHubSubscription struct {
	initial []fleetStatusEvent
	updates <-chan struct{}
	take    func() []fleetStatusEvent
	cancel  func()
}

type fleetStatusSubscriber struct {
	mu      sync.Mutex
	pending map[string]fleetStatusEvent
	notify  chan struct{}
}

func newFleetStatusSubscriber() *fleetStatusSubscriber {
	return &fleetStatusSubscriber{
		pending: map[string]fleetStatusEvent{},
		notify:  make(chan struct{}, 1),
	}
}

func (s *fleetStatusSubscriber) publish(event fleetStatusEvent) {
	s.mu.Lock()
	s.pending[fleetStatusEventKey(event)] = event
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *fleetStatusSubscriber) take() []fleetStatusEvent {
	s.mu.Lock()
	events := make([]fleetStatusEvent, 0, len(s.pending))
	for _, event := range s.pending {
		events = append(events, event)
	}
	s.pending = map[string]fleetStatusEvent{}
	s.mu.Unlock()
	sortFleetStatusEvents(events)
	return events
}

func newFleetStatusHub(
	manager backend,
	clients *activityClientPool,
	processMetrics ...processmetrics.Provider,
) *fleetStatusHub {
	var metrics processmetrics.Provider
	if len(processMetrics) > 0 {
		metrics = processMetrics[0]
	}
	return &fleetStatusHub{
		manager:        manager,
		clients:        clients,
		processMetrics: metrics,
		processPID:     os.Getpid(),
		current:        map[string]fleetStatusEvent{},
		subscribers:    map[uint64]*fleetStatusSubscriber{},
		streamID:       newFleetStatusStreamID(),
		reconcile:      make(chan struct{}, 1),
	}
}

func (h *fleetStatusHub) subscribe(since string) fleetStatusHubSubscription {
	for {
		h.mu.Lock()
		if !h.running {
			ctx, cancel := context.WithCancel(context.Background())
			h.generation++
			generation := h.generation
			h.sequence = 0
			h.current = map[string]fleetStatusEvent{}
			h.roster = nil
			h.rosterReady = false
			h.running = true
			h.runCancel = cancel
			h.runReady = make(chan struct{})
			go h.run(ctx, generation, h.runReady)
		}
		ready := h.runReady
		h.mu.Unlock()
		<-ready

		h.mu.Lock()
		if !h.running || h.runReady != ready {
			h.mu.Unlock()
			continue
		}
		h.nextID++
		id := h.nextID
		subscriber := newFleetStatusSubscriber()
		h.subscribers[id] = subscriber
		initial, found := eventsAfterFleetCursor(h.history, since)
		if since == "" || !found {
			initial = make([]fleetStatusEvent, 0, len(h.current))
			for _, event := range h.current {
				initial = append(initial, event)
			}
			sortFleetStatusEvents(initial)
		}
		h.mu.Unlock()

		return fleetStatusHubSubscription{
			initial: initial,
			updates: subscriber.notify,
			take:    subscriber.take,
			cancel: func() {
				var cancel context.CancelFunc
				h.mu.Lock()
				delete(h.subscribers, id)
				if len(h.subscribers) == 0 && h.running {
					cancel = h.runCancel
					h.runCancel = nil
					h.runReady = nil
					h.running = false
					h.current = map[string]fleetStatusEvent{}
					h.roster = nil
					h.rosterReady = false
				}
				h.mu.Unlock()
				if cancel != nil {
					cancel()
				}
			},
		}
	}
}

func (h *fleetStatusHub) publish(generation uint64, event fleetStatusEvent) {
	h.mu.Lock()
	if !h.running || h.generation != generation {
		h.mu.Unlock()
		return
	}
	h.sequence++
	event.Cursor = fmt.Sprintf("%s:%d:%d", h.streamID, generation, h.sequence)
	event.Sequence = h.sequence
	h.current[fleetStatusEventKey(event)] = event
	h.history = append(h.history, event)
	if len(h.history) > fleetStatusHistoryLimit {
		h.history = append([]fleetStatusEvent(nil), h.history[len(h.history)-fleetStatusHistoryLimit:]...)
	}
	subscribers := make([]*fleetStatusSubscriber, 0, len(h.subscribers))
	for _, subscriber := range h.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	h.mu.Unlock()

	for _, subscriber := range subscribers {
		subscriber.publish(event)
	}
}

func sortFleetStatusEvents(events []fleetStatusEvent) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].Sequence != events[j].Sequence {
			return events[i].Sequence < events[j].Sequence
		}
		return events[i].AgentID < events[j].AgentID
	})
}

func newFleetStatusStreamID() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func eventsAfterFleetCursor(history []fleetStatusEvent, since string) ([]fleetStatusEvent, bool) {
	if since == "" {
		return nil, false
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Cursor == since {
			return append([]fleetStatusEvent(nil), history[i+1:]...), true
		}
	}
	return nil, false
}

func (h *fleetStatusHub) removeCurrent(generation uint64, agentID string) {
	h.mu.Lock()
	if !h.running || h.generation != generation {
		h.mu.Unlock()
		return
	}
	delete(h.current, "agent.status:"+agentID)
	delete(h.current, "agent.process:"+agentID)
	h.mu.Unlock()
}

func fleetStatusEventKey(event fleetStatusEvent) string {
	if event.Type == "fleet.roster" || event.Type == "fleet.status" {
		return event.Type
	}
	return event.Type + ":" + event.AgentID
}

func (h *fleetStatusHub) publishProcess(generation uint64, ctx context.Context) {
	if h.processMetrics == nil {
		return
	}
	var process *processmetrics.Usage
	usage, err := h.processMetrics.Sample(ctx, "fleet", h.processPID)
	if err == nil {
		process = &usage
	}
	h.publish(generation, fleetStatusEvent{Type: "fleet.status", Process: &process})
}

func (h *fleetStatusHub) publishAgentProcesses(generation uint64, statuses []fleet.AgentStatus) {
	present := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		present[status.ID] = struct{}{}
	}
	h.mu.Lock()
	if h.running && h.generation == generation {
		for key := range h.current {
			if !strings.HasPrefix(key, "agent.process:") {
				continue
			}
			if _, exists := present[strings.TrimPrefix(key, "agent.process:")]; !exists {
				delete(h.current, key)
			}
		}
	}
	h.mu.Unlock()
	for _, status := range statuses {
		var process *processmetrics.Usage
		if status.Process != nil {
			usage := *status.Process
			process = &usage
		}
		h.publish(generation, fleetStatusEvent{
			Type:    "agent.process",
			AgentID: status.ID,
			Process: &process,
		})
	}
}

func (h *fleetStatusHub) requestReconcile() {
	if h == nil {
		return
	}
	select {
	case h.reconcile <- struct{}{}:
	default:
	}
}

func (h *fleetStatusHub) publishRoster(generation uint64, statuses []fleet.AgentStatus) {
	next := cloneFleetStatuses(statuses)
	sort.Slice(next, func(i, j int) bool { return next[i].ID < next[j].ID })
	h.mu.Lock()
	if !h.running || h.generation != generation {
		h.mu.Unlock()
		return
	}
	if h.rosterReady && equalFleetRoster(h.roster, next) {
		h.mu.Unlock()
		return
	}
	h.roster = cloneFleetStatuses(next)
	h.rosterReady = true
	h.mu.Unlock()
	h.publish(generation, fleetStatusEvent{Type: "fleet.roster", Agents: &next})
}

func cloneFleetStatuses(statuses []fleet.AgentStatus) []fleet.AgentStatus {
	cloned := make([]fleet.AgentStatus, len(statuses))
	copy(cloned, statuses)
	for index := range cloned {
		if cloned[index].Process != nil {
			process := *cloned[index].Process
			cloned[index].Process = &process
		}
	}
	return cloned
}

func equalFleetRoster(left, right []fleet.AgentStatus) bool {
	left = cloneFleetStatuses(left)
	right = cloneFleetStatuses(right)
	for index := range left {
		left[index].Process = nil
	}
	for index := range right {
		right[index].Process = nil
	}
	return reflect.DeepEqual(left, right)
}

func (h *fleetStatusHub) run(ctx context.Context, generation uint64, ready chan<- struct{}) {
	updates := make(chan fleetStatusEvent, 32)
	subscriptions := map[string]fleetStatusSubscription{}
	defer func() {
		for _, subscription := range subscriptions {
			subscription.cancel()
		}
	}()

	reconcile := func() {
		h.publishProcess(generation, ctx)
		statuses, err := h.manager.Status(ctx)
		if err != nil {
			return
		}
		h.publishRoster(generation, statuses)
		h.publishAgentProcesses(generation, statuses)
		active := make(map[string]fleet.AgentStatus)
		for _, status := range statuses {
			if status.RuntimeHealth == fleet.RuntimeHealthy && status.Endpoint != "" {
				active[status.ID] = status
			}
		}
		for id, subscription := range subscriptions {
			status, exists := active[id]
			if exists && status.Endpoint == subscription.endpoint {
				continue
			}
			subscription.cancel()
			delete(subscriptions, id)
			h.removeCurrent(generation, id)
		}
		for id, status := range active {
			if _, exists := subscriptions[id]; exists {
				continue
			}
			streamCtx, cancel := context.WithCancel(ctx)
			subscriptions[id] = fleetStatusSubscription{endpoint: status.Endpoint, cancel: cancel}
			go h.followAgentStatus(streamCtx, status, updates)
		}
	}

	reconcile()
	close(ready)
	ticker := time.NewTicker(fleetReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case event := <-updates:
			h.publish(generation, event)
		case <-h.reconcile:
			reconcile()
		case <-ticker.C:
			reconcile()
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) handleFleetEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "general_error", "streaming not supported")
		return
	}
	subscription := s.fleetStatus.subscribe(fleetResumeCursor(r))
	defer subscription.cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	for _, event := range subscription.initial {
		if err := writeFleetStatusSSE(w, event); err != nil {
			return
		}
	}
	flusher.Flush()

	for {
		select {
		case <-subscription.updates:
			events := subscription.take()
			if len(events) == 0 {
				continue
			}
			for _, event := range events {
				if err := writeFleetStatusSSE(w, event); err != nil {
					return
				}
			}
		case <-r.Context().Done():
			return
		}
	}
}

func fleetResumeCursor(r *http.Request) string {
	if r == nil {
		return ""
	}
	if cursor := strings.TrimSpace(r.Header.Get("Last-Event-ID")); cursor != "" {
		return cursor
	}
	return strings.TrimSpace(r.URL.Query().Get("since"))
}

func (h *fleetStatusHub) followAgentStatus(
	ctx context.Context,
	status fleet.AgentStatus,
	updates chan<- fleetStatusEvent,
) {
	for ctx.Err() == nil {
		_ = h.clients.streamStatus(ctx, status, func(activity *agentActivity) {
			select {
			case updates <- fleetStatusEvent{
				Type:     "agent.status",
				AgentID:  status.ID,
				Activity: activity,
			}:
			case <-ctx.Done():
			}
		})
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func scanAgentStatusSSE(r io.Reader, onActivity func(*agentActivity)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxAgentActivityBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event upstreamAgentStatusEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			return fmt.Errorf("decode agent status event: %w", err)
		}
		if event.Type != "agent.status" || event.Activity == nil {
			continue
		}
		if event.Activity.State != "idle" && event.Activity.State != "working" {
			return fmt.Errorf("decode agent status event: unsupported state %q", event.Activity.State)
		}
		onActivity(event.Activity)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read agent status stream: %w", err)
	}
	return nil
}

func writeFleetStatusSSE(w http.ResponseWriter, event fleetStatusEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.Cursor != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", event.Cursor); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
