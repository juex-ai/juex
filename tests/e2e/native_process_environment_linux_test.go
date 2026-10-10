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

func TestNativeHostedPrivatePATHUsesChildIdentity(t *testing.T) {
	helper := os.Getenv("JUEX_TEST_GUEST_HELPER")
	if helper == "" {
		t.Skip("set JUEX_TEST_GUEST_HELPER to the rebuilt Linux helper")
	}
	if os.Geteuid() != 0 {
		t.Fatal("run in an isolated root controller container")
	}
	base, err := os.MkdirTemp("", "juex-private-environment-")
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
	config := native.Config{StateDirectory: filepath.Join(base, "state"), EnvironmentID: "private-path", WorkingDirectory: work, Grants: map[string][]execprotocol.Capability{"agent-one": {execprotocol.Shell, execprotocol.MCP}}, ProcessUser: &native.ProcessUser{UID: 1000, GID: 1000, Home: work, Helper: helper}}
	privatePathProcesses(t, config, 1000)
	info, err := os.Stat(filepath.Join(work, "private-mcp-counter"))
	if err != nil || info.Sys().(*syscall.Stat_t).Uid != 1000 {
		t.Fatal("MCP did not run as Agent", err)
	}
}
