package fleetservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) Install(ctx context.Context) (Registration, error) {
	if m == nil {
		return Registration{}, fmt.Errorf("fleet service: manager is nil")
	}
	if m.plan.registration.Platform == PlatformTermux {
		if err := m.validateTermuxInstall(); err != nil {
			return Registration{}, err
		}
	}
	if m.plan.registration.Platform == PlatformLaunchd {
		if err := os.MkdirAll(m.plan.launchdLogDir, 0o700); err != nil {
			return Registration{}, fmt.Errorf("fleet service: create launchd log directory: %w", err)
		}
	}
	if err := publishFiles(m.plan.files); err != nil {
		return Registration{}, err
	}
	var err error
	switch m.plan.registration.Platform {
	case PlatformLaunchd:
		err = m.installLaunchd(ctx)
	case PlatformSystemd:
		err = m.installSystemd(ctx)
	case PlatformTermux:
		err = m.installTermux(ctx)
	default:
		err = fmt.Errorf("fleet service: unsupported platform %q", m.plan.registration.Platform)
	}
	if err != nil {
		return Registration{}, err
	}
	return m.plan.registration, nil
}

func (m *Manager) Uninstall(ctx context.Context) (Registration, error) {
	if m == nil {
		return Registration{}, fmt.Errorf("fleet service: manager is nil")
	}
	var err error
	switch m.plan.registration.Platform {
	case PlatformLaunchd:
		err = m.uninstallLaunchd(ctx)
	case PlatformSystemd:
		err = m.uninstallSystemd(ctx)
	case PlatformTermux:
		err = m.uninstallTermux(ctx)
	default:
		err = fmt.Errorf("fleet service: unsupported platform %q", m.plan.registration.Platform)
	}
	if err != nil {
		return Registration{}, err
	}
	return m.plan.registration, nil
}

func (m *Manager) installLaunchd(ctx context.Context) error {
	loaded, err := m.launchdLoaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		if _, err := m.run(ctx, command{name: "launchctl", args: []string{"bootout", m.plan.launchdTarget}}); err != nil {
			return err
		}
	}
	if _, err := m.run(ctx, command{name: "launchctl", args: []string{"bootstrap", m.plan.launchdDomain, m.plan.registration.DefinitionPath}}); err != nil {
		return err
	}
	_, err = m.run(ctx, command{name: "launchctl", args: []string{"kickstart", "-k", m.plan.launchdTarget}})
	return err
}

func (m *Manager) uninstallLaunchd(ctx context.Context) error {
	loaded, err := m.launchdLoaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		if _, err := m.run(ctx, command{name: "launchctl", args: []string{"bootout", m.plan.launchdTarget}}); err != nil {
			return err
		}
		if err := m.waitLaunchdUnloaded(ctx); err != nil {
			return err
		}
	}
	if err := os.Remove(m.plan.registration.DefinitionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fleet service: remove %s: %w", m.plan.registration.DefinitionPath, err)
	}
	return nil
}

func (m *Manager) waitLaunchdUnloaded(ctx context.Context) error {
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		loaded, err := m.launchdLoaded(waitCtx)
		if err != nil {
			return err
		}
		if !loaded {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("fleet service: wait for launchd service %s to unload: %w", m.plan.registration.Name, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (m *Manager) launchdLoaded(ctx context.Context) (bool, error) {
	output, err := m.runner.Run(ctx, command{name: "launchctl", args: []string{"print", m.plan.launchdTarget}})
	if err == nil {
		return true, nil
	}
	if launchdServiceAbsent(output, err) {
		return false, nil
	}
	return false, commandFailure(command{name: "launchctl", args: []string{"print", m.plan.launchdTarget}}, output, err)
}

func launchdServiceAbsent(output string, err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(output + " " + err.Error())
	return strings.Contains(message, "could not find service") || strings.Contains(message, "service not found")
}

func (m *Manager) installSystemd(ctx context.Context) error {
	for _, cmd := range []command{
		{name: "systemctl", args: []string{"--user", "daemon-reload"}},
		{name: "systemctl", args: []string{"--user", "enable", m.plan.systemdUnit}},
		{name: "systemctl", args: []string{"--user", "restart", m.plan.systemdUnit}},
	} {
		if _, err := m.run(ctx, cmd); err != nil {
			return err
		}
	}
	return nil
}

type systemdState struct {
	load   string
	active string
}

func (s systemdState) absent() bool {
	return s.load == "not-found" && s.active == "inactive"
}

func (m *Manager) uninstallSystemd(ctx context.Context) error {
	state, err := m.systemdState(ctx)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(m.plan.registration.DefinitionPath)
	definitionPresent := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("fleet service: inspect %s: %w", m.plan.registration.DefinitionPath, statErr)
	}
	if state.absent() && !definitionPresent {
		return nil
	}
	if !state.absent() {
		if _, err := m.run(ctx, command{name: "systemctl", args: []string{"--user", "disable", "--now", m.plan.systemdUnit}}); err != nil {
			return err
		}
	}
	if err := os.Remove(m.plan.registration.DefinitionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fleet service: remove %s: %w", m.plan.registration.DefinitionPath, err)
	}
	if _, err := m.run(ctx, command{name: "systemctl", args: []string{"--user", "daemon-reload"}}); err != nil {
		return err
	}
	state, err = m.systemdState(ctx)
	if err != nil {
		return err
	}
	if !state.absent() {
		return fmt.Errorf("fleet service: systemd unit %s remained load=%s active=%s after uninstall", m.plan.systemdUnit, state.load, state.active)
	}
	return nil
}

func (m *Manager) systemdState(ctx context.Context) (systemdState, error) {
	cmd := command{name: "systemctl", args: []string{"--user", "show", m.plan.systemdUnit, "--property=LoadState", "--property=ActiveState"}}
	output, err := m.runner.Run(ctx, cmd)
	if err != nil {
		return systemdState{}, commandFailure(cmd, output, err)
	}
	state := systemdState{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "LoadState":
			state.load = value
		case "ActiveState":
			state.active = value
		}
	}
	if state.load == "" || state.active == "" {
		return systemdState{}, fmt.Errorf("fleet service: systemctl returned incomplete state for %s: %q", m.plan.systemdUnit, boundedOutput(output))
	}
	return state, nil
}

func (m *Manager) validateTermuxInstall() error {
	serviceRoot := filepath.Join(m.plan.termuxPrefix, "var", "service")
	info, err := os.Stat(serviceRoot)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("fleet service: Termux service root %s is unavailable; install termux-services and initialize its service daemon", serviceRoot)
	}
	for _, name := range []string{"sh", "sv", "sv-enable", "sv-disable", "svlogger"} {
		path := filepath.Join(m.plan.termuxPrefix, "bin", name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			return fmt.Errorf("fleet service: required Termux command %s is unavailable; run pkg install termux-services", path)
		}
	}
	return nil
}

func (m *Manager) installTermux(ctx context.Context) error {
	_, err := m.runTermux(ctx, "sv-enable", m.plan.registration.Name)
	return err
}

func (m *Manager) uninstallTermux(ctx context.Context) error {
	info, err := os.Stat(m.plan.termuxDir)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fleet service: Termux service directory %s is missing; cannot confirm that the supervisor is stopped safely", m.plan.termuxDir)
	}
	if err != nil {
		return fmt.Errorf("fleet service: inspect Termux service directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("fleet service: Termux service path %s is not a directory", m.plan.termuxDir)
	}
	if _, err := m.runTermux(ctx, "sv-disable", m.plan.registration.Name); err != nil {
		return err
	}
	if _, err := m.runTermux(ctx, "sv", "-w", "15", "down", m.plan.registration.Name); err != nil {
		return err
	}
	statusCmd := m.termuxCommand("sv", "status", m.plan.registration.Name)
	output, statusErr := m.runner.Run(ctx, statusCmd)
	if !strings.HasPrefix(strings.TrimSpace(output), "down:") {
		if statusErr != nil {
			return commandFailure(statusCmd, output, statusErr)
		}
		return fmt.Errorf("fleet service: Termux service %s did not report down: %q", m.plan.registration.Name, boundedOutput(output))
	}
	if err := os.RemoveAll(m.plan.termuxDir); err != nil {
		return fmt.Errorf("fleet service: remove Termux service directory: %w", err)
	}
	return nil
}

func (m *Manager) runTermux(ctx context.Context, name string, args ...string) (string, error) {
	return m.run(ctx, m.termuxCommand(name, args...))
}

func (m *Manager) termuxCommand(name string, args ...string) command {
	return command{
		name: filepath.Join(m.plan.termuxPrefix, "bin", name),
		args: args,
		env: map[string]string{
			"SVDIR":  filepath.Join(m.plan.termuxPrefix, "var", "service"),
			"LOGDIR": filepath.Join(m.plan.termuxPrefix, "var", "log"),
		},
	}
}

func (m *Manager) run(ctx context.Context, cmd command) (string, error) {
	output, err := m.runner.Run(ctx, cmd)
	if err != nil {
		if m.plan.registration.Platform == PlatformTermux {
			return output, fmt.Errorf("%w; ensure termux-services is installed and source %s", commandFailure(cmd, output, err), filepath.Join(m.plan.termuxPrefix, "etc", "profile.d", "start-services.sh"))
		}
		return output, commandFailure(cmd, output, err)
	}
	return output, nil
}
