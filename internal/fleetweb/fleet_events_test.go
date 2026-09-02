package fleetweb

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/processmetrics"
)

func TestFleetEventsPushesAgentStatusWithoutRosterPoll(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status/events" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`id: cursor-2
data: {"type":"agent.status","activity":{"state":"working","pending_input_count":0,"selected_status":{"cursor":"cursor-2","thread":{"id":"234567","state":"turn_active","working":true,"pending_count":0,"max_pending_inputs":16,"can_accept_input":true},"turn":{"id":"turn-1","state":"active","phase":"provider_iteration","streaming":true,"started_at":"2026-07-19T00:00:00Z","updated_at":"2026-07-19T00:00:01Z"},"tools":[],"token_usage":{"input_tokens":0,"output_tokens":0}}}}

`))
	}))
	defer upstream.Close()

	backend := &fakeBackend{statuses: []fleet.AgentStatus{{
		ID:            "agent-1",
		RuntimeHealth: fleet.RuntimeHealthy,
		Endpoint:      strings.Replace(upstream.URL, "http://", "tcp://", 1),
	}}}
	server := httptest.NewServer(newServer(backend, Options{Addr: "127.0.0.1:0"}).Handler())
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(server.URL + "/api/fleet/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	scanner := bufio.NewScanner(response.Body)
	var event fleetStatusEvent
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "agent.status" {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if event.AgentID != "agent-1" || event.Activity == nil || event.Activity.State != "working" {
		t.Fatalf("event = %+v", event)
	}
	if event.Activity.SelectedStatus == nil || event.Activity.SelectedStatus.Cursor != "cursor-2" ||
		event.Activity.SelectedStatus.Thread.ID != "234567" ||
		event.Activity.SelectedStatus.Turn == nil || !event.Activity.SelectedStatus.Turn.Streaming {
		t.Fatalf("status = %+v", event.Activity.SelectedStatus)
	}
}

func TestFleetEventsSharesOneUpstreamStreamAcrossBrowserClients(t *testing.T) {
	var upstreamConnections atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status/events" {
			http.NotFound(w, r)
			return
		}
		upstreamConnections.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`id: cursor-1
data: {"type":"agent.status","activity":{"state":"idle","pending_input_count":0,"selected_status":{"cursor":"cursor-1","thread":{"id":"234567","state":"idle","working":false,"pending_count":0,"max_pending_inputs":16,"can_accept_input":true},"tools":[],"token_usage":{"input_tokens":0,"output_tokens":0}}}}

`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()

	backend := &fakeBackend{statuses: []fleet.AgentStatus{{
		ID:            "agent-1",
		RuntimeHealth: fleet.RuntimeHealthy,
		Endpoint:      strings.Replace(upstream.URL, "http://", "tcp://", 1),
	}}}
	server := httptest.NewServer(newServer(backend, Options{Addr: "127.0.0.1:0"}).Handler())
	defer server.Close()

	first, err := http.Get(server.URL + "/api/fleet/events")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	firstEvent := readFleetStatusEventType(t, first, "agent.status")

	second, err := http.Get(server.URL + "/api/fleet/events")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	secondEvent := readFleetStatusEventType(t, second, "agent.status")

	if firstEvent.AgentID != "agent-1" || secondEvent.AgentID != "agent-1" {
		t.Fatalf("events = %+v / %+v", firstEvent, secondEvent)
	}
	if got := upstreamConnections.Load(); got != 1 {
		t.Fatalf("upstream status streams = %d, want 1", got)
	}
}

func TestFleetStatusHubResumesAfterAggregateCursor(t *testing.T) {
	hub := newFleetStatusHub(&fakeBackend{}, newActivityClientPool())
	first := hub.subscribe("")
	defer first.cancel()
	hub.mu.Lock()
	generation := hub.generation
	hub.mu.Unlock()

	hub.publish(generation, fleetStatusEvent{Type: "agent.status", AgentID: "agent-1"})
	<-first.updates
	firstBatch := first.take()
	if len(firstBatch) != 1 || firstBatch[0].Cursor == "" {
		t.Fatalf("first batch = %+v", firstBatch)
	}
	hub.publish(generation, fleetStatusEvent{Type: "agent.status", AgentID: "agent-2"})

	resumed := hub.subscribe(firstBatch[0].Cursor)
	defer resumed.cancel()
	if len(resumed.initial) != 1 || resumed.initial[0].AgentID != "agent-2" {
		t.Fatalf("resumed initial = %+v", resumed.initial)
	}
	if resumed.initial[0].Cursor == firstBatch[0].Cursor {
		t.Fatalf("resume cursor did not advance: %+v", resumed.initial)
	}

	fallback := hub.subscribe("cursor-from-another-process")
	defer fallback.cancel()
	if len(fallback.initial) != 3 {
		t.Fatalf("unknown cursor fallback = %+v, want roster and both agent snapshots", fallback.initial)
	}
}

func TestFleetStatusSubscriberCoalescesPerAgent(t *testing.T) {
	subscriber := newFleetStatusSubscriber()
	subscriber.publish(fleetStatusEvent{Type: "agent.status", AgentID: "hot", Cursor: "1", Sequence: 1})
	subscriber.publish(fleetStatusEvent{Type: "agent.status", AgentID: "quiet", Cursor: "2", Sequence: 2})
	subscriber.publish(fleetStatusEvent{Type: "agent.status", AgentID: "hot", Cursor: "3", Sequence: 3})

	select {
	case <-subscriber.notify:
	case <-time.After(time.Second):
		t.Fatal("subscriber was not notified")
	}
	events := subscriber.take()
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].AgentID != "quiet" || events[0].Cursor != "2" ||
		events[1].AgentID != "hot" || events[1].Cursor != "3" {
		t.Fatalf("coalesced events = %+v", events)
	}
}

func TestFleetStatusHubPublishesRosterOnlyWhenSnapshotChanges(t *testing.T) {
	first := []fleet.AgentStatus{{ID: "agent-1", RuntimeHealth: fleet.RuntimeStopped}}
	backend := &fakeBackend{statuses: first}
	hub := newFleetStatusHub(backend, newActivityClientPool())
	subscription := hub.subscribe("")
	defer subscription.cancel()
	events := subscription.initial
	if len(events) != 2 || events[0].Type != "fleet.roster" || events[0].Agents == nil || len(*events[0].Agents) != 1 ||
		events[1].Type != "agent.process" || events[1].Process == nil || *events[1].Process != nil {
		t.Fatalf("first roster events = %+v", events)
	}

	hub.mu.Lock()
	generation := hub.generation
	hub.mu.Unlock()
	hub.publishRoster(generation, first)
	select {
	case <-subscription.updates:
		t.Fatalf("unchanged roster events = %+v", subscription.take())
	case <-time.After(30 * time.Millisecond):
	}

	metricsOnly := cloneFleetStatuses(first)
	metricsOnly[0].Process = &processmetrics.Usage{RSSBytes: 1024}
	hub.publishRoster(generation, metricsOnly)
	select {
	case <-subscription.updates:
		t.Fatalf("metrics-only roster events = %+v", subscription.take())
	case <-time.After(30 * time.Millisecond):
	}

	changed := []fleet.AgentStatus{{ID: "agent-1", RuntimeHealth: fleet.RuntimeHealthy, PID: 42}}
	hub.publishRoster(generation, changed)
	select {
	case <-subscription.updates:
		events = subscription.take()
	case <-time.After(time.Second):
		t.Fatal("changed roster was not published")
	}
	if len(events) != 1 || events[0].Agents == nil || (*events[0].Agents)[0].PID != 42 {
		t.Fatalf("changed roster events = %+v", events)
	}
}

func TestFleetStatusHubPublishesRosterFailureAndRecovery(t *testing.T) {
	var unavailable atomic.Bool
	statuses := []fleet.AgentStatus{{ID: "agent-1", RuntimeHealth: fleet.RuntimeStopped}}
	backend := &fakeBackend{statusFn: func(context.Context) ([]fleet.AgentStatus, error) {
		if unavailable.Load() {
			return nil, errors.New("registry unavailable")
		}
		return statuses, nil
	}}
	hub := newFleetStatusHub(backend, newActivityClientPool())
	subscription := hub.subscribe("")
	defer subscription.cancel()

	unavailable.Store(true)
	hub.requestReconcile()
	select {
	case <-subscription.updates:
	case <-time.After(time.Second):
		t.Fatal("roster failure was not published")
	}
	events := subscription.take()
	if len(events) != 1 || events[0].Type != "fleet.roster.unavailable" ||
		events[0].Error != "registry unavailable" {
		t.Fatalf("roster failure events = %+v", events)
	}
	joined := hub.subscribe("")
	if len(joined.initial) != 3 || joined.initial[0].Type != "fleet.roster" ||
		joined.initial[1].Type != "agent.process" ||
		joined.initial[2].Type != "fleet.roster.unavailable" {
		t.Fatalf("unavailable current snapshot = %+v", joined.initial)
	}
	joined.cancel()

	hub.requestReconcile()
	select {
	case <-subscription.updates:
		t.Fatalf("unchanged roster failure events = %+v", subscription.take())
	case <-time.After(30 * time.Millisecond):
	}

	unavailable.Store(false)
	hub.requestReconcile()
	select {
	case <-subscription.updates:
	case <-time.After(time.Second):
		t.Fatal("roster recovery was not published")
	}
	events = subscription.take()
	if len(events) == 0 || events[0].Type != "fleet.roster" || events[0].Agents == nil ||
		len(*events[0].Agents) != 1 {
		t.Fatalf("roster recovery events = %+v", events)
	}
	hub.mu.Lock()
	_, stillUnavailable := hub.current["fleet.roster.unavailable"]
	hub.mu.Unlock()
	if stillUnavailable {
		t.Fatal("recovered current snapshot retained roster failure")
	}
}

func TestFleetStatusHubPublishesAgentProcessSeparatelyFromRoster(t *testing.T) {
	hub := newFleetStatusHub(&fakeBackend{}, newActivityClientPool())
	subscription := hub.subscribe("")
	defer subscription.cancel()
	hub.mu.Lock()
	generation := hub.generation
	hub.mu.Unlock()

	hub.publishAgentProcesses(generation, []fleet.AgentStatus{{
		ID:      "agent-1",
		Process: &processmetrics.Usage{RSSBytes: 2048},
	}})
	select {
	case <-subscription.updates:
	case <-time.After(time.Second):
		t.Fatal("agent process was not published")
	}
	events := subscription.take()
	if len(events) != 1 || events[0].Type != "agent.process" ||
		events[0].AgentID != "agent-1" || events[0].Process == nil ||
		*events[0].Process == nil || (**events[0].Process).RSSBytes != 2048 {
		t.Fatalf("agent process events = %+v", events)
	}

	hub.publishAgentProcesses(generation, []fleet.AgentStatus{{ID: "agent-1"}})
	select {
	case <-subscription.updates:
	case <-time.After(time.Second):
		t.Fatal("unavailable agent process was not published")
	}
	events = subscription.take()
	if len(events) != 1 || events[0].Process == nil || *events[0].Process != nil {
		t.Fatalf("unavailable agent process events = %+v", events)
	}

	hub.publishAgentProcesses(generation, nil)
	hub.mu.Lock()
	_, retained := hub.current["agent.process:agent-1"]
	hub.mu.Unlock()
	if retained {
		t.Fatal("removed Agent process remained in the current snapshot")
	}
}

func TestFleetRosterEventEncodesEmptySnapshotAsArray(t *testing.T) {
	agents := []fleet.AgentStatus{}
	body, err := json.Marshal(fleetStatusEvent{Type: "fleet.roster", Agents: &agents})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != `{"type":"fleet.roster","agents":[]}` {
		t.Fatalf("event JSON = %s", got)
	}
}

func TestFleetRosterUnavailableEventEncodesError(t *testing.T) {
	body, err := json.Marshal(fleetStatusEvent{
		Type:  "fleet.roster.unavailable",
		Error: "registry unavailable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != `{"type":"fleet.roster.unavailable","error":"registry unavailable"}` {
		t.Fatalf("event JSON = %s", got)
	}
}

func TestFleetProcessEventEncodesUnavailableSampleAsNull(t *testing.T) {
	var process *processmetrics.Usage
	body, err := json.Marshal(fleetStatusEvent{Type: "agent.process", AgentID: "one", Process: &process})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != `{"type":"agent.process","agent_id":"one","process":null}` {
		t.Fatalf("event JSON = %s", got)
	}
}

func TestFollowAgentStatusBacksOffAfterNormalEOF(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := newFleetStatusHub(&fakeBackend{}, newActivityClientPool())
	status := fleet.AgentStatus{
		ID:            "agent-1",
		RuntimeHealth: fleet.RuntimeHealthy,
		Endpoint:      strings.Replace(upstream.URL, "http://", "tcp://", 1),
	}
	done := make(chan struct{})
	go func() {
		hub.followAgentStatus(ctx, status, make(chan fleetStatusEvent, 1))
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if requests.Load() != 1 {
		t.Fatalf("initial requests = %d, want 1", requests.Load())
	}
	time.Sleep(150 * time.Millisecond)
	if requests.Load() != 1 {
		t.Fatalf("requests during backoff = %d, want 1", requests.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("followAgentStatus did not stop")
	}
}

func TestFleetEventsUsesLastEventIDForResume(t *testing.T) {
	hub := newFleetStatusHub(&fakeBackend{}, newActivityClientPool())
	firstSubscription := hub.subscribe("")
	hub.mu.Lock()
	generation := hub.generation
	hub.mu.Unlock()
	hub.publish(generation, fleetStatusEvent{
		Type: "agent.status", AgentID: "agent-1",
		Activity: &agentActivity{State: "idle"},
	})
	<-firstSubscription.updates
	first := firstSubscription.take()[0]
	hub.publish(generation, fleetStatusEvent{
		Type: "agent.status", AgentID: "agent-1",
		Activity: &agentActivity{State: "working"},
	})
	firstSubscription.cancel()
	hub.mu.Lock()
	if hub.running || len(hub.history) != 3 {
		t.Fatalf("hub after last disconnect: running=%v history=%d", hub.running, len(hub.history))
	}
	hub.mu.Unlock()

	server := &Server{fleetStatus: hub}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handleFleetEvents))
	defer httpServer.Close()
	request, err := http.NewRequest(http.MethodGet, httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", first.Cursor)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(ctx)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		if strings.HasPrefix(line, "data:") {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n")
	if !strings.Contains(body, `"state":"working"`) {
		t.Fatalf("resumed SSE = %q", body)
	}
}

func TestFleetResumeCursorPrefersLastEventIDOnReconnect(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/fleet/events?since=initial-cursor", nil)
	request.Header.Set("Last-Event-ID", "latest-cursor")
	if got := fleetResumeCursor(request); got != "latest-cursor" {
		t.Fatalf("resume cursor = %q, want latest-cursor", got)
	}
}

func readFleetStatusEventType(t *testing.T, response *http.Response, eventType string) fleetStatusEvent {
	t.Helper()
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event fleetStatusEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			t.Fatal(err)
		}
		if eventType != "" && event.Type != eventType {
			continue
		}
		return event
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("fleet status stream ended before an event")
	return fleetStatusEvent{}
}
