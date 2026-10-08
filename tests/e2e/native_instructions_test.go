//go:build linux || darwin

package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativeInstructionReadRetainsReceiptAcrossFileChangesAndRestart(t *testing.T) {
	config := nativeConfig(t)
	path := filepath.Join(config.WorkingDirectory, "AGENTS.md")
	if err := os.WriteFile(path, []byte("first request guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, config)
	request := nativeRequest(t, "first-instruction-snapshot", "read_agent_instructions", native.FileArguments{})
	first := nativeRun(t, engine, request)
	if first.State != execprotocol.Completed {
		t.Fatal(first)
	}
	if err := os.WriteFile(path, []byte("next request guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine = openNative(t, config)
	retained := nativeRun(t, engine, request)
	if retained.State != execprotocol.Completed || string(retained.Output) != string(first.Output) {
		t.Fatal("recovered operation reread changed instructions", retained)
	}
	request.ID = "next-instruction-snapshot"
	next := nativeRun(t, engine, request)
	var snapshot execprotocol.InstructionSnapshot
	if next.State != execprotocol.Completed || json.Unmarshal(next.Output, &snapshot) != nil || snapshot.Validate() != nil || snapshot.Sources[0].Text != "next request guidance" {
		t.Fatal("new operation did not read current instructions", next)
	}
	if err := engine.Restrict(map[string][]execprotocol.Capability{"agent-one": {execprotocol.Shell}}); err != nil {
		t.Fatal(err)
	}
	request.ID = "denied-instruction-snapshot"
	if _, err := engine.Submit(request); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("instruction read bypassed Files grant", err)
	}
}
