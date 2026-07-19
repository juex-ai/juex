package fleetweb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/runtime"
)

const (
	agentActivityTimeout  = time.Second
	maxAgentActivityBytes = 64 << 10
)

type agentActivity struct {
	State        string                  `json:"state"`
	SessionID    string                  `json:"session_id,omitempty"`
	SessionAlias string                  `json:"session_alias,omitempty"`
	PendingCount int                     `json:"pending_count"`
	Status       *runtime.StatusSnapshot `json:"status,omitempty"`
}

type agentRosterItem struct {
	fleet.AgentStatus
	Activity *agentActivity `json:"activity,omitempty"`
}

type activityClientPool struct {
	mu         sync.Mutex
	clients    map[string]*http.Client
	transports map[string]*http.Transport
}

func newActivityClientPool() *activityClientPool {
	return &activityClientPool{
		clients:    make(map[string]*http.Client),
		transports: make(map[string]*http.Transport),
	}
}

func (p *activityClientPool) client(rawEndpoint string) (*http.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if client := p.clients[rawEndpoint]; client != nil {
		return client, nil
	}
	target, err := endpoint.Parse(rawEndpoint)
	if err != nil {
		return nil, fmt.Errorf("parse agent activity endpoint: %w", err)
	}
	transport := target.NewTransport()
	client := &http.Client{Transport: transport}
	p.clients[rawEndpoint] = client
	p.transports[rawEndpoint] = transport
	return client, nil
}

func (p *activityClientPool) retain(activeEndpoints map[string]struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for rawEndpoint, transport := range p.transports {
		if _, active := activeEndpoints[rawEndpoint]; active {
			continue
		}
		transport.CloseIdleConnections()
		delete(p.transports, rawEndpoint)
		delete(p.clients, rawEndpoint)
	}
}

func (p *activityClientPool) close() {
	p.retain(map[string]struct{}{})
}

func (s *Server) roster(ctx context.Context, statuses []fleet.AgentStatus) []agentRosterItem {
	items := make([]agentRosterItem, len(statuses))
	activeEndpoints := make(map[string]struct{})
	for _, status := range statuses {
		if status.RuntimeHealth == fleet.RuntimeHealthy && status.Endpoint != "" {
			activeEndpoints[status.Endpoint] = struct{}{}
		}
	}
	if s.activityClients != nil {
		s.activityClients.retain(activeEndpoints)
	}

	var wait sync.WaitGroup
	for index, status := range statuses {
		items[index].AgentStatus = status
		if status.RuntimeHealth != fleet.RuntimeHealthy || status.Endpoint == "" {
			continue
		}
		wait.Add(1)
		go func(index int, status fleet.AgentStatus) {
			defer wait.Done()
			activity, err := s.readActivity(ctx, status)
			if err == nil {
				items[index].Activity = activity
			}
		}(index, status)
	}
	wait.Wait()
	return items
}

func (p *activityClientPool) fetch(
	ctx context.Context,
	status fleet.AgentStatus,
) (*agentActivity, error) {
	client, err := p.client(status.Endpoint)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, agentActivityTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		"http://juex/api/status",
		nil,
	)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("read agent activity: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read agent activity: status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAgentActivityBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read agent activity: %w", err)
	}
	if len(body) > maxAgentActivityBytes {
		return nil, fmt.Errorf("read agent activity: response exceeds %d bytes", maxAgentActivityBytes)
	}
	var activity agentActivity
	if err := json.Unmarshal(body, &activity); err != nil {
		return nil, fmt.Errorf("decode agent activity: %w", err)
	}
	if activity.State != "idle" && activity.State != "working" {
		return nil, fmt.Errorf("decode agent activity: unsupported state %q", activity.State)
	}
	return &activity, nil
}

func (p *activityClientPool) streamStatus(
	ctx context.Context,
	status fleet.AgentStatus,
	onActivity func(*agentActivity),
) error {
	client, err := p.client(status.Endpoint)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://juex/api/status/events", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("stream agent status: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("stream agent status: status %d", response.StatusCode)
	}
	return scanAgentStatusSSE(response.Body, onActivity)
}
