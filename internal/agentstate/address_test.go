package agentstate

import (
	"path/filepath"
	"testing"
)

func TestNewAgentAddressOwnsResidentLayout(t *testing.T) {
	home := t.TempDir()
	address, err := NewAgentAddress(home, "abcdefghijklmnop")
	if err != nil {
		t.Fatal(err)
	}
	if address.ID() != "abcdefghijklmnop" {
		t.Fatalf("ID() = %q", address.ID())
	}
	canonicalHome, err := canonicalPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if address.StateDir() != filepath.Join(canonicalHome, "agents", "abcdefghijklmnop") {
		t.Fatalf("StateDir() = %q", address.StateDir())
	}
	wantLock := filepath.Join(canonicalHome, ".locks", "endpoints", "abcdefghijklmnop.lock")
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
		{name: "empty home", id: "abcdefghijklmnop"},
		{name: "empty id", home: t.TempDir()},
		{name: "invalid id", home: t.TempDir(), id: "../agent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewAgentAddress(test.home, test.id); err == nil {
				t.Fatal("NewAgentAddress() succeeded, want validation error")
			}
		})
	}
}
