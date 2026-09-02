package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/fleetservice"
	"github.com/juex-ai/juex/internal/version"
)

type fakeFleetServiceInstaller struct {
	existing      fleetservice.InstalledServeOptions
	existingFound bool
	existingErr   error
	existingCalls int
	registration  fleetservice.Registration
	installCalls  int
}

func (f *fakeFleetServiceInstaller) ExistingServeOptions() (
	fleetservice.InstalledServeOptions,
	bool,
	error,
) {
	f.existingCalls++
	return f.existing, f.existingFound, f.existingErr
}

func (f *fakeFleetServiceInstaller) Install(context.Context) (fleetservice.Registration, error) {
	f.installCalls++
	return f.registration, nil
}

type fakeFleetAgentRestarter struct {
	result fleet.RestartAgentsResult
	err    error
	calls  int
}

func (f *fakeFleetAgentRestarter) RestartRunningAgents(context.Context) (
	fleet.RestartAgentsResult,
	error,
) {
	f.calls++
	return f.result, f.err
}

func TestFleetStatusDoesNotCreateWorkspaceIdentity(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	t.Chdir(work)

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"fleet", "status", "--format", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != "[]" {
		t.Fatalf("status output = %q, want []", output.String())
	}
	if _, err := os.Stat(filepath.Join(work, ".juex")); !os.IsNotExist(err) {
		t.Fatalf("fleet status created workspace identity: %v", err)
	}
}

func TestMapFleetErrorTreatsUnavailableLogAsNotFound(t *testing.T) {
	source := &fleet.LogUnavailableError{
		AgentID: "aaaaaa",
		Path:    "/private/fleet.log",
	}

	err := mapFleetError(source)

	var missing *notFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %T %v, want notFoundError", err, err)
	}
	if err.Error() != source.Error() {
		t.Fatalf("error = %q, want %q", err, source)
	}
}

func TestFleetStatusPreservesOrthogonalState(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	writeFleetAgentFixture(t, home, workspace, "aaaaaa", "alpha")

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"fleet", "status", "--format", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(output.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 ||
		rows[0]["binding"] != "bound" ||
		rows[0]["runtime_health"] != "stopped" ||
		rows[0]["enabled"] != true ||
		rows[0]["autostart"] != false {
		t.Fatalf("status rows = %#v", rows)
	}
}

func TestFleetRejectsWorkspaceConfigAndModelOverrides(t *testing.T) {
	for _, args := range [][]string{
		{"-C", t.TempDir(), "fleet", "status"},
		{"--config", filepath.Join(t.TempDir(), "juex.yaml"), "fleet", "status"},
		{"--models", "openai:test", "fleet", "status"},
	} {
		root := newRootCmd()
		root.SetArgs(args)
		err := root.Execute()
		var usage *usageError
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("args %v error = %T %v, want unsupported usage error", args, err, err)
		}
		if _, ok := err.(*usageError); !ok {
			t.Fatalf("args %v error = %T, want *usageError", args, usage)
		}
	}
}

func TestFleetGCConfirmationControlsDeletion(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "missing-workspace")
	t.Setenv("JUEX_HOME", home)
	agentDir := writeFleetAgentFixture(t, home, workspace, "aaaaaa", "alpha")
	if err := os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetIn(strings.NewReader("n\n"))
	root.SetArgs([]string{"fleet", "gc"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agentDir); err != nil {
		t.Fatalf("denied GC removed agent directory: %v", err)
	}

	root = newRootCmd()
	output.Reset()
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"fleet", "gc", "--yes"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("confirmed GC preserved agent directory: %v", err)
	}
}

func TestFleetAddEnableDisableAndRemove(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("JUEX_HOME", home)

	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{
		"fleet", "add", workspace,
		"--name", "alpha",
		"--autostart",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	entries, err := agentstate.ListRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 ||
		entries[0].Agent.Name != "alpha" ||
		!entries[0].Agent.Enabled ||
		!entries[0].Agent.Autostart {
		t.Fatalf("registered agents = %+v", entries)
	}
	agentID := entries[0].ID
	if !strings.Contains(output.String(), agentID) ||
		!strings.Contains(output.String(), "alpha") {
		t.Fatalf("add output = %q", output.String())
	}

	for _, action := range []struct {
		name        string
		wantEnabled bool
	}{
		{name: "disable", wantEnabled: false},
		{name: "enable", wantEnabled: true},
	} {
		root = newRootCmd()
		output.Reset()
		root.SetOut(&output)
		root.SetErr(&output)
		root.SetArgs([]string{"fleet", action.name, agentID})
		if err := root.Execute(); err != nil {
			t.Fatalf("fleet %s: %v", action.name, err)
		}
		entries, err = agentstate.ListRegistry(home)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Agent.Enabled != action.wantEnabled {
			t.Fatalf("after %s registry = %+v", action.name, entries)
		}
	}

	agentDir := filepath.Join(home, "agents", agentID)
	markerPath := filepath.Join(workspace, ".juex", "juex.local.json")
	root = newRootCmd()
	output.Reset()
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetIn(strings.NewReader("n\n"))
	root.SetArgs([]string{"fleet", "remove", agentID})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{agentDir, markerPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cancelled remove changed %s: %v", path, err)
		}
	}

	root = newRootCmd()
	output.Reset()
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetIn(strings.NewReader("y\n"))
	root.SetArgs([]string{"fleet", "remove", agentID})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{agentDir, markerPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("confirmed remove preserved %s: %v", path, err)
		}
	}
	if !strings.Contains(output.String(), "Removed") {
		t.Fatalf("remove output = %q", output.String())
	}
}

func TestFleetAddValidationMapsToUsageError(t *testing.T) {
	t.Setenv("JUEX_HOME", t.TempDir())
	root := newRootCmd()
	root.SetArgs([]string{"fleet", "add", "relative"})
	err := root.Execute()
	var usage *usageError
	if !errors.As(err, &usage) {
		t.Fatalf("error = %T %v, want usage error", err, err)
	}
}

func TestFleetHelpExposesCommandsAndFlags(t *testing.T) {
	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"fleet", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"serve",
		"status",
		"add",
		"enable",
		"disable",
		"remove",
		"start",
		"stop",
		"restart",
		"logs",
		"gc",
		"install",
		"uninstall",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("fleet help missing %q:\n%s", want, output.String())
		}
	}

	root = newRootCmd()
	output.Reset()
	root.SetOut(&output)
	root.SetArgs([]string{"fleet", "serve", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--addr", "--unsafe-bind-any"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("fleet serve help missing %q:\n%s", want, output.String())
		}
	}

	for _, test := range []struct {
		command string
		flag    string
	}{
		{command: "logs", flag: "--lines"},
		{command: "gc", flag: "--yes"},
	} {
		root = newRootCmd()
		output.Reset()
		root.SetOut(&output)
		root.SetArgs([]string{"fleet", test.command, "--help"})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), test.flag) {
			t.Fatalf("fleet %s help missing %q:\n%s", test.command, test.flag, output.String())
		}
	}

}

func TestFleetHelpAdvertisesOnlyAcceptedInheritedFlags(t *testing.T) {
	root := newRootCmd()
	fleetCommand, _, err := root.Find([]string{"fleet"})
	if err != nil {
		t.Fatal(err)
	}

	allRootFlags := []string{
		"config",
		"cwd",
		"models",
		"debug",
		"enable-user-agents-resources",
		"log-level",
		"verbose",
	}
	forbidden := map[string]bool{"config": true, "cwd": true, "models": true}

	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		t.Helper()
		var help bytes.Buffer
		root.SetOut(&help)
		root.SetErr(&help)
		if err := command.Help(); err != nil {
			t.Fatalf("%s help: %v", command.CommandPath(), err)
		}

		for _, name := range allRootFlags {
			inHelp := strings.Contains(help.String(), "--"+name)
			if forbidden[name] && inHelp {
				t.Errorf("%s advertises rejected flag --%s", command.CommandPath(), name)
			}
			if !forbidden[name] && !inHelp {
				t.Errorf("%s omits accepted flag --%s", command.CommandPath(), name)
			}
		}

		for _, child := range command.Commands() {
			if child.Hidden || child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			visit(child)
		}
	}
	visit(fleetCommand)

	for _, commandName := range []string{
		"send",
		"listen",
		"threads",
		"init",
		"bundle",
		"doctor",
	} {
		command, _, err := root.Find([]string{commandName})
		if err != nil {
			t.Errorf("find %s: %v", commandName, err)
			continue
		}
		var help bytes.Buffer
		root.SetOut(&help)
		root.SetErr(&help)
		if err := command.Help(); err != nil {
			t.Errorf("%s help: %v", command.CommandPath(), err)
			continue
		}
		for name := range forbidden {
			if !strings.Contains(help.String(), "--"+name) {
				t.Errorf("%s help missing supported flag --%s after Fleet help rendering", commandName, name)
			}
		}
	}
}

func TestFleetInstallRejectsUnstableOrMalformedAddressBeforeMutation(t *testing.T) {
	for _, args := range [][]string{
		{"fleet", "install", "--addr", "127.0.0.1"},
		{"fleet", "install", "--addr", "127.0.0.1:0"},
		{"fleet", "install", "--addr", "localhost:not-a-port"},
	} {
		root := newRootCmd()
		root.SetArgs(args)
		err := root.Execute()
		var usage *usageError
		if !errors.As(err, &usage) {
			t.Fatalf("args %v error = %T %v, want usage error", args, err, err)
		}
	}
}

func TestFleetInstallRejectsNonLoopbackWithoutEscapeHatch(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"fleet", "install", "--addr", "0.0.0.0:9000"})
	err := root.Execute()
	var usage *usageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "--unsafe-bind-any") {
		t.Fatalf("error = %T %v, want loopback usage error", err, err)
	}
}

func TestFleetServeRejectsNonLoopbackWithoutEscapeHatch(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"fleet", "serve", "--addr", "0.0.0.0:9000"})
	err := root.Execute()
	var usage *usageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "--unsafe-bind-any") {
		t.Fatalf("error = %T %v, want loopback usage error", err, err)
	}
}

func TestFleetServeRejectsMalformedAddressBeforeReconciliation(t *testing.T) {
	for _, args := range [][]string{
		{"fleet", "serve", "--addr", "127.0.0.1"},
		{"fleet", "serve", "--addr", "localhost:"},
		{"fleet", "serve", "--addr", "localhost:not-a-port"},
		{"fleet", "serve", "--addr", "127.0.0.1", "--unsafe-bind-any"},
	} {
		root := newRootCmd()
		root.SetArgs(args)
		err := root.Execute()
		var usage *usageError
		if !errors.As(err, &usage) || !strings.Contains(err.Error(), "host:port") {
			t.Fatalf("args %v error = %T %v, want host:port usage error", args, err, err)
		}
	}
}

func TestFleetAddressPrecedenceUsesFlagThenHomeConfigThenDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JUEX_HOME", home)

	cmd := newFleetServeCmd(nil)
	settings, err := resolveFleetServeSettings(cmd, config.DefaultFleetAddr, false)
	if err != nil || settings.Addr != config.DefaultFleetAddr || settings.UnsafeBindAny {
		t.Fatalf("default settings=%+v error=%v", settings, err)
	}

	if err := os.WriteFile(
		filepath.Join(home, "juex.yaml"),
		[]byte("fleet:\n  addr: 127.0.0.1:6843\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	settings, err = resolveFleetServeSettings(cmd, config.DefaultFleetAddr, false)
	if err != nil || settings.Addr != "127.0.0.1:6843" || settings.UnsafeBindAny {
		t.Fatalf("config settings=%+v error=%v", settings, err)
	}

	if err := cmd.Flags().Set("addr", "127.0.0.1:6844"); err != nil {
		t.Fatal(err)
	}
	settings, err = resolveFleetServeSettings(cmd, "127.0.0.1:6844", false)
	if err != nil || settings.Addr != "127.0.0.1:6844" || settings.UnsafeBindAny {
		t.Fatalf("flag settings=%+v error=%v", settings, err)
	}
}

func TestFleetServeReadsPersistentUnsafeBindForEachInvocation(t *testing.T) {
	t.Setenv("JUEX_HOME", t.TempDir())
	if _, err := config.SetHomeFleetSettings("0.0.0.0:6843", true); err != nil {
		t.Fatal(err)
	}
	settings, err := resolveFleetServeSettings(
		newFleetServeCmd(nil),
		config.DefaultFleetAddr,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Addr != "0.0.0.0:6843" || !settings.UnsafeBindAny {
		t.Fatalf("settings = %+v", settings)
	}

	if _, err := config.SetHomeFleetSettings("0.0.0.0:6843", false); err != nil {
		t.Fatal(err)
	}
	settings, err = resolveFleetServeSettings(
		newFleetServeCmd(nil),
		config.DefaultFleetAddr,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Addr != "0.0.0.0:6843" || settings.UnsafeBindAny {
		t.Fatalf("revoked settings = %+v", settings)
	}
}

func TestFleetInstallUsesCurrentDefaultWithoutPersisting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JUEX_HOME", home)
	fleetCfg, err := config.LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cmd := newFleetInstallCmd(nil)
	settings, err := resolveFleetInstallSettings(
		cmd,
		config.DefaultFleetAddr,
		false,
		fleetCfg,
	)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Addr != config.DefaultFleetAddr ||
		settings.UnsafeBindAny ||
		settings.ConfigPath != "" {
		t.Fatalf("settings = %+v", settings)
	}
	loaded, err := config.LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Addr != config.DefaultFleetAddr || loaded.AddrConfigured {
		t.Fatalf("home config = %+v", loaded)
	}
}

func TestFleetInstallRequiresExplicitUnsafeBindForNonLoopbackHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	if _, err := config.SetHomeFleetSettings("0.0.0.0:6843", false); err != nil {
		t.Fatal(err)
	}
	fleetCfg, err := config.LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cmd := newFleetInstallCmd(nil)
	if _, err := resolveFleetInstallSettings(
		cmd,
		config.DefaultFleetAddr,
		false,
		fleetCfg,
	); err == nil || !strings.Contains(err.Error(), "--unsafe-bind-any") {
		t.Fatalf("error = %v, want explicit unsafe-bind requirement", err)
	}
	if err := cmd.Flags().Set("unsafe-bind-any", "true"); err != nil {
		t.Fatal(err)
	}
	settings, err := resolveFleetInstallSettings(
		cmd,
		config.DefaultFleetAddr,
		true,
		fleetCfg,
	)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Addr != "0.0.0.0:6843" ||
		!settings.UnsafeBindAny ||
		settings.ConfigPath == "" {
		t.Fatalf("settings = %+v", settings)
	}
	loaded, err := config.LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.UnsafeBindAny {
		t.Fatalf("home config = %+v", loaded)
	}
}

func TestFleetInstallUsesPersistentUnsafeBindForNonLoopbackHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	if _, err := config.SetHomeFleetSettings("0.0.0.0:6843", true); err != nil {
		t.Fatal(err)
	}
	service := &fakeFleetServiceInstaller{
		registration: fleetservice.Registration{
			Platform:       fleetservice.PlatformLaunchd,
			Name:           "dev.juex.fleet",
			DefinitionPath: "/tmp/dev.juex.fleet.plist",
		},
	}
	managerCalls := 0
	cmd := newFleetInstallCmdWithDeps(fleetInstallCommandDeps{
		newServiceManager: func() (fleetServiceInstaller, error) {
			managerCalls++
			return service, nil
		},
		newAgentManager: func() (fleetAgentRestarter, error) {
			return &fakeFleetAgentRestarter{}, nil
		},
	})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.installCalls != 1 {
		t.Fatalf("install calls = %d, want 1", service.installCalls)
	}
	if managerCalls != 1 {
		t.Fatalf("service manager calls = %d, want 1", managerCalls)
	}
}

func TestFleetInstallExplicitAddressDoesNotInheritHomeUnsafeBind(t *testing.T) {
	t.Setenv("JUEX_HOME", t.TempDir())
	if _, err := config.SetHomeFleetSettings("0.0.0.0:6843", true); err != nil {
		t.Fatal(err)
	}
	fleetCfg, err := config.LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cmd := newFleetInstallCmd(nil)
	if err := cmd.Flags().Set("addr", "0.0.0.0:6844"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveFleetInstallSettings(
		cmd,
		"0.0.0.0:6844",
		false,
		fleetCfg,
	); err == nil || !strings.Contains(err.Error(), "--unsafe-bind-any") {
		t.Fatalf("error = %v, want fresh unsafe-bind confirmation", err)
	}
}

func TestFleetInstallExplicitFlagsOverrideExistingServiceOptions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	fleetCfg, err := config.LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	cmd := newFleetInstallCmd(nil)
	if err := cmd.Flags().Set("addr", "127.0.0.1:6844"); err != nil {
		t.Fatal(err)
	}
	settings, err := resolveFleetInstallSettings(
		cmd,
		"127.0.0.1:6844",
		false,
		fleetCfg,
	)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Addr != "127.0.0.1:6844" ||
		settings.UnsafeBindAny ||
		settings.ConfigPath == "" {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestFleetInstallReinstallsAfterReadingButIgnoringExistingDefinition(t *testing.T) {
	t.Setenv("JUEX_HOME", t.TempDir())
	service := &fakeFleetServiceInstaller{
		existing: fleetservice.InstalledServeOptions{
			Addr:          "0.0.0.0:8181",
			UnsafeBindAny: true,
		},
		existingFound: true,
		registration: fleetservice.Registration{
			Platform:       fleetservice.PlatformLaunchd,
			Name:           "dev.juex.fleet",
			DefinitionPath: "/tmp/dev.juex.fleet.plist",
		},
	}
	managerCalls := 0
	cmd := newFleetInstallCmdWithDeps(fleetInstallCommandDeps{
		newServiceManager: func() (fleetServiceInstaller, error) {
			managerCalls++
			return service, nil
		},
		newAgentManager: func() (fleetAgentRestarter, error) {
			return &fakeFleetAgentRestarter{}, nil
		},
	})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.existingCalls != 1 || service.installCalls != 1 {
		t.Fatalf(
			"existing reads = %d, install calls = %d, want 1/1",
			service.existingCalls,
			service.installCalls,
		)
	}
	if managerCalls != 1 {
		t.Fatalf("service manager calls = %d, want 1", managerCalls)
	}
}

func TestFleetInstallRejectsUnreadableExistingDefinitionBeforeInstall(t *testing.T) {
	t.Setenv("JUEX_HOME", t.TempDir())
	service := &fakeFleetServiceInstaller{
		existingErr: errors.New("malformed existing definition"),
	}
	cmd := newFleetInstallCmdWithDeps(fleetInstallCommandDeps{
		newServiceManager: func() (fleetServiceInstaller, error) {
			return service, nil
		},
		newAgentManager: func() (fleetAgentRestarter, error) {
			return &fakeFleetAgentRestarter{}, nil
		},
	})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "malformed existing definition") {
		t.Fatalf("error = %v, want existing-definition parse failure", err)
	}
	if service.existingCalls != 1 || service.installCalls != 0 {
		t.Fatalf(
			"existing reads = %d, install calls = %d, want 1/0",
			service.existingCalls,
			service.installCalls,
		)
	}
}

func TestFleetInstallExplicitAddressPersistsThroughCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	service := &fakeFleetServiceInstaller{
		registration: fleetservice.Registration{
			Platform:       fleetservice.PlatformLaunchd,
			Name:           "dev.juex.fleet",
			DefinitionPath: "/tmp/dev.juex.fleet.plist",
		},
	}
	cmd := newFleetInstallCmdWithDeps(fleetInstallCommandDeps{
		newServiceManager: func() (fleetServiceInstaller, error) {
			return service, nil
		},
		newAgentManager: func() (fleetAgentRestarter, error) {
			return &fakeFleetAgentRestarter{}, nil
		},
	})
	cmd.SetArgs([]string{"--addr", "127.0.0.1:6844"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Addr != "127.0.0.1:6844" || !loaded.AddrConfigured {
		t.Fatalf("home config = %+v", loaded)
	}
}

func TestFleetInstallRestartAgentsFlagIsOptIn(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		wantCalls int
	}{
		{name: "default", wantCalls: 0},
		{name: "explicit", args: []string{"--restart-agents"}, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("JUEX_HOME", t.TempDir())
			service := &fakeFleetServiceInstaller{
				registration: fleetservice.Registration{
					Platform:       fleetservice.PlatformLaunchd,
					Name:           "dev.juex.fleet",
					DefinitionPath: "/tmp/dev.juex.fleet.plist",
				},
			}
			agents := &fakeFleetAgentRestarter{
				result: fleet.RestartAgentsResult{
					Items: []fleet.RestartAgentResult{{
						Agent: fleet.AgentStatus{
							ID:            "aaaaaa",
							Name:          "alpha",
							RuntimeHealth: fleet.RuntimeHealthy,
						},
						Outcome: fleet.RestartAgentRestarted,
						Resume:  fleet.RestartResume{Required: true, Sent: true},
					}},
					Restarted: 1,
				},
			}
			cmd := newFleetInstallCmdWithDeps(fleetInstallCommandDeps{
				newServiceManager: func() (fleetServiceInstaller, error) {
					return service, nil
				},
				newAgentManager: func() (fleetAgentRestarter, error) {
					return agents, nil
				},
			})
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs(test.args)

			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if service.installCalls != 1 || agents.calls != test.wantCalls {
				t.Fatalf(
					"install calls = %d, agent refresh calls = %d, want 1/%d",
					service.installCalls,
					agents.calls,
					test.wantCalls,
				)
			}
			if test.wantCalls == 1 {
				for _, want := range []string{
					"Agent aaaaaa alpha: restarted runtime=healthy resume=sent",
					"Agent refresh: 1 restarted, 0 skipped, 0 failed.",
				} {
					if !strings.Contains(output.String(), want) {
						t.Fatalf("output missing %q:\n%s", want, output.String())
					}
				}
			}
		})
	}
}

func TestFleetInstallRestartAgentsRendersCompleteBatchBeforeReturningFailure(t *testing.T) {
	t.Setenv("JUEX_HOME", t.TempDir())
	service := &fakeFleetServiceInstaller{
		registration: fleetservice.Registration{
			Platform:       fleetservice.PlatformSystemd,
			Name:           "juex-fleet.service",
			DefinitionPath: "/tmp/juex-fleet.service",
		},
	}
	agents := &fakeFleetAgentRestarter{
		result: fleet.RestartAgentsResult{
			Items: []fleet.RestartAgentResult{
				{
					Agent: fleet.AgentStatus{
						ID:            "aaaaaa",
						Name:          "failed",
						RuntimeHealth: fleet.RuntimeHealthy,
					},
					Outcome: fleet.RestartAgentFailed,
					Reason:  "shutdown failed",
				},
				{
					Agent: fleet.AgentStatus{
						ID:            "bbbbbb",
						Name:          "continued",
						RuntimeHealth: fleet.RuntimeHealthy,
					},
					Outcome: fleet.RestartAgentRestarted,
					Resume: fleet.RestartResume{
						Error: "status route unavailable",
					},
				},
			},
			Restarted: 1,
			Failed:    1,
		},
		err: &fleet.RestartAgentsError{Failed: 1},
	}
	cmd := newFleetInstallCmdWithDeps(fleetInstallCommandDeps{
		newServiceManager: func() (fleetServiceInstaller, error) {
			return service, nil
		},
		newAgentManager: func() (fleetAgentRestarter, error) {
			return agents, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--restart-agents"})

	err := cmd.Execute()
	var aggregate *fleet.RestartAgentsError
	if !errors.As(err, &aggregate) {
		t.Fatalf("error = %T %v, want RestartAgentsError", err, err)
	}
	for _, want := range []string{
		"Agent aaaaaa failed: failed",
		"Agent bbbbbb continued: restarted",
		"resume=unknown",
		"Agent refresh: 1 restarted, 0 skipped, 1 failed.",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, output.String())
		}
	}
}

func TestFleetStatusRendersBinaryVersionAndWarnsOnSkew(t *testing.T) {
	previousVersion := version.Version
	version.Version = "2.0.0"
	t.Cleanup(func() { version.Version = previousVersion })

	root := newRootCmd()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	statuses := []fleet.AgentStatus{{
		ID:            "aaaaaa",
		Name:          "alpha",
		RuntimeHealth: fleet.RuntimeHealthy,
		BinaryVersion: "1.0.0",
		ProcessAlive:  true,
	}}
	renderFleetStatusTable(root, statuses)
	reportFleetVersionSkew(root, statuses)

	if !strings.Contains(stdout.String(), "VERSION") || !strings.Contains(stdout.String(), "1.0.0") {
		t.Fatalf("status table missing version:\n%s", stdout.String())
	}
	for _, want := range []string{"installed 2.0.0", "aaaaaa(1.0.0)", "not restarted automatically"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("skew warning missing %q:\n%s", want, stderr.String())
		}
	}
}

func writeFleetAgentFixture(t *testing.T, home, workspace, id, name string) string {
	t.Helper()
	agentDir := filepath.Join(home, "agents", id)
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	agent := agentstate.Agent{
		ID:        id,
		Name:      name,
		Workspace: workspace,
		Enabled:   true,
		Autostart: false,
		CreatedAt: time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".juex"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(map[string]string{"agent_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".juex", "juex.local.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	return agentDir
}
