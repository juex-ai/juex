package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/environment"
)

func TestLoadAgentConfigAfterWorkspaceWithInheritedImportScope(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	writeTextFile(t, filepath.Join(workspace, ".juex", "juex.yaml"), "enable_user_agents_resources: true\n")
	resolved, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	importPath := filepath.Join(resolved.Address.StateDir(), "agent-import.yaml")
	writeTextFile(t, importPath, "enable_user_agents_resources: false\nenvironment:\n  variables:\n    AGENT_IMPORTED: agent\n")
	writeTextFile(t, resolved.Address.ConfigPath(), "imports:\n  - source: agent-import.yaml\n")

	cfg, err := LoadWithOptions(LoadOptions{
		HomeDir:    home,
		WorkDir:    workspace,
		AgentState: AgentStateExisting,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableUserAgentsResources {
		t.Fatal("Agent config import did not override workspace config")
	}
	if cfg.AgentID != resolved.Agent.ID || cfg.AgentConfigPath() != resolved.Address.ConfigPath() {
		t.Fatalf("loaded Agent = %q path = %q", cfg.AgentID, cfg.AgentConfigPath())
	}
	metadata := cfg.EnvironmentSnapshot().ConfiguredMetadata()
	if len(metadata) != 1 || metadata[0].Key != "AGENT_IMPORTED" || metadata[0].Source != environment.SourceAgentConfig || metadata[0].Path != importPath {
		t.Fatalf("Agent import environment metadata = %+v", metadata)
	}
}

func TestAgentConfigRejectsFleetAndWriteLeavesWorkspaceUnchanged(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	workspacePath := filepath.Join(workspace, ".juex", "juex.yaml")
	workspaceContent := []byte("enable_user_agents_resources: true\n")
	writeTextFile(t, workspacePath, string(workspaceContent))
	resolved, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ValidateAgentConfig([]byte("fleet:\n  addr: 127.0.0.1:7337\n"), home, resolved.Agent.ID); err == nil || !strings.Contains(err.Error(), "fleet") {
		t.Fatalf("ValidateAgentConfig() error = %v, want fleet rejection", err)
	}
	fleetImport := filepath.Join(resolved.Address.StateDir(), "fleet.yaml")
	writeTextFile(t, fleetImport, "fleet:\n  addr: 127.0.0.1:7337\n")
	if _, err := ValidateAgentConfig([]byte("imports:\n  - source: fleet.yaml\n"), home, resolved.Agent.ID); err == nil || !strings.Contains(err.Error(), "fleet") {
		t.Fatalf("ValidateAgentConfig() imported error = %v, want Agent-scope fleet rejection", err)
	}
	oldAgent := []byte("enable_user_agents_resources: true\n")
	writeTextFile(t, resolved.Address.ConfigPath(), string(oldAgent))
	if _, err := WriteAgentConfig([]byte("unknown_field: true\n"), home, resolved.Agent.ID); err == nil {
		t.Fatal("WriteAgentConfig() accepted an invalid field")
	}
	unchangedAgent, err := os.ReadFile(resolved.Address.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(unchangedAgent) != string(oldAgent) {
		t.Fatalf("invalid update changed Agent config: %q", unchangedAgent)
	}
	written, err := WriteAgentConfig([]byte("enable_user_agents_resources: false\n"), home, resolved.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if written != resolved.Address.ConfigPath() {
		t.Fatalf("WriteAgentConfig() = %q, want %q", written, resolved.Address.ConfigPath())
	}
	gotWorkspace, err := os.ReadFile(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotWorkspace) != string(workspaceContent) {
		t.Fatalf("workspace config changed: %q", gotWorkspace)
	}
	gotAgent, err := os.ReadFile(written)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotAgent) != "enable_user_agents_resources: false\n" {
		t.Fatalf("agent config = %q", gotAgent)
	}
}

func TestAgentConfigsRemainIsolatedAcrossWorkspaces(t *testing.T) {
	home := t.TempDir()
	workspaces := []string{t.TempDir(), t.TempDir()}
	wantTimeouts := []time.Duration{4 * time.Second, 9 * time.Second}
	resolutions := make([]agentstate.Resolution, 0, len(workspaces))
	for index, workspace := range workspaces {
		resolved, err := agentstate.Resolve(agentstate.Options{HomeDir: home, WorkDir: workspace})
		if err != nil {
			t.Fatal(err)
		}
		resolutions = append(resolutions, resolved)
		writeTextFile(t, resolved.Address.ConfigPath(), "runtime:\n  tool_timeout: "+wantTimeouts[index].String()+"\n")
	}
	for index, resolved := range resolutions {
		cfg, err := LoadWithOptions(LoadOptions{HomeDir: home, AgentID: resolved.Agent.ID, AgentState: AgentStateExisting})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.WorkDir != resolved.Agent.Workspace || cfg.ToolTimeout != wantTimeouts[index] {
			t.Fatalf("Agent %s loaded Workspace %q timeout %s, want %q and %s", resolved.Agent.ID, cfg.WorkDir, cfg.ToolTimeout, resolved.Agent.Workspace, wantTimeouts[index])
		}
	}
}

func TestLoadConfigPriorityEndsWithAgentThenExplicitOverride(t *testing.T) {
	root := t.TempDir()
	userHome := filepath.Join(root, "user")
	effectiveHome := filepath.Join(root, "instance")
	workspace := filepath.Join(root, "workspace")
	for _, path := range []string{userHome, workspace} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), "runtime:\n  tool_timeout: 1s\n")
	writeTextFile(t, filepath.Join(effectiveHome, "juex.yaml"), "runtime:\n  tool_timeout: 2s\n")
	writeTextFile(t, filepath.Join(workspace, ".juex", "juex.yaml"), "runtime:\n  tool_timeout: 3s\n")
	resolved, err := agentstate.Resolve(agentstate.Options{HomeDir: effectiveHome, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	writeTextFile(t, resolved.Address.ConfigPath(), "runtime:\n  tool_timeout: 4s\n")
	explicitPath := filepath.Join(root, "explicit.yaml")
	writeTextFile(t, explicitPath, "runtime:\n  tool_timeout: 5s\n")

	agentConfig, err := LoadWithOptions(LoadOptions{
		HomeDir:    effectiveHome,
		AgentID:    resolved.Agent.ID,
		AgentState: AgentStateExisting,
	})
	if err != nil {
		t.Fatal(err)
	}
	if agentConfig.ToolTimeout != 4*time.Second {
		t.Fatalf("Agent config priority = %s, want 4s", agentConfig.ToolTimeout)
	}
	explicitConfig, err := LoadWithOptions(LoadOptions{
		HomeDir:    effectiveHome,
		AgentID:    resolved.Agent.ID,
		ConfigPath: explicitPath,
		AgentState: AgentStateExisting,
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicitConfig.ToolTimeout != 5*time.Second || explicitConfig.ExplicitConfigPath() != explicitPath {
		t.Fatalf("explicit config priority = %s path = %q, want 5s and %q", explicitConfig.ToolTimeout, explicitConfig.ExplicitConfigPath(), explicitPath)
	}
}

func TestExplicitWorkspaceConfigOverridesAgentLayer(t *testing.T) {
	root := t.TempDir()
	userHome := filepath.Join(root, "user")
	effectiveHome := filepath.Join(root, "instance")
	workspace := filepath.Join(root, "workspace")
	for _, path := range []string{userHome, workspace} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	workspacePath := filepath.Join(workspace, ".juex", "juex.yaml")
	writeTextFile(t, workspacePath, "runtime:\n  tool_timeout: 3s\n")
	resolved, err := agentstate.Resolve(agentstate.Options{HomeDir: effectiveHome, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	writeTextFile(t, resolved.Address.ConfigPath(), "runtime:\n  tool_timeout: 4s\n")

	cfg, err := LoadWithOptions(LoadOptions{
		HomeDir:    effectiveHome,
		AgentID:    resolved.Agent.ID,
		ConfigPath: workspacePath,
		AgentState: AgentStateExisting,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolTimeout != 3*time.Second {
		t.Fatalf("explicit Workspace config priority = %s, want 3s", cfg.ToolTimeout)
	}
	if cfg.ExplicitConfigPath() != workspacePath {
		t.Fatalf("explicit config path = %q, want %q", cfg.ExplicitConfigPath(), workspacePath)
	}
}
