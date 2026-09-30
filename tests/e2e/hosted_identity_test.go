//go:build linux && hosted

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestHostedWorkerIdentityProtectsControlState(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("hosted identity test requires isolated Linux root")
	}
	helper := os.Getenv("JUEX_GUEST_BINARY")
	if !filepath.IsAbs(helper) {
		t.Fatal("JUEX_GUEST_BINARY must name the candidate guest executable")
	}
	dir, err := os.MkdirTemp("/tmp", "juex-hosted-identity-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chown(filepath.Join(dir, "workspace"), 0, 0)
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	control, work := filepath.Join(dir, "control"), filepath.Join(dir, "workspace")
	for _, path := range []string{control, work} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(control, "credential")
	if err := os.WriteFile(secret, []byte("private-control-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(work, "secret-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(work, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	config := native.Config{StateDirectory: filepath.Join(control, "journal"), EnvironmentID: "hosted-identity", WorkingDirectory: work, Grants: map[string][]execprotocol.Capability{"agent-one": {execprotocol.Files, execprotocol.Shell, execprotocol.MCP}}, ProcessUser: &native.ProcessUser{UID: 1000, GID: 1000, Home: work, Helper: helper}}
	engine := openNative(t, config)
	for _, path := range []string{secret, filepath.Join(work, "secret-link")} {
		result := nativeRun(t, engine, nativeRequest(t, "read-"+filepath.Base(path), "read", native.FileArguments{Path: path}))
		if result.State != execprotocol.Failed || strings.Contains(result.Text(), "private-control-token") {
			t.Fatal("file tool bypassed worker identity", result)
		}
	}
	write := nativeRun(t, engine, nativeRequest(t, "file-write", "write", native.FileArguments{Path: "owned", Content: "worker"}))
	if write.State != execprotocol.Completed {
		t.Fatal(write)
	}
	info, err := os.Stat(filepath.Join(work, "owned"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Sys().(*syscall.Stat_t).Uid != 1000 {
		t.Fatal("file write ran under control identity")
	}
	for _, tty := range []bool{false, true} {
		id := "shell"
		if tty {
			id = "pty"
		}
		result := nativeRun(t, engine, nativeRequest(t, id, "exec_command", native.CommandArguments{Command: "id -u; test ! -r '" + secret + "' && printf protected", TTY: tty}))
		if result.State != execprotocol.Completed || !strings.Contains(result.Text(), "1000") || !strings.Contains(result.Text(), "protected") {
			t.Fatal(result)
		}
	}
	connection := nativeRequest(t, "mcp-worker", "mcp_connect", native.MCPArguments{Command: os.Args[0], Args: []string{"-test.run=^TestNativeExecutorMCPHelper$"}, Environment: map[string]string{"JUEX_NATIVE_MCP_HELPER": "1", "JUEX_NATIVE_MCP_COUNTER": filepath.Join(work, "mcp-counter")}})
	if _, err := engine.Submit(connection); err != nil {
		t.Fatal(err)
	}
	nativeEventually(t, engine, connection.ID, func(s execprotocol.Snapshot) bool { return strings.Contains(s.Text(), `"type":"connected"`) })
	result := nativeRun(t, engine, nativeRequest(t, "mcp-call", "mcp_call", native.MCPArguments{ConnectionID: connection.ID, Name: "echo", Arguments: map[string]any{"text": "worker"}}))
	if result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	info, err = os.Stat(filepath.Join(work, "mcp-counter"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Sys().(*syscall.Stat_t).Uid != 1000 {
		t.Fatal("MCP ran under control identity")
	}
	if err := engine.Cancel("agent-one", connection.ID); err != nil {
		t.Fatal(err)
	}
	nativeEventually(t, engine, connection.ID, func(s execprotocol.Snapshot) bool { return s.State == execprotocol.Cancelled })
}
