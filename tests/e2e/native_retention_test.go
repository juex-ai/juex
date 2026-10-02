//go:build linux || darwin

package e2e

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativeExecutorAutomaticRetentionReleasesQuota(t *testing.T) {
	config := nativeConfig(t)
	config.OutputLimit, config.StorageLimit, config.Retention = 8192, 20000, 10*time.Millisecond
	engine := openNative(t, config)
	request := nativeRequest(t, "retained-output", "exec_command", native.CommandArguments{Command: "head -c 8192 /dev/zero"})
	result := nativeRun(t, engine, request)
	if result.State != execprotocol.Completed || result.OutputBytes != config.OutputLimit {
		t.Fatal(result.State, result.OutputBytes)
	}
	second := nativeRequest(t, "after-retention", "exec_command", native.CommandArguments{Command: "printf next"})
	if _, err := engine.Submit(second); !errors.Is(err, execprotocol.ErrQuota) {
		t.Fatal("unacknowledged output did not reserve quota", err)
	}
	if err := engine.Acknowledge(request.AgentID, request.ID, result.OutputBytes); err != nil {
		t.Fatal(err)
	}
	nativeEventually(t, engine, request.ID, func(s execprotocol.Snapshot) bool { return s.OutputExpired })
	if result := nativeRun(t, engine, second); result.State != execprotocol.Completed || result.Text() != "next" {
		t.Fatal(result)
	}
	if replay, err := engine.Submit(request); err != nil || !replay.OutputExpired || replay.State != execprotocol.Completed {
		t.Fatal("automatic retention lost the original operation identity", replay, err)
	}
}

func TestNativeExecutorStartupRetention(t *testing.T) {
	config := nativeConfig(t)
	engine := openNative(t, config)
	request := nativeRequest(t, "expired-while-offline", "exec_command", native.CommandArguments{Command: "printf saved"})
	result := nativeRun(t, engine, request)
	if err := engine.Acknowledge(request.AgentID, request.ID, result.OutputBytes); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	config.Retention = 10 * time.Millisecond
	time.Sleep(2 * config.Retention)
	engine = openNative(t, config)
	result, err := engine.Snapshot(request.AgentID, request.ID, 0, 100)
	if err != nil || !result.OutputExpired || len(result.Output) != 0 {
		t.Fatal("startup retained expired acknowledged output", result, err)
	}
}

func TestNativeExecutorAutomaticRetentionKeepsUnknownAndUnacknowledged(t *testing.T) {
	config := nativeConfig(t)
	command := exec.Command(os.Args[0], "-test.run=^TestNativeFileTransferCrashHelper$")
	command.Env = append(os.Environ(), "JUEX_FILE_CRASH_STATE="+config.StateDirectory, "JUEX_FILE_CRASH_WORK="+config.WorkingDirectory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	config.Retention = 10 * time.Millisecond
	engine := openNative(t, config)
	unknown, err := engine.Snapshot("agent-one", "crashed-file-import", 0, 100)
	if err != nil || unknown.State != execprotocol.Unknown {
		t.Fatal(unknown, err)
	}
	if err := engine.Acknowledge("agent-one", unknown.ID, unknown.OutputBytes); err != nil {
		t.Fatal(err)
	}
	unacknowledged := nativeRequest(t, "unacknowledged-output", "exec_command", native.CommandArguments{Command: "printf preserve"})
	nativeRun(t, engine, unacknowledged)
	settled := nativeRequest(t, "acknowledged-output", "exec_command", native.CommandArguments{Command: "printf expire"})
	result := nativeRun(t, engine, settled)
	if err := engine.Acknowledge(settled.AgentID, settled.ID, result.OutputBytes); err != nil {
		t.Fatal(err)
	}
	nativeEventually(t, engine, settled.ID, func(s execprotocol.Snapshot) bool { return s.OutputExpired })
	retained, err := engine.Snapshot(unacknowledged.AgentID, unacknowledged.ID, 0, 100)
	if err != nil || retained.OutputExpired || retained.Text() != "preserve" {
		t.Fatal("unacknowledged output expired", retained, err)
	}
	unknown, err = engine.Snapshot("agent-one", unknown.ID, 0, 100)
	if err != nil || unknown.State != execprotocol.Unknown || unknown.OutputExpired || unknown.FileExpired || unknown.File == nil || unknown.File.Cursor != 2 {
		t.Fatal("unknown transfer recovery record expired", unknown, err)
	}
	if _, err := os.Lstat(filepath.Join(config.WorkingDirectory, "target")); !os.IsNotExist(err) {
		t.Fatal("unknown import was replayed", err)
	}
}

func TestNativeExecutorAutomaticRetentionWaitsForFileAcknowledgment(t *testing.T) {
	config := nativeConfig(t)
	config.Retention = 10 * time.Millisecond
	if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "source"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, config)
	request := nativeRequest(t, "file-acknowledgment", "export_file", execprotocol.FileTransferArguments{Path: "source"})
	result := nativeRun(t, engine, request)
	if result.State != execprotocol.Completed || result.File == nil {
		t.Fatal(result)
	}
	if err := engine.Acknowledge(request.AgentID, request.ID, result.OutputBytes); err != nil {
		t.Fatal(err)
	}
	nativeEventually(t, engine, request.ID, func(s execprotocol.Snapshot) bool { return s.OutputExpired })
	if chunk, err := engine.ReadFile(request.AgentID, request.ID, 0, 100); err != nil || string(chunk.Data) != "retained" {
		t.Fatal("result acknowledgment expired unreceived source bytes", err)
	}
	if err := engine.AcknowledgeFile(request.AgentID, request.ID, result.File.Manifest); err != nil {
		t.Fatal(err)
	}
	nativeEventually(t, engine, request.ID, func(s execprotocol.Snapshot) bool { return s.FileExpired })
	if _, err := engine.ReadFile(request.AgentID, request.ID, 0, 100); !errors.Is(err, execprotocol.ErrNotFound) {
		t.Fatal("acknowledged source bytes retained after expiry", err)
	}
	if data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "source")); err != nil || string(data) != "retained" {
		t.Fatal("retention touched the user's source file", err)
	}
}
