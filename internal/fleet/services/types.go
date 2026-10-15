// Package services manages independent Fleet service processes, not OS service registration.
package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

const (
	Managed  = "managed"
	External = "external"
)

type Definition struct {
	Mode    string         `json:"mode" yaml:"mode"`
	Enabled bool           `json:"enabled" yaml:"enabled"`
	Command []string       `json:"command,omitempty" yaml:"command,omitempty"`
	Network string         `json:"network,omitempty" yaml:"network,omitempty"`
	Address string         `json:"address,omitempty" yaml:"address,omitempty"`
	Config  map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}

func (d Definition) Validate() error {
	switch d.Mode {
	case Managed:
		if len(d.Command) == 0 || strings.TrimSpace(d.Command[0]) == "" {
			return errors.New("managed service requires command")
		}
		if d.Network != "" && d.Network != "tcp" && d.Network != "unix" {
			return errors.New("managed service network must be unix or tcp")
		}
		if d.Address != "" {
			return serviceendpoint.ValidateAddress(d.Network, d.Address)
		}
	case External:
		if len(d.Command) > 0 || len(d.Config) > 0 {
			return errors.New("external service cannot define a command or local configuration")
		}
		return serviceendpoint.ValidateAddress(d.Network, d.Address)
	default:
		return fmt.Errorf("invalid service mode %q", d.Mode)
	}
	return nil
}

type Status struct {
	ID      string                  `json:"id"`
	Mode    string                  `json:"mode"`
	Enabled bool                    `json:"enabled"`
	Desired string                  `json:"desired"`
	Phase   string                  `json:"phase"`
	Reason  string                  `json:"reason,omitempty"`
	Runtime *serviceendpoint.Record `json:"runtime,omitempty"`
}

type Options struct {
	Home                      string
	Definitions               map[string]Definition
	StartTimeout, StopTimeout time.Duration
}
type Manager struct {
	home, fleet               string
	definitions               map[string]Definition
	startTimeout, stopTimeout time.Duration
}
type desiredState struct {
	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error,omitempty"`
}
type launchIntent struct {
	Runtime    serviceendpoint.Record `json:"runtime"`
	Definition Definition             `json:"definition"`
}

func desiredPath(home, id string) string {
	return filepath.Join(serviceendpoint.StateDir(home, id), "desired.json")
}
func New(opts Options) (*Manager, error) {
	if strings.TrimSpace(opts.Home) == "" {
		return nil, errors.New("services: explicit Home is required")
	}
	home, err := filepath.Abs(opts.Home)
	if err != nil {
		return nil, err
	}
	defs := make(map[string]Definition, len(opts.Definitions))
	for id, d := range opts.Definitions {
		if err := serviceendpoint.ValidateID(id); err != nil {
			return nil, err
		}
		if err := d.Validate(); err != nil {
			return nil, fmt.Errorf("service %s: %w", id, err)
		}
		// Own a snapshot so caller mutations cannot race management.
		data, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		var copy Definition
		if err := json.Unmarshal(data, &copy); err != nil {
			return nil, err
		}
		defs[id] = copy
	}
	fleet, err := serviceendpoint.FleetID(home)
	if err != nil {
		return nil, err
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 15 * time.Second
	}
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = 15 * time.Second
	}
	return &Manager{home: home, fleet: fleet, definitions: defs, startTimeout: opts.StartTimeout, stopTimeout: opts.StopTimeout}, nil
}
func (m *Manager) Resolver() serviceendpoint.Resolver {
	return serviceendpoint.FileResolver{Home: m.home, Fleet: m.fleet}
}
func (m *Manager) definition(id string) (Definition, error) {
	if err := serviceendpoint.ValidateID(id); err != nil {
		return Definition{}, err
	}
	d, ok := m.definitions[id]
	if !ok {
		return d, fmt.Errorf("service %q not configured", id)
	}
	return d, nil
}
func (m *Manager) desired(id string) (desiredState, error) {
	var state desiredState
	err := serviceendpoint.ReadJSON(desiredPath(m.home, id), &state)
	if errors.Is(err, os.ErrNotExist) {
		return desiredState{State: "running"}, nil
	}
	if err == nil && state.State != "running" && state.State != "stopped" {
		err = errors.New("invalid desired service state")
	}
	return state, err
}
func (m *Manager) newIntent(id string, d Definition) launchIntent {
	instance := serviceendpoint.NewID()
	network := d.Network
	address := d.Address
	if network == "" {
		network = "unix"
		if runtime.GOOS == "windows" {
			network = "tcp"
		}
	}
	if address == "" {
		if network == "unix" {
			address = serviceendpoint.SocketPath(m.home, m.fleet, id, instance)
		} else {
			address = "0.0.0.0:0"
		}
	}
	return launchIntent{Runtime: serviceendpoint.Record{Identity: serviceendpoint.Identity{FleetID: m.fleet, ServiceID: id, InstanceID: instance}, Network: network, Address: address, StartedAt: time.Now().UTC()}, Definition: d}
}
