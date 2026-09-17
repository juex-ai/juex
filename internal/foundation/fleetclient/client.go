// Package fleetclient provides the typed, instance-checked Fleet management
// protocol. Clients can be constructed while Fleet is offline.
package fleetclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

const (
	ProfileAgent      = "agent"
	ProfileSupervisor = "supervisor"
	FleetHeader       = "X-Juex-Fleet-ID"
	InstanceHeader    = "X-Juex-Instance-ID"
	ProfileHeader     = "X-Juex-Profile"
	AgentHeader       = "X-Juex-Agent-ID"
	Path              = "/api/management/agents"
)

type Caller struct{ Profile, AgentID string }
type Endpoint struct {
	FleetID    string `json:"fleet_id"`
	InstanceID string `json:"instance_id"`
	URL        string `json:"url"`
}

func EndpointPath(home string) string { return filepath.Join(home, "run", "fleet-management.json") }
func Publish(home string, endpoint Endpoint) error {
	return serviceendpoint.WriteJSON(EndpointPath(home), endpoint)
}

type Agent struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Workspace     string `json:"workspace"`
	Enabled       bool   `json:"enabled"`
	Autostart     bool   `json:"autostart"`
	RuntimeHealth string `json:"runtime_health"`
	Problem       string `json:"problem,omitempty"`
}
type CreateRequest struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name,omitempty"`
	Start     bool   `json:"start"`
}
type Config struct {
	Content         string `json:"content"`
	Revision        string `json:"revision"`
	Exists          bool   `json:"exists"`
	RestartRequired bool   `json:"restart_required"`
}
type ConfigRequest struct {
	Content          string `json:"content"`
	ExpectedRevision string `json:"expected_revision"`
	Apply            bool   `json:"apply"`
	Interrupt        bool   `json:"interrupt"`
}
type LifecycleRequest struct {
	Action    string `json:"action"`
	Interrupt bool   `json:"interrupt"`
}
type Result struct {
	Agent            Agent  `json:"agent"`
	Revision         string `json:"revision,omitempty"`
	Saved            bool   `json:"saved"`
	Published        bool   `json:"published"`
	Applied          bool   `json:"applied"`
	Restarted        bool   `json:"restarted"`
	RestartRequired  bool   `json:"restart_required"`
	Deferred         bool   `json:"deferred"`
	BehaviorVerified bool   `json:"behavior_verified"`
	Error            string `json:"error,omitempty"`
}

type Client struct {
	home   string
	caller Caller
	http   *http.Client
}

func New(home, profile, agentID string) *Client {
	if profile == "" {
		profile = ProfileAgent
	}
	return &Client{home: home, caller: Caller{Profile: profile, AgentID: agentID}, http: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var result []Agent
	err := c.call(ctx, http.MethodGet, Path, nil, &result)
	return result, err
}
func (c *Client) Create(ctx context.Context, request CreateRequest) (Result, error) {
	var result Result
	err := c.call(ctx, http.MethodPost, Path, request, &result)
	return result, err
}
func (c *Client) Config(ctx context.Context, id string) (Config, error) {
	var result Config
	err := c.call(ctx, http.MethodGet, Path+"/"+url.PathEscape(id)+"/config", nil, &result)
	return result, err
}
func (c *Client) Configure(ctx context.Context, id string, request ConfigRequest) (Result, error) {
	var result Result
	err := c.call(ctx, http.MethodPut, Path+"/"+url.PathEscape(id)+"/config", request, &result)
	return result, err
}
func (c *Client) Lifecycle(ctx context.Context, id string, request LifecycleRequest) (Result, error) {
	var result Result
	err := c.call(ctx, http.MethodPost, Path+"/"+url.PathEscape(id)+"/lifecycle", request, &result)
	return result, err
}

func (c *Client) call(ctx context.Context, method, path string, input, output any) error {
	var endpoint Endpoint
	if err := serviceendpoint.ReadJSON(EndpointPath(c.home), &endpoint); err != nil {
		return fmt.Errorf("fleet client: discover management endpoint: %w", err)
	}
	if endpoint.FleetID == "" || endpoint.InstanceID == "" {
		return fmt.Errorf("fleet client: missing endpoint identity")
	}
	fleetID, err := serviceendpoint.FleetID(c.home)
	if err != nil {
		return err
	}
	if fleetID != endpoint.FleetID {
		return fmt.Errorf("fleet client: endpoint identity differs from owning Home")
	}
	var body []byte
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.URL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(FleetHeader, endpoint.FleetID)
	request.Header.Set(InstanceHeader, endpoint.InstanceID)
	request.Header.Set(ProfileHeader, c.caller.Profile)
	request.Header.Set(AgentHeader, c.caller.AgentID)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("fleet client: %w", err)
	}
	defer response.Body.Close()
	if response.Header.Get(FleetHeader) != endpoint.FleetID || response.Header.Get(InstanceHeader) != endpoint.InstanceID {
		return fmt.Errorf("fleet client: response identity mismatch")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("fleet client: HTTP %d: %s", response.StatusCode, data)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("fleet client: decode response: %w", err)
	}
	return nil
}
