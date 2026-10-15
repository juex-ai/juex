package agentstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveUsesCanonicalWorkspaceRegistryIdentity(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	first, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(Options{HomeDir: home, WorkDir: filepath.Join(workspace, ".")})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || second.Created || second.Agent.ID != first.Agent.ID {
		t.Fatalf("resolutions = first %+v second %+v", first, second)
	}

	byID, err := ResolveByID(Options{HomeDir: home}, first.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, err := canonicalPath(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if byID.Agent.Workspace != canonicalWorkspace || byID.Address.StateDir() != first.Address.StateDir() {
		t.Fatalf("ResolveByID() = %+v, want workspace %q and state %q", byID, canonicalWorkspace, first.Address.StateDir())
	}
	if got, want := byID.Address.ConfigPath(), filepath.Join(first.Address.StateDir(), "juex.yaml"); got != want {
		t.Fatalf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestResolveReusesWorkspaceIdentityThroughMovedDirectorySymlink(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	originalWorkspace := filepath.Join(root, "original")
	movedWorkspace := filepath.Join(root, "moved")
	if err := os.MkdirAll(originalWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}

	first, err := Resolve(Options{HomeDir: home, WorkDir: originalWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(originalWorkspace, movedWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(movedWorkspace, originalWorkspace); err != nil {
		t.Fatal(err)
	}

	second, err := Resolve(Options{HomeDir: home, WorkDir: movedWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Agent.ID != first.Agent.ID {
		t.Fatalf("resolutions = first %+v second %+v", first, second)
	}
	entries, err := ListRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("registry entries = %+v, want one", entries)
	}
}

func TestResolveCreatesIndependentIdentitiesForDifferentCheckouts(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	firstWorkspace := filepath.Join(root, "checkout-a")
	secondWorkspace := filepath.Join(root, "checkout-b")
	for _, path := range []string{firstWorkspace, secondWorkspace} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	first, err := Resolve(Options{HomeDir: home, WorkDir: firstWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(Options{HomeDir: home, WorkDir: secondWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	if first.Agent.ID == second.Agent.ID {
		t.Fatalf("different workspaces share agent id %q", first.Agent.ID)
	}
	entries, err := ListRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("registry entries = %+v, want two", entries)
	}
}

func TestResolveExistingUsesRegistryAndReportsMissingWorkspaceIdentity(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	created, err := Resolve(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := ResolveExisting(Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if existing.Agent.ID != created.Agent.ID || existing.Created {
		t.Fatalf("ResolveExisting() = %+v, created = %+v", existing, created)
	}

	_, err = ResolveExisting(Options{HomeDir: home, WorkDir: t.TempDir()})
	var missing *NoAgentError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want NoAgentError", err)
	}
}

func TestResolveRejectsDuplicateWorkspaceRegistrations(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	canonicalWorkspace, err := canonicalPath(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"aaaaaa", "bbbbbb"} {
		address, err := NewAgentAddress(home, id)
		if err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(address.StateDir(), agentFileName), Agent{
			ID: id, Name: id, Workspace: canonicalWorkspace, Enabled: true, CreatedAt: time.Now().UTC(),
		})
	}
	if _, err := ResolveExisting(Options{HomeDir: home, WorkDir: workspace}); err == nil || !strings.Contains(err.Error(), "multiple agents") {
		t.Fatalf("ResolveExisting() error = %v, want duplicate Workspace rejection", err)
	}
}
