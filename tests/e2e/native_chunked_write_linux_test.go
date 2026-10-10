//go:build linux

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativeHostedChunkedWriteStreamsLargeCommitAsFileOwner(t *testing.T) {
	helper := os.Getenv("JUEX_TEST_GUEST_HELPER")
	if helper == "" {
		t.Skip("set JUEX_TEST_GUEST_HELPER to a rebuilt Linux juex-guest for hosted acceptance")
	}
	if os.Geteuid() != 0 {
		t.Fatal("run inside an isolated root controller container")
	}
	base, err := os.MkdirTemp("", "juex-hosted-write-")
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
	if err := os.WriteFile(filepath.Join(work, "private"), []byte("controller"), 0600); err != nil {
		t.Fatal(err)
	}
	config := native.Config{StateDirectory: filepath.Join(base, "state"), EnvironmentID: "hosted-write", WorkingDirectory: work, Grants: map[string][]execprotocol.Capability{"agent-one": {execprotocol.Files}}, ProcessUser: &native.ProcessUser{UID: 1000, GID: 1000, Home: work, Helper: helper}}
	e := openNative(t, config)
	run := func(id, kind string, args any) execprotocol.Snapshot {
		t.Helper()
		r := nativeRequest(t, id, kind, args)
		r.WriteContext = &execprotocol.WriteContext{ThreadID: "thread", ResetID: "reset"}
		return nativeRun(t, e, r)
	}
	denied := run("private", "write_begin", map[string]any{"path": "private"})
	if denied.State != execprotocol.Failed {
		t.Fatal("controller-only file accepted", denied)
	}
	if got := run("begin", "write_begin", map[string]any{"path": "large.txt", "mode": "create"}); got.State != execprotocol.Completed {
		t.Fatal(got)
	}
	chunk := strings.Repeat("中", 1333)
	for index := 0; index < 526; index++ {
		got := run(fmt.Sprintf("chunk-%d", index), "write_chunk", map[string]any{"write_id": "begin", "index": index, "content": chunk})
		if got.State != execprotocol.Completed {
			t.Fatal(index, got)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e = openNative(t, config)
	committed := run("commit", "write_commit", map[string]any{"write_id": "begin", "expected_chunks": 526})
	if committed.State != execprotocol.Completed || committed.Write == nil || committed.Write.Bytes <= 2<<20 {
		t.Fatal(committed)
	}
	data, err := os.ReadFile(filepath.Join(work, "large.txt"))
	if err != nil || !bytes.Equal(data, bytes.Repeat([]byte(chunk), 526)) {
		t.Fatal("stream changed", len(data), err)
	}
	info, err := os.Stat(filepath.Join(work, "large.txt"))
	if err != nil || info.Sys().(*syscall.Stat_t).Uid != 1000 {
		t.Fatal("controller published Agent file", info, err)
	}
	data, err = os.ReadFile(filepath.Join(work, "private"))
	if err != nil || string(data) != "controller" {
		t.Fatal("private file changed", err)
	}
}
