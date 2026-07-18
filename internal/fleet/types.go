package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
)

type BindingState string

type RuntimeHealth string

const (
	BindingBound    BindingState = "bound"
	BindingOrphaned BindingState = "orphaned"
	BindingInvalid  BindingState = "invalid"

	RuntimeHealthy   RuntimeHealth = "healthy"
	RuntimeStopped   RuntimeHealth = "stopped"
	RuntimeUnhealthy RuntimeHealth = "unhealthy"
	RuntimeAmbiguous RuntimeHealth = "ambiguous"
)

type AgentStatus struct {
	ID                string        `json:"id"`
	Name              string        `json:"name,omitempty"`
	Workspace         string        `json:"workspace,omitempty"`
	Enabled           bool          `json:"enabled"`
	Autostart         bool          `json:"autostart"`
	Binding           BindingState  `json:"binding"`
	RuntimeHealth     RuntimeHealth `json:"runtime_health"`
	PID               int           `json:"pid,omitempty"`
	Endpoint          string        `json:"endpoint,omitempty"`
	StartedAt         time.Time     `json:"started_at,omitempty"`
	BinaryVersion     string        `json:"binary_version,omitempty"`
	RuntimePresent    bool          `json:"runtime_present"`
	ProcessAlive      bool          `json:"process_alive"`
	EndpointReachable bool          `json:"endpoint_reachable"`
	EndpointMatched   bool          `json:"endpoint_matched"`
	Problem           string        `json:"problem,omitempty"`
}

type Action struct {
	AgentID string `json:"agent_id,omitempty"`
	Kind    string `json:"kind"`
	Detail  string `json:"detail,omitempty"`
	Err     error  `json:"-"`
}

type GCCandidate struct {
	AgentID      string    `json:"agent_id"`
	Workspace    string    `json:"workspace"`
	SizeBytes    int64     `json:"size_bytes"`
	LastActivity time.Time `json:"last_activity,omitempty"`
	Running      bool      `json:"running"`
	Reason       string    `json:"reason"`
}

type AddOptions struct {
	Workspace string
	Name      *string
	Autostart *bool
	Start     bool
}

type AddResult struct {
	Agent   AgentStatus `json:"agent"`
	Created bool        `json:"created"`
}

type RemoveOptions struct {
	ConfirmName      string
	SkipConfirmation bool
}

type RemovedAgent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
}

type Options struct {
	HomeDir      string
	Executable   string
	StartTimeout time.Duration
	StopTimeout  time.Duration
	ProbeTimeout time.Duration
}

type ValidationError struct {
	Reason string
}

func (e *ValidationError) Error() string {
	return "fleet: " + e.Reason
}

type NotFoundError struct {
	Selector string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("fleet: agent %q not found", e.Selector)
}

type AmbiguousSelectorError struct {
	Selector string
	IDs      []string
}

func (e *AmbiguousSelectorError) Error() string {
	return fmt.Sprintf("fleet: agent name %q is ambiguous; matching ids: %s", e.Selector, strings.Join(e.IDs, ", "))
}

type ConflictError struct {
	AgentID string
	Reason  string
}

func (e *ConflictError) Error() string {
	if e.AgentID == "" {
		return "fleet: " + e.Reason
	}
	return fmt.Sprintf("fleet: agent %q: %s", e.AgentID, e.Reason)
}

type maintenanceGuard interface {
	Close() error
}

type noopGuard struct{}

func (noopGuard) Close() error { return nil }

type spawnedProcess struct {
	PID     int
	Done    <-chan error
	LogPath string
}

type dependencies struct {
	listRegistry       func(string) ([]agentstate.RegistryEntry, error)
	inspectBinding     func(agentstate.RegistryEntry) agentstate.WorkspaceBinding
	resolveAgent       func(agentstate.Options) (agentstate.Resolution, error)
	updateAgent        func(string, string, agentstate.AgentUpdate) (agentstate.Agent, error)
	deleteRegistered   func(string, string) error
	readRuntime        func(string) (endpoint.Runtime, error)
	removeRuntime      func(string, endpoint.Runtime) error
	acquireMaintenance func(string) (maintenanceGuard, error)
	processAlive       func(int) (bool, error)
	probe              func(context.Context, endpoint.Runtime) error
	requestShutdown    func(context.Context, endpoint.Runtime) error
	spawn              func(string, string, agentstate.RegistryEntry) (spawnedProcess, error)
}

func defaultDependencies() dependencies {
	return dependencies{
		listRegistry:     agentstate.ListRegistry,
		inspectBinding:   agentstate.InspectBinding,
		resolveAgent:     agentstate.Resolve,
		updateAgent:      agentstate.UpdateAgent,
		deleteRegistered: agentstate.DeleteRegistered,
		readRuntime:      endpoint.ReadRuntime,
		removeRuntime:    endpoint.RemoveRuntime,
		acquireMaintenance: func(agentDir string) (maintenanceGuard, error) {
			return endpoint.AcquireMaintenance(agentDir)
		},
		processAlive:    processExists,
		probe:           endpoint.Probe,
		requestShutdown: endpoint.RequestShutdown,
		spawn:           spawnDetached,
	}
}

type Manager struct {
	homeDir      string
	executable   string
	startTimeout time.Duration
	stopTimeout  time.Duration
	probeTimeout time.Duration
	deps         dependencies
}

func New(opts Options) (*Manager, error) {
	homeDir := strings.TrimSpace(opts.HomeDir)
	if homeDir == "" {
		var err error
		homeDir, err = agentstate.EffectiveHome()
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		homeDir, err = filepath.Abs(filepath.Clean(homeDir))
		if err != nil {
			return nil, fmt.Errorf("fleet: resolve home: %w", err)
		}
	}
	executable := strings.TrimSpace(opts.Executable)
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("fleet: resolve executable: %w", err)
		}
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 15 * time.Second
	}
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = 15 * time.Second
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = time.Second
	}
	return &Manager{
		homeDir:      homeDir,
		executable:   executable,
		startTimeout: opts.StartTimeout,
		stopTimeout:  opts.StopTimeout,
		probeTimeout: opts.ProbeTimeout,
		deps:         defaultDependencies(),
	}, nil
}

func resolveSelector(entries []agentstate.RegistryEntry, selector string) (agentstate.RegistryEntry, error) {
	for _, entry := range entries {
		if entry.ID == selector {
			return entry, nil
		}
	}
	var matches []agentstate.RegistryEntry
	for _, entry := range entries {
		if entry.Problem == "" && entry.Agent.Name == selector {
			matches = append(matches, entry)
		}
	}
	switch len(matches) {
	case 0:
		return agentstate.RegistryEntry{}, &NotFoundError{Selector: selector}
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.ID)
		}
		sort.Strings(ids)
		return agentstate.RegistryEntry{}, &AmbiguousSelectorError{Selector: selector, IDs: ids}
	}
}
