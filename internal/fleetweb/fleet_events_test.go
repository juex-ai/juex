package fleetweb

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/fleet"
)

func TestFleetEventsPushesAgentStatusWithoutRosterPoll(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status/events" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`id: cursor-2
data: {"type":"agent.status","activity":{"state":"working","session_id":"session-1","pending_count":0,"status":{"cursor":"cursor-2","session":{"id":"session-1","state":"turn_active","pending_count":0,"max_pending_inputs":16,"can_accept_input":true},"turn":{"id":"turn-1","state":"active","phase":"provider_iteration","streaming":true,"started_at":"2026-07-19T00:00:00Z","updated_at":"2026-07-19T00:00:01Z"},"tools":[],"token_usage":{"input_tokens":0,"output_tokens":0}}}}

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
	var event fleetAgentStatusEvent
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			t.Fatal(err)
		}
		break
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if event.AgentID != "agent-1" || event.Activity == nil || event.Activity.State != "working" {
		t.Fatalf("event = %+v", event)
	}
	if event.Activity.Status == nil || event.Activity.Status.Cursor != "cursor-2" ||
		event.Activity.Status.Turn == nil || !event.Activity.Status.Turn.Streaming {
		t.Fatalf("status = %+v", event.Activity.Status)
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
data: {"type":"agent.status","activity":{"state":"idle","session_id":"session-1","pending_count":0,"status":{"cursor":"cursor-1","session":{"id":"session-1","state":"idle","pending_count":0,"max_pending_inputs":16,"can_accept_input":true},"tools":[],"token_usage":{"input_tokens":0,"output_tokens":0}}}}

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
	firstEvent := readFleetStatusEvent(t, first)

	second, err := http.Get(server.URL + "/api/fleet/events")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	secondEvent := readFleetStatusEvent(t, second)

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

	hub.publish(generation, fleetAgentStatusEvent{Type: "agent.status", AgentID: "agent-1"})
	<-first.updates
	firstBatch := first.take()
	if len(firstBatch) != 1 || firstBatch[0].Cursor == "" {
		t.Fatalf("first batch = %+v", firstBatch)
	}
	hub.publish(generation, fleetAgentStatusEvent{Type: "agent.status", AgentID: "agent-2"})

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
	if len(fallback.initial) != 2 {
		t.Fatalf("unknown cursor fallback = %+v, want current snapshot for both agents", fallback.initial)
	}
}

func TestFleetStatusSubscriberCoalescesPerAgent(t *testing.T) {
	subscriber := newFleetStatusSubscriber()
	subscriber.publish(fleetAgentStatusEvent{AgentID: "hot", Cursor: "1", Sequence: 1})
	subscriber.publish(fleetAgentStatusEvent{AgentID: "quiet", Cursor: "2", Sequence: 2})
	subscriber.publish(fleetAgentStatusEvent{AgentID: "hot", Cursor: "3", Sequence: 3})

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
		hub.followAgentStatus(ctx, status, make(chan fleetAgentStatusEvent, 1))
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
	hub.publish(generation, fleetAgentStatusEvent{
		Type: "agent.status", AgentID: "agent-1",
		Activity: &agentActivity{State: "idle"},
	})
	<-firstSubscription.updates
	first := firstSubscription.take()[0]
	hub.publish(generation, fleetAgentStatusEvent{
		Type: "agent.status", AgentID: "agent-1",
		Activity: &agentActivity{State: "working"},
	})
	firstSubscription.cancel()
	hub.mu.Lock()
	if hub.running || len(hub.history) != 2 {
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
	hub.mu.Lock()
	latestCursor := hub.history[len(hub.history)-1].Cursor
	hub.mu.Unlock()
	if !strings.Contains(body, "id: "+latestCursor) || !strings.Contains(body, `"state":"working"`) {
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

func readFleetStatusEvent(t *testing.T, response *http.Response) fleetAgentStatusEvent {
	t.Helper()
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event fleetAgentStatusEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			t.Fatal(err)
		}
		return event
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("fleet status stream ended before an event")
	return fleetAgentStatusEvent{}
}
