//go:build linux || darwin

package e2e

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativePatchReceiptSurvivesRestartWithoutReapplying(t *testing.T) {
	config := nativeConfig(t)
	engine := openNative(t, config)
	request := nativeRequest(t, "patch-receipt", "apply_patch", native.FileArguments{PatchText: "*** Begin Patch\n*** Add File: new.txt\n+first\n*** End Patch"})
	first := nativeRun(t, engine, request)
	if first.State != execprotocol.Completed {
		t.Fatal(first)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "new.txt"), []byte("later edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	reopened := openNative(t, config)
	replay := nativeRun(t, reopened, request)
	if replay.State != first.State || replay.Text() != first.Text() {
		t.Fatal("lost immutable receipt", replay)
	}
	data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "new.txt"))
	if err != nil || string(data) != "later edit\n" {
		t.Fatal("replayed patch", string(data), err)
	}
	changed := nativeRequest(t, request.ID, "apply_patch", native.FileArguments{PatchText: "*** Begin Patch\n*** Delete File: new.txt\n*** End Patch"})
	if _, err := reopened.Submit(changed); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("changed retry accepted", err)
	}
}
