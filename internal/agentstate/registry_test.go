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
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	validID := "bbbbbbbb"
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(agentsDir, validID, agentFileName), Agent{
		ID:        validID,
		Name:      "valid",
		Workspace: workspace,
		Enabled:   true,
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
	})

	malformedID := "cccccccc"
	writeText(t, filepath.Join(agentsDir, malformedID, agentFileName), "{")

	invalidID := "not-an-agent-id"
	if err := os.MkdirAll(filepath.Join(agentsDir, invalidID), 0o755); err != nil {
		t.Fatal(err)
	}

	symlinkID := "aaaaaaaa"
	if err := os.Symlink(filepath.Join(agentsDir, validID), filepath.Join(agentsDir, symlinkID)); err != nil {
		t.Fatal(err)
	}

	entries, err := ListRegistry(home)
	if err != nil {
		t.Fatalf("ListRegistry() error = %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("ListRegistry() returned %d entries, want 4: %+v", len(entries), entries)
	}
	canonicalHome, err := canonicalPath(home)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{symlinkID, validID, malformedID, invalidID}
	for i, want := range wantIDs {
		if entries[i].ID != want {
			t.Fatalf("entries[%d].ID = %q, want %q", i, entries[i].ID, want)
		}
		if entries[i].Address.ID() != "" &&
			entries[i].Address.StateDir() != filepath.Join(canonicalHome, "agents", want) {
			t.Fatalf("entries[%d] state dir = %q, want registry path", i, entries[i].Address.StateDir())
		}
	}
	if entries[1].Problem != "" || entries[1].Agent.ID != validID {
		t.Fatalf("valid entry = %+v", entries[1])
	}
	for _, entry := range []RegistryEntry{entries[0], entries[2], entries[3]} {
		if strings.TrimSpace(entry.Problem) == "" {
			t.Fatalf("problem entry hidden as valid: %+v", entry)
		}
	}
}

func TestListRegistryReturnsEmptyWhenAgentsDirectoryIsMissing(t *testing.T) {
	entries, err := ListRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("ListRegistry() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ListRegistry() = %+v, want empty", entries)
	}
}

func TestListRegistrySkipsPrivateDeletingTombstones(t *testing.T) {
	home := t.TempDir()
	agentsDir := filepath.Join(home, "agents")
	writeRegistryAgent(t, home, "aaaaaaaa", filepath.Join(home, "workspace"))
	tombstone := filepath.Join(agentsDir, ".bbbbbbbb.deleting-cccccccc")
	if err := os.MkdirAll(tombstone, 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := ListRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "aaaaaaaa" {
		t.Fatalf("registry entries = %+v, want only real agent", entries)
	}
}

func TestListRegistryValidatesAgentMetadata(t *testing.T) {
	home := t.TempDir()
	agentsDir := filepath.Join(home, "agents")
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	writeJSON(t, filepath.Join(agentsDir, "aaaaaaaa", agentFileName), Agent{
		ID: "bbbbbbbb", Workspace: workspace, CreatedAt: createdAt,
	})
	writeJSON(t, filepath.Join(agentsDir, "bbbbbbbb", agentFileName), Agent{
		ID: "bbbbbbbb", Workspace: "relative/workspace", CreatedAt: createdAt,
	})
	writeJSON(t, filepath.Join(agentsDir, "cccccccc", agentFileName), Agent{
		ID: "cccccccc", Workspace: workspace,
	})
	if err := os.MkdirAll(filepath.Join(agentsDir, "dddddddd"), 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := ListRegistry(home)
	if err != nil {
		t.Fatalf("ListRegistry() error = %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("ListRegistry() returned %d entries, want 4: %+v", len(entries), entries)
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Problem) == "" {
			t.Fatalf("invalid metadata accepted for %q: %+v", entry.ID, entry.Agent)
		}
	}
}

func TestInspectBindingClassifiesWorkspaceBindings(t *testing.T) {
	root := t.TempDir()
	const agentID = "aaaaaaaa"
	const otherID = "bbbbbbbb"

	tests := []struct {
		name     string
		prepare  func(t *testing.T, workspace string)
		entry    func(workspace string) RegistryEntry
		wantKind BindingKind
	}{
		{
			name:     "missing workspace is orphaned",
			prepare:  func(*testing.T, string) {},
			entry:    func(workspace string) RegistryEntry { return validRegistryEntry(agentID, workspace) },
			wantKind: WorkspaceOrphaned,
		},
		{
			name: "matching marker is bound",
			prepare: func(t *testing.T, workspace string) {
				writeJSON(t, filepath.Join(workspace, ".juex", markerName), Marker{AgentID: agentID})
			},
			entry:    func(workspace string) RegistryEntry { return validRegistryEntry(agentID, workspace) },
			wantKind: WorkspaceBound,
		},
		{
			name: "marker for another valid agent is orphaned",
			prepare: func(t *testing.T, workspace string) {
				writeJSON(t, filepath.Join(workspace, ".juex", markerName), Marker{AgentID: otherID})
			},
			entry:    func(workspace string) RegistryEntry { return validRegistryEntry(agentID, workspace) },
			wantKind: WorkspaceOrphaned,
		},
		{
			name: "missing marker is invalid",
			prepare: func(t *testing.T, workspace string) {
				if err := os.MkdirAll(workspace, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			entry:    func(workspace string) RegistryEntry { return validRegistryEntry(agentID, workspace) },
			wantKind: WorkspaceInvalid,
		},
		{
			name: "corrupt marker is invalid",
			prepare: func(t *testing.T, workspace string) {
				writeText(t, filepath.Join(workspace, ".juex", markerName), "{")
			},
			entry:    func(workspace string) RegistryEntry { return validRegistryEntry(agentID, workspace) },
			wantKind: WorkspaceInvalid,
		},
		{
			name: "unreadable marker is invalid",
			prepare: func(t *testing.T, workspace string) {
				if err := os.MkdirAll(filepath.Join(workspace, ".juex", markerName), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			entry:    func(workspace string) RegistryEntry { return validRegistryEntry(agentID, workspace) },
			wantKind: WorkspaceInvalid,
		},
		{
			name: "invalid registry entry remains invalid",
			prepare: func(t *testing.T, workspace string) {
				if err := os.MkdirAll(workspace, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			entry: func(workspace string) RegistryEntry {
				entry := validRegistryEntry(agentID, workspace)
				entry.Problem = "malformed agent.json"
				return entry
			},
			wantKind: WorkspaceInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-"))
			tt.prepare(t, workspace)
			got := InspectBinding(tt.entry(workspace))
			if got.Kind != tt.wantKind {
				t.Fatalf("InspectBinding() = %+v, want kind %q", got, tt.wantKind)
			}
			if strings.TrimSpace(got.Reason) == "" {
				t.Fatalf("InspectBinding() = %+v, want a reason", got)
			}
		})
	}
}

func validRegistryEntry(id, workspace string) RegistryEntry {
	address, err := NewAgentAddress(filepath.Dir(workspace), id)
	if err != nil {
		panic(err)
	}
	return RegistryEntry{
		ID:      id,
		Address: address,
		Agent: Agent{
			ID:        id,
			Name:      "agent",
			Workspace: workspace,
			Enabled:   true,
			CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		},
	}
}

func TestDeleteOrphanRejectsBoundInvalidAndSymlinkEntries(t *testing.T) {
	home := t.TempDir()
	agentsDir := filepath.Join(home, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	boundID := "aaaaaaaa"
	boundWorkspace := filepath.Join(home, "bound-workspace")
	writeJSON(t, filepath.Join(boundWorkspace, ".juex", markerName), Marker{AgentID: boundID})
	boundDir := writeRegistryAgent(t, home, boundID, boundWorkspace)

	invalidID := "bbbbbbbb"
	invalidWorkspace := filepath.Join(home, "invalid-workspace")
	if err := os.MkdirAll(invalidWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	invalidDir := writeRegistryAgent(t, home, invalidID, invalidWorkspace)

	symlinkID := "cccccccc"
	symlinkTarget := filepath.Join(home, "outside-agent")
	if err := os.MkdirAll(symlinkTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkDir := filepath.Join(agentsDir, symlinkID)
	if err := os.Symlink(symlinkTarget, symlinkDir); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{boundID, invalidID, symlinkID, "../../outside"} {
		if err := DeleteOrphan(home, id); err == nil {
			t.Fatalf("DeleteOrphan(%q) succeeded, want rejection", id)
		}
	}
	for _, path := range []string{boundDir, invalidDir, symlinkDir, symlinkTarget} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("rejected deletion changed %s: %v", path, err)
		}
	}
}

func TestDeleteOrphanDeletesOnlySelectedDefiniteOrphan(t *testing.T) {
	home := t.TempDir()
	selectedID := "aaaaaaaa"
	selectedDir := writeRegistryAgent(t, home, selectedID, filepath.Join(home, "missing-selected"))
	otherID := "bbbbbbbb"
	otherDir := writeRegistryAgent(t, home, otherID, filepath.Join(home, "missing-other"))

	if err := DeleteOrphan(home, selectedID); err != nil {
		t.Fatalf("DeleteOrphan(%q) error = %v", selectedID, err)
	}
	if _, err := os.Lstat(selectedDir); !os.IsNotExist(err) {
		t.Fatalf("selected directory still exists or cannot be checked: %v", err)
	}
	if _, err := os.Lstat(otherDir); err != nil {
		t.Fatalf("unselected orphan was changed: %v", err)
	}

	entries, err := ListRegistry(home)
	if err != nil {
		t.Fatalf("ListRegistry() error = %v", err)
	}
	if len(entries) != 1 || entries[0].ID != otherID {
		t.Fatalf("registry after selected deletion = %+v, want only %q", entries, otherID)
	}
}

func TestUpdateAgentAppliesOnlyDeclaredMetadata(t *testing.T) {
	home, workspace := prepareResolveTest(t)
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	enabled := false
	autostart := true

	updated, err := UpdateAgent(home, resolved.Agent.ID, AgentUpdate{
		Name:      &name,
		Enabled:   &enabled,
		Autostart: &autostart,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.Enabled || !updated.Autostart {
		t.Fatalf("updated agent = %+v", updated)
	}
	if updated.ID != resolved.Agent.ID ||
		updated.Workspace != resolved.Agent.Workspace ||
		!updated.CreatedAt.Equal(resolved.Agent.CreatedAt) {
		t.Fatalf("immutable metadata changed: before=%+v after=%+v", resolved.Agent, updated)
	}

	reloaded, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Agent != updated {
		t.Fatalf("persisted agent = %+v, want %+v", reloaded.Agent, updated)
	}
}

func TestUpdateAgentRejectsEmptyNameWithoutMutation(t *testing.T) {
	home, workspace := prepareResolveTest(t)
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	empty := "  "
	if _, err := UpdateAgent(home, resolved.Agent.ID, AgentUpdate{Name: &empty}); err == nil {
		t.Fatal("UpdateAgent accepted an empty name")
	}
	reloaded, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Agent != resolved.Agent {
		t.Fatalf("rejected update changed agent: before=%+v after=%+v", resolved.Agent, reloaded.Agent)
	}
}

func TestWorkspaceHasMarkerReportsAnyRegularMarker(t *testing.T) {
	workspace := t.TempDir()
	hasMarker, err := WorkspaceHasMarker(workspace)
	if err != nil || hasMarker {
		t.Fatalf("missing marker = %t, %v", hasMarker, err)
	}
	writeJSON(t, filepath.Join(workspace, ".juex", markerName), Marker{AgentID: "zzzzzzzz"})
	hasMarker, err = WorkspaceHasMarker(workspace)
	if err != nil || !hasMarker {
		t.Fatalf("unknown regular marker = %t, %v", hasMarker, err)
	}
}

func TestDeleteRegisteredRemovesStateAndMatchingMarker(t *testing.T) {
	home, workspace := prepareResolveTest(t)
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(workspace, "README.md")
	if err := os.WriteFile(userFile, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := DeleteRegistered(home, resolved.Agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(resolved.Address.StateDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agent directory still exists: %v", err)
	}
	if _, err := os.Lstat(resolved.MarkerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("matching marker still exists: %v", err)
	}
	if body, err := os.ReadFile(userFile); err != nil || string(body) != "keep\n" {
		t.Fatalf("workspace user file = %q, %v", body, err)
	}

	recreated, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if !recreated.Created || recreated.Agent.ID == resolved.Agent.ID {
		t.Fatalf("recreated identity = %+v, removed = %q", recreated, resolved.Agent.ID)
	}
}

func TestDeleteRegisteredPreservesMarkerForAnotherIdentity(t *testing.T) {
	home, workspace := prepareResolveTest(t)
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	const otherID = "bbbbbbbb"
	writeJSON(t, resolved.MarkerPath, Marker{AgentID: otherID})

	if err := DeleteRegistered(home, resolved.Agent.ID); err != nil {
		t.Fatal(err)
	}
	var marker Marker
	readJSONTest(t, resolved.MarkerPath, &marker)
	if marker.AgentID != otherID {
		t.Fatalf("marker = %+v, want preserved other identity", marker)
	}
}

func TestDeleteRegisteredRejectsCorruptOrSymlinkedWorkspaceWithoutMutation(t *testing.T) {
	t.Run("corrupt marker", func(t *testing.T) {
		home, workspace := prepareResolveTest(t)
		resolved, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
		if err != nil {
			t.Fatal(err)
		}
		writeText(t, resolved.MarkerPath, "{")
		if err := DeleteRegistered(home, resolved.Agent.ID); err == nil {
			t.Fatal("DeleteRegistered accepted a corrupt marker")
		}
		assertDir(t, resolved.Address.StateDir())
		assertFile(t, resolved.MarkerPath)
	})

	t.Run("workspace symlink", func(t *testing.T) {
		home, workspace := prepareResolveTest(t)
		resolved, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
		if err != nil {
			t.Fatal(err)
		}
		target := workspace + "-target"
		if err := os.Rename(workspace, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, workspace); err != nil {
			t.Fatal(err)
		}
		if err := DeleteRegistered(home, resolved.Agent.ID); err == nil {
			t.Fatal("DeleteRegistered followed a workspace symlink")
		}
		assertDir(t, resolved.Address.StateDir())
		assertFile(t, filepath.Join(target, ".juex", markerName))
	})
}

func TestDeleteRegisteredRestoresRegistryWhenMarkerRemovalFails(t *testing.T) {
	home, workspace := prepareResolveTest(t)
	resolved, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	originalRemove := removeWorkspaceMarker
	removeWorkspaceMarker = func(string) error { return errors.New("injected marker removal failure") }
	t.Cleanup(func() { removeWorkspaceMarker = originalRemove })

	err = DeleteRegistered(home, resolved.Agent.ID)
	if err == nil || !strings.Contains(err.Error(), "injected marker removal failure") {
		t.Fatalf("DeleteRegistered error = %v", err)
	}
	assertDir(t, resolved.Address.StateDir())
	assertFile(t, resolved.MarkerPath)
	entries, listErr := ListRegistry(home)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 1 || entries[0].ID != resolved.Agent.ID {
		t.Fatalf("restored registry = %+v", entries)
	}
}

func writeRegistryAgent(t *testing.T, home, id, workspace string) string {
	t.Helper()
	agentDir := filepath.Join(home, "agents", id)
	writeJSON(t, filepath.Join(agentDir, agentFileName), Agent{
		ID:        id,
		Name:      id,
		Workspace: workspace,
		Enabled:   true,
		CreatedAt: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
	})
	return agentDir
}
