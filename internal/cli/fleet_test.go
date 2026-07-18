package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/fleetservice"
	"github.com/juex-ai/juex/internal/version"
)

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

func TestFleetStatusPreservesOrthogonalState(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("JUEX_HOME", home)
	writeFleetAgentFixture(t, home, workspace, "aaaaaaaa", "alpha")

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
		{"--model", "openai:test", "fleet", "status"},
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
	agentDir := writeFleetAgentFixture(t, home, workspace, "aaaaaaaa", "alpha")
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

func TestFleetHelpAndSchemaExposeCommandsAndFlags(t *testing.T) {
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

	root = newRootCmd()
	output.Reset()
	root.SetOut(&output)
	root.SetArgs([]string{"schema"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"name": "fleet"`,
		`"name": "addr"`,
		`"name": "unsafe-bind-any"`,
		`"name": "lines"`,
		`"name": "yes"`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("schema missing %q", want)
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
	t.Setenv("JUEX_HOME", home)

	cmd := newFleetServeCmd(nil)
	addr, explicit, err := resolveFleetAddr(cmd, config.DefaultFleetAddr, false)
	if err != nil || explicit || addr != config.DefaultFleetAddr {
		t.Fatalf("default addr=%q explicit=%t error=%v", addr, explicit, err)
	}

	if err := os.WriteFile(
		filepath.Join(home, "juex.yaml"),
		[]byte("fleet:\n  addr: 127.0.0.1:6843\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	addr, explicit, err = resolveFleetAddr(cmd, config.DefaultFleetAddr, false)
	if err != nil || explicit || addr != "127.0.0.1:6843" {
		t.Fatalf("config addr=%q explicit=%t error=%v", addr, explicit, err)
	}

	if err := cmd.Flags().Set("addr", "127.0.0.1:6844"); err != nil {
		t.Fatal(err)
	}
	addr, explicit, err = resolveFleetAddr(cmd, "127.0.0.1:6844", false)
	if err != nil || !explicit || addr != "127.0.0.1:6844" {
		t.Fatalf("flag addr=%q explicit=%t error=%v", addr, explicit, err)
	}
}

func TestFleetInstallMigratesLegacyCustomServiceOptions(t *testing.T) {
	home := t.TempDir()
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
		fleetservice.InstalledServeOptions{
			Addr:          "0.0.0.0:8181",
			UnsafeBindAny: true,
		},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Addr != "0.0.0.0:8181" ||
		!settings.UnsafeBindAny ||
		!settings.MigratedLegacyAddr ||
		!settings.PreservedLegacyBind ||
		settings.ConfigPath == "" {
		t.Fatalf("settings = %+v", settings)
	}
	loaded, err := config.LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Addr != "0.0.0.0:8181" || !loaded.AddrConfigured {
		t.Fatalf("migrated config = %+v", loaded)
	}
}

func TestFleetInstallMovesLegacyDefaultButPreservesHomeConfig(t *testing.T) {
	t.Run("legacy default adopts new default", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("JUEX_HOME", home)
		fleetCfg, err := config.LoadHomeFleetConfig()
		if err != nil {
			t.Fatal(err)
		}
		settings, err := resolveFleetInstallSettings(
			newFleetInstallCmd(nil),
			config.DefaultFleetAddr,
			false,
			fleetCfg,
			fleetservice.InstalledServeOptions{Addr: config.LegacyDefaultFleetAddr},
			true,
		)
		if err != nil {
			t.Fatal(err)
		}
		if settings.Addr != config.DefaultFleetAddr ||
			settings.MigratedLegacyAddr ||
			settings.ConfigPath != "" {
			t.Fatalf("settings = %+v", settings)
		}
	})

	t.Run("explicit home config wins and unsafe is retained", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("JUEX_HOME", home)
		if _, err := config.SetHomeFleetAddr("0.0.0.0:6843"); err != nil {
			t.Fatal(err)
		}
		fleetCfg, err := config.LoadHomeFleetConfig()
		if err != nil {
			t.Fatal(err)
		}
		settings, err := resolveFleetInstallSettings(
			newFleetInstallCmd(nil),
			config.DefaultFleetAddr,
			false,
			fleetCfg,
			fleetservice.InstalledServeOptions{
				Addr:          "0.0.0.0:8181",
				UnsafeBindAny: true,
			},
			true,
		)
		if err != nil {
			t.Fatal(err)
		}
		if settings.Addr != "0.0.0.0:6843" ||
			!settings.UnsafeBindAny ||
			settings.MigratedLegacyAddr ||
			!settings.PreservedLegacyBind ||
			settings.ConfigPath != "" {
			t.Fatalf("settings = %+v", settings)
		}
	})
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
		fleetservice.InstalledServeOptions{
			Addr:          "0.0.0.0:8181",
			UnsafeBindAny: true,
		},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Addr != "127.0.0.1:6844" ||
		settings.UnsafeBindAny ||
		settings.MigratedLegacyAddr ||
		settings.PreservedLegacyBind ||
		settings.ConfigPath == "" {
		t.Fatalf("settings = %+v", settings)
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
		ID:            "aaaaaaaa",
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
	for _, want := range []string{"installed 2.0.0", "aaaaaaaa(1.0.0)", "not restarted automatically"} {
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
