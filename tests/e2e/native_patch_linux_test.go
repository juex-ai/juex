//go:build linux

package e2e

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativeHostedPatchUsesUnprivilegedWorker(t *testing.T) {
	helper := os.Getenv("JUEX_TEST_GUEST_HELPER")
	if helper == "" {
		t.Skip("set JUEX_TEST_GUEST_HELPER to a rebuilt Linux juex-guest for hosted acceptance")
	}
	if os.Geteuid() != 0 {
		t.Fatal("hosted controller acceptance requires root inside an isolated container")
	}
	base, err := os.MkdirTemp("", "juex-hosted-patch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "workspace")
	if err := os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(work, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "controller-private"), []byte("private\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := native.Config{StateDirectory: filepath.Join(base, "state"), EnvironmentID: "hosted-patch", WorkingDirectory: work, Grants: map[string][]execprotocol.Capability{"agent-one": {execprotocol.Files}}, ProcessUser: &native.ProcessUser{UID: 1000, GID: 1000, Home: work, Helper: helper}}
	engine := openNative(t, config)
	result := nativeRun(t, engine, nativeRequest(t, "hosted-patch", "apply_patch", native.FileArguments{PatchText: "*** Begin Patch\n*** Add File: new.txt\n+worker-owned\n*** End Patch"}))
	if result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	info, err := os.Stat(filepath.Join(work, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Sys().(*syscall.Stat_t).Uid != 1000 {
		t.Fatal("controller wrote Agent file", info.Sys())
	}
	denied := nativeRun(t, engine, nativeRequest(t, "hosted-private-denied", "apply_patch", native.FileArguments{PatchText: "*** Begin Patch\n*** Update File: controller-private\n-private\n+overwritten\n*** End Patch"}))
	if denied.State != execprotocol.Failed {
		t.Fatal("file permission denial did not remain a definite rejection", denied)
	}
	data, err := os.ReadFile(filepath.Join(work, "controller-private"))
	if err != nil || string(data) != "private\n" {
		t.Fatal("privileged file changed", string(data), err)
	}
}
