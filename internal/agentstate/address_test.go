package agentstate

import (
	"path/filepath"
	"testing"
)

func TestNewAgentAddressOwnsResidentLayout(t *testing.T) {
	home := t.TempDir()
	address, err := NewAgentAddress(home, "abc234")
	if err != nil {
		t.Fatal(err)
	}
	if address.ID() != "abc234" {
		t.Fatalf("ID() = %q", address.ID())
	}
	canonicalHome, err := canonicalPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if address.StateDir() != filepath.Join(canonicalHome, "agents", "abc234") {
		t.Fatalf("StateDir() = %q", address.StateDir())
	}
	wantLock := filepath.Join(canonicalHome, ".locks", "endpoints", "abc234.lock")
	if address.EndpointLockPath() != wantLock {
		t.Fatalf("EndpointLockPath() = %q, want %q", address.EndpointLockPath(), wantLock)
	}
}

func TestNewAgentAddressRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name string
		home string
		id   string
	}{
		{name: "empty home", id: "abc234"},
		{name: "empty id", home: t.TempDir()},
		{name: "short id", home: t.TempDir(), id: "abc23"},
		{name: "long id", home: t.TempDir(), id: "abc2345"},
		{name: "invalid alphabet", home: t.TempDir(), id: "abc231"},
		{name: "path traversal", home: t.TempDir(), id: "../agent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewAgentAddress(test.home, test.id); err == nil {
				t.Fatal("NewAgentAddress() succeeded, want validation error")
			}
		})
	}
}
