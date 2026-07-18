// Package fleetservice registers the resident fleet supervisor with the
// current user's native service manager.
package fleetservice

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Platform string

const (
	PlatformLaunchd Platform = "launchd"
	PlatformSystemd Platform = "systemd-user"
	PlatformTermux  Platform = "termux-services"
)

type Options struct {
	HomeDir       string
	Executable    string
	UnsafeBindAny bool
}

type Registration struct {
	Platform       Platform `json:"platform"`
	Name           string   `json:"name"`
	DefinitionPath string   `json:"definition_path"`
	Notes          []string `json:"notes,omitempty"`
}

type InstalledServeOptions struct {
	Addr          string
	UnsafeBindAny bool
}

type Manager struct {
	plan   registrationPlan
	runner commandRunner
}

type hostInfo struct {
	goos          string
	userHome      string
	uid           int
	xdgConfigHome string
	termuxPrefix  string
}

type definitionFile struct {
	path string
	data []byte
	mode os.FileMode
}

type registrationPlan struct {
	registration Registration
	files        []definitionFile

	launchdDomain string
	launchdTarget string
	launchdLogDir string
	systemdUnit   string
	termuxPrefix  string
	termuxDir     string
}

type command struct {
	name string
	args []string
	env  map[string]string
}

type commandRunner interface {
	Run(context.Context, command) (string, error)
}

func New(opts Options) (*Manager, error) {
	host, err := currentHostInfo()
	if err != nil {
		return nil, err
	}
	return newManagerForHost(opts, host, execCommandRunner{})
}

func newManagerForHost(opts Options, host hostInfo, runner commandRunner) (*Manager, error) {
	plan, err := buildPlan(opts, host)
	if err != nil {
		return nil, err
	}
	if runner == nil {
		return nil, fmt.Errorf("fleet service: command runner is required")
	}
	return &Manager{plan: plan, runner: runner}, nil
}

func currentHostInfo() (hostInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return hostInfo{}, fmt.Errorf("fleet service: resolve user home: %w", err)
	}
	host := hostInfo{
		goos:          runtime.GOOS,
		userHome:      home,
		xdgConfigHome: strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")),
	}
	if runtime.GOOS == "darwin" {
		current, err := user.Current()
		if err != nil {
			return hostInfo{}, fmt.Errorf("fleet service: resolve current user: %w", err)
		}
		host.uid, err = strconv.Atoi(current.Uid)
		if err != nil {
			return hostInfo{}, fmt.Errorf("fleet service: parse current uid %q: %w", current.Uid, err)
		}
	}
	if runtime.GOOS == "linux" {
		prefix := strings.TrimSpace(os.Getenv("TERMUX__PREFIX"))
		if prefix == "" {
			prefix = strings.TrimSpace(os.Getenv("PREFIX"))
		}
		termuxEvidence := strings.TrimSpace(os.Getenv("TERMUX_VERSION")) != "" ||
			strings.TrimSpace(os.Getenv("TERMUX_APP__PACKAGE_NAME")) != "" ||
			strings.TrimSpace(os.Getenv("TERMUX__PREFIX")) != "" ||
			strings.Contains(filepath.ToSlash(prefix), "/com.termux/")
		if termuxEvidence {
			host.termuxPrefix = prefix
		}
	}
	return host, nil
}
