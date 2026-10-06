package hostservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const signature = "JueX native executor service"

type Manager struct{ StateDirectory, Executable, Home, ConfigHome, OS, Path string }

func New(directory, executable string) (*Manager, error) {
	if !filepath.IsAbs(directory) || strings.ContainsAny(directory, "\r\n\x00") {
		return nil, errors.New("service state must be an absolute path without control characters")
	}
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(executable) {
		return nil, errors.New("executor executable must be an absolute path")
	}
	binary, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return &Manager{StateDirectory: directory, Executable: binary, Home: home, ConfigHome: config, OS: runtime.GOOS, Path: os.Getenv("PATH")}, nil
}

// Remove releases only this executor's owned service definition after stopping.
func (m *Manager) Remove(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	if err := m.Autostart(ctx, false); err != nil {
		return err
	}
	if m.OS != "linux" {
		return nil
	}
	data, err := os.ReadFile(m.unitPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Contains(data, []byte(signature)) {
		return errors.New("refusing to remove an unrelated service definition")
	}
	if err := os.Remove(m.unitPath()); err != nil {
		return err
	}
	return run(ctx, "systemctl", "--user", "daemon-reload")
}
func (m *Manager) label() string {
	hash := sha256.Sum256([]byte(m.StateDirectory))
	return fmt.Sprintf("ai.juex.executor.%x", hash[:12])
}
func (m *Manager) unitPath() string {
	return filepath.Join(m.ConfigHome, "systemd", "user", m.label()+".service")
}
func (m *Manager) agentPath() string {
	return filepath.Join(m.Home, "Library", "LaunchAgents", m.label()+".plist")
}
func (m *Manager) Status() (Status, error) {
	v, err := readStatus(m.StateDirectory)
	if err != nil {
		return v, err
	}
	path := m.agentPath()
	if m.OS == "linux" {
		path = filepath.Join(m.ConfigHome, "systemd", "user", "default.target.wants", m.label()+".service")
	}
	_, err = os.Lstat(path)
	v.Autostart = err == nil
	if err != nil && !os.IsNotExist(err) {
		return v, err
	}
	return v, nil
}
func run(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	output := &limitedOutput{}
	command.Stdout, command.Stderr = output, output
	err := command.Run()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(output.String()))
	}
	return nil
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	_, _ = b.Buffer.Write(p[:min(len(p), max(0, 8192-b.Len()))])
	return len(p), nil
}
func (m *Manager) domain(ctx context.Context) string {
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if run(ctx, "launchctl", "print", domain) != nil {
		return "user/" + strconv.Itoa(os.Getuid())
	}
	return domain
}
func (m *Manager) install(path string) error {
	data, err := m.definition()
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil && !bytes.Contains(old, []byte(signature)) {
		return errors.New("refusing to replace an unrelated service definition")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writePrivate(path, data)
}

func (m *Manager) Start(ctx context.Context) (Status, error) {
	v, err := m.Status()
	if err != nil {
		return v, err
	}
	if _, err := StopRequested(m.StateDirectory); err != nil {
		return v, err
	}
	if err := os.Remove(filepath.Join(m.StateDirectory, "service-stop")); err != nil && !os.IsNotExist(err) {
		return v, err
	}
	if v.Running {
		return v, nil
	}
	switch m.OS {
	case "linux":
		if err := m.install(m.unitPath()); err != nil {
			return v, err
		}
		if err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return v, err
		}
		if err := run(ctx, "systemctl", "--user", "start", m.label()+".service"); err != nil {
			return v, err
		}
	case "darwin":
		path := filepath.Join(m.StateDirectory, "launchagent.plist")
		if err := m.install(path); err != nil {
			return v, err
		}
		domain := m.domain(ctx)
		if err := run(ctx, "launchctl", "enable", domain+"/"+m.label()); err != nil {
			return v, err
		}
		if run(ctx, "launchctl", "print", domain+"/"+m.label()) != nil {
			if err := run(ctx, "launchctl", "bootstrap", domain, path); err != nil {
				return v, err
			}
		}
		if err := run(ctx, "launchctl", "kickstart", domain+"/"+m.label()); err != nil {
			return v, err
		}
	default:
		return v, errors.New("background executor supports Linux systemd --user and macOS launchd")
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-deadline.C:
			return v, errors.New("executor did not become ready; inspect juex-executor logs")
		case <-ticker.C:
		}
		v, err = m.Status()
		if err != nil || v.Running {
			return v, err
		}
	}
}
func (m *Manager) Stop(ctx context.Context) error {
	v, err := m.Status()
	if err != nil {
		return err
	}
	if v.Running && !v.Background {
		return errors.New("executor is running in a terminal; stop its run command with Ctrl+C")
	}
	if err := writePrivate(filepath.Join(m.StateDirectory, "service-stop"), nil); err != nil {
		return err
	}
	switch m.OS {
	case "linux":
		if _, err := os.Stat(m.unitPath()); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		err = run(ctx, "systemctl", "--user", "stop", m.label()+".service")
	case "darwin":
		return m.stopDarwin(ctx, v)
	default:
		return errors.New("background executor supports Linux and macOS")
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := m.Status()
		if err != nil {
			return err
		}
		if !status.Running {
			return confirmedStop(status)
		}
		select {
		case <-ctx.Done():
			return errors.New("executor stop has not been confirmed")
		case <-ticker.C:
		}
	}
}

func confirmedStop(status Status) error {
	if status.PID > 1 && !status.CleanExit {
		return errors.New("executor exited without a clean shutdown receipt")
	}
	return nil
}

func launchdPID(ctx context.Context, target string) (int, bool, error) {
	output, err := exec.CommandContext(ctx, "launchctl", "print", target).CombinedOutput()
	if err != nil {
		if bytes.Contains(output, []byte("Could not find service")) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("inspect executor service: %w", err)
	}
	for line := range strings.SplitSeq(string(output), "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "pid = "); found {
			pid, err := strconv.Atoi(value)
			return pid, true, err
		}
	}
	return 0, true, nil
}

func (m *Manager) stopDarwin(ctx context.Context, previous Status) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	target := m.domain(ctx) + "/" + m.label()
	pid, _, err := launchdPID(ctx, target)
	if err != nil {
		return err
	}
	if pid > 0 {
		// bootout may escalate to SIGKILL. Signal cooperatively first; the
		// startup marker makes even an abnormal KeepAlive restart exit idle.
		if err := run(ctx, "launchctl", "kill", "SIGTERM", target); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := m.Status()
		if err != nil {
			return err
		}
		pid, loaded, err := launchdPID(ctx, target)
		if err != nil {
			return err
		}
		if !status.Running && pid == 0 {
			if err := confirmedStop(status); err != nil {
				return err
			}
			if previous.Running && status.Fingerprint != previous.Fingerprint {
				return errors.New("executor process changed during shutdown")
			}
			if loaded {
				return run(ctx, "launchctl", "bootout", target)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("executor stop has not been confirmed; no force stop was sent")
		case <-ticker.C:
		}
	}
}
func (m *Manager) Autostart(ctx context.Context, enabled bool) error {
	switch m.OS {
	case "linux":
		if enabled {
			if err := m.install(m.unitPath()); err != nil {
				return err
			}
			if err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
				return err
			}
			return run(ctx, "systemctl", "--user", "enable", m.label()+".service")
		}
		if _, err := os.Stat(m.unitPath()); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		return run(ctx, "systemctl", "--user", "disable", m.label()+".service")
	case "darwin":
		if enabled {
			if err := m.install(m.agentPath()); err != nil {
				return err
			}
			return run(ctx, "launchctl", "enable", m.domain(ctx)+"/"+m.label())
		}
		path := m.agentPath()
		data, err := readPrivate(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !bytes.Contains(data, []byte(signature)) {
			return errors.New("refusing to remove an unrelated launch agent")
		}
		return os.Remove(path)
	default:
		return errors.New("autostart supports Linux and macOS")
	}
}

func systemdQuote(value string) string    { return strconv.Quote(strings.ReplaceAll(value, "%", "%%")) }
func systemdArgument(value string) string { return systemdQuote(strings.ReplaceAll(value, "$", "$$")) }
func (m *Manager) definition() ([]byte, error) {
	switch m.OS {
	case "linux":
		return []byte(fmt.Sprintf("# %s\n[Unit]\nDescription=JueX native executor\n\n[Service]\nType=simple\nExecStart=%s --state %s run --background-log\nEnvironment=%s\nUMask=0077\nRestart=on-failure\nRestartSec=10\nTimeoutStopSec=20\nSendSIGKILL=no\nStandardOutput=null\nStandardError=null\n\n[Install]\nWantedBy=default.target\n", signature, systemdArgument(m.Executable), systemdArgument(m.StateDirectory), systemdQuote("PATH="+m.Path))), nil
	case "darwin":
		text := func(value string) string {
			var b bytes.Buffer
			_ = xml.EscapeText(&b, []byte(value))
			return b.String()
		}
		return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!-- %s -->
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>--state</string><string>%s</string><string>run</string><string>--background-log</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ThrottleInterval</key><integer>10</integer>
<key>WorkingDirectory</key><string>%s</string>
<key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict>
<key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>/dev/null</string>
</dict></plist>
`, signature, text(m.label()), text(m.Executable), text(m.StateDirectory), text(m.StateDirectory), text(m.Path))), nil
	default:
		return nil, errors.New("unsupported native service platform")
	}
}
