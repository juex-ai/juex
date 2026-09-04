package agentstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListRegistryReturnsSortedEntriesAndKeepsProblemsVisible(t *testing.T) {
	home := t.TempDir()
	agentsDir := filepath.Join(home, "agents")
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRegistryAgent(t, home, "bbbbbb", workspace)
	writeText(t, filepath.Join(agentsDir, "cccccc", agentFileName), "{")
	if err := os.MkdirAll(filepath.Join(agentsDir, "not-an-agent-id"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(agentsDir, "bbbbbb"), filepath.Join(agentsDir, "aaaaaa")); err != nil {
		t.Fatal(err)
	}

	entries, err := ListRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %+v", entries)
	}
	wantIDs := []string{"aaaaaa", "bbbbbb", "cccccc", "not-an-agent-id"}
	for i, want := range wantIDs {
		if entries[i].ID != want {
			t.Fatalf("entries[%d].ID = %q, want %q", i, entries[i].ID, want)
		}
	}
	if entries[1].Problem != "" {
		t.Fatalf("valid entry = %+v", entries[1])
	}
	for _, index := range []int{0, 2, 3} {
		if strings.TrimSpace(entries[index].Problem) == "" {
			t.Fatalf("invalid entry hidden: %+v", entries[index])
		}
	}
}

func TestListRegistrySkipsPrivateLifecycleDirectories(t *testing.T) {
	home := t.TempDir()
	writeRegistryAgent(t, home, "aaaaaa", t.TempDir())
	for _, name := range []string{".bbbbbb.deleting-cccccc", ".dddddd.creating-123456"} {
		if err := os.MkdirAll(filepath.Join(home, "agents", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ListRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "aaaaaa" {
		t.Fatalf("entries = %+v, want only real Agent", entries)
	}
}

func TestInspectBindingUsesAgentWorkspaceState(t *testing.T) {
	home := t.TempDir()
	boundWorkspace := t.TempDir()
	bound := writeRegistryAgent(t, home, "aaaaaa", boundWorkspace)
	if binding := InspectBinding(bound); binding.Kind != WorkspaceBound {
		t.Fatalf("bound = %+v", binding)
	}
	orphan := writeRegistryAgent(t, home, "bbbbbb", filepath.Join(home, "missing"))
	if binding := InspectBinding(orphan); binding.Kind != WorkspaceOrphaned {
		t.Fatalf("orphan = %+v", binding)
	}
	invalid := bound
	invalid.Problem = "broken metadata"
	if binding := InspectBinding(invalid); binding.Kind != WorkspaceInvalid {
		t.Fatalf("invalid = %+v", binding)
	}
}

func TestUpdateAgentAppliesOnlyDeclaredMetadata(t *testing.T) {
	home := t.TempDir()
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	enabled := false
	autostart := true
	updated, err := UpdateAgent(home, resolved.Agent.ID, AgentUpdate{Name: &name, Enabled: &enabled, Autostart: &autostart})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.Enabled || !updated.Autostart || updated.Workspace != resolved.Agent.Workspace || !updated.CreatedAt.Equal(resolved.Agent.CreatedAt) {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestUpdateAgentRejectsEmptyNameWithoutMutation(t *testing.T) {
	home := t.TempDir()
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	empty := "  "
	if _, err := UpdateAgent(home, resolved.Agent.ID, AgentUpdate{Name: &empty}); err == nil {
		t.Fatal("empty name accepted")
	}
	current, err := ResolveByID(Options{HomeDir: home}, resolved.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Agent.Name != resolved.Agent.Name {
		t.Fatalf("Agent mutated: %+v", current.Agent)
	}
}

func TestDeleteOrphanDeletesOnlySelectedOrphan(t *testing.T) {
	home := t.TempDir()
	orphan := writeRegistryAgent(t, home, "aaaaaa", filepath.Join(home, "missing"))
	bound := writeRegistryAgent(t, home, "bbbbbb", t.TempDir())
	if err := DeleteOrphan(home, orphan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan.Address.StateDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan remains: %v", err)
	}
	if _, err := os.Stat(bound.Address.StateDir()); err != nil {
		t.Fatalf("bound Agent changed: %v", err)
	}
	if err := DeleteOrphan(home, bound.ID); err == nil {
		t.Fatal("bound Agent deleted as orphan")
	}
}

func TestDeleteRegisteredRemovesAgentStateAndKeepsWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	projectFile := filepath.Join(workspace, "project.txt")
	writeText(t, projectFile, "keep\n")
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteRegistered(home, resolved.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(resolved.Address.StateDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Agent state remains: %v", err)
	}
	assertText(t, projectFile, "keep\n")
}

func writeRegistryAgent(t *testing.T, home, id, workspace string) RegistryEntry {
	t.Helper()
	address, err := NewAgentAddress(home, id)
	if err != nil {
		t.Fatal(err)
	}
	agent := Agent{
		ID: id, Name: id, Workspace: filepath.Clean(workspace), Enabled: true,
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
	}
	writeJSON(t, filepath.Join(address.StateDir(), agentFileName), agent)
	return RegistryEntry{ID: id, Address: address, Agent: agent}
}
