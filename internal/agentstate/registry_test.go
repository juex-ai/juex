package agentstate

import (
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
	wantIDs := []string{symlinkID, validID, malformedID, invalidID}
	for i, want := range wantIDs {
		if entries[i].ID != want {
			t.Fatalf("entries[%d].ID = %q, want %q", i, entries[i].ID, want)
		}
		if entries[i].Dir != filepath.Join(agentsDir, want) {
			t.Fatalf("entries[%d].Dir = %q, want registry path", i, entries[i].Dir)
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
	return RegistryEntry{
		ID:  id,
		Dir: filepath.Join(filepath.Dir(workspace), "agents", id),
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
