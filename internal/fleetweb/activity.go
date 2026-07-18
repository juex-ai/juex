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
)

const (
	agentActivityTimeout  = time.Second
	maxAgentActivityBytes = 64 << 10
)

type agentActivity struct {
	State        string `json:"state"`
	SessionID    string `json:"session_id,omitempty"`
	SessionAlias string `json:"session_alias,omitempty"`
	PendingCount int    `json:"pending_count"`
}

type agentRosterItem struct {
	fleet.AgentStatus
	Activity *agentActivity `json:"activity,omitempty"`
}

func (s *Server) roster(ctx context.Context, statuses []fleet.AgentStatus) []agentRosterItem {
	items := make([]agentRosterItem, len(statuses))
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

func fetchAgentActivity(
	ctx context.Context,
	status fleet.AgentStatus,
) (*agentActivity, error) {
	target, err := endpoint.Parse(status.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse agent activity endpoint: %w", err)
	}
	transport := target.NewTransport()
	defer transport.CloseIdleConnections()

	requestCtx, cancel := context.WithTimeout(ctx, agentActivityTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		"http://juex/api/activity",
		nil,
	)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
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
