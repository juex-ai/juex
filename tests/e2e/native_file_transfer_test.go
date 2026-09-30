//go:build linux || darwin

package e2e

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativeFileTransferEngineIdentityAndDeferredImport(t *testing.T) {
	config := nativeConfig(t)
	config.Concurrency = 1
	engine := openNative(t, config)
	data := bytes.Repeat([]byte{0, 255, 13, 10}, (9<<20)/4)
	if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "source.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	export := nativeRequest(t, "capture-once", "export_file", execprotocol.FileTransferArguments{Path: "source.bin"})
	result := nativeRun(t, engine, export)
	if result.State != execprotocol.Completed || result.File == nil || !result.File.Ready || result.File.Manifest.SHA256 != execprotocol.FileDigest(data) || result.OutputBytes > 1024 {
		t.Fatal("capture did not isolate bytes from tool output", result)
	}
	if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "source.bin"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Submit(export); err != nil {
		t.Fatal(err)
	}
	page, err := engine.ReadFile("agent-one", export.ID, 0, execprotocol.FileChunkBytes)
	if err != nil || !bytes.Equal(page.Data, data[:len(page.Data)]) {
		t.Fatal("capture retry reopened source", err)
	}
	if _, err := engine.ReadFile("another-agent", export.ID, 0, 10); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("capture escaped Agent", err)
	}
	manifest := result.File.Manifest
	importRequest := nativeRequest(t, "import-once", "import_file", execprotocol.FileTransferArguments{Path: "target.bin", Manifest: &manifest})
	if _, err := engine.Submit(importRequest); err != nil {
		t.Fatal(err)
	}
	// Receiving data must not occupy the only command execution slot.
	probe := nativeRun(t, engine, nativeRequest(t, "during-upload", "exec_command", native.CommandArguments{Command: "printf available"}))
	if probe.State != execprotocol.Completed {
		t.Fatal(probe)
	}
	for offset := 0; offset < len(data); {
		end := min(offset+execprotocol.FileChunkBytes, len(data))
		chunk := execprotocol.FileChunk{Offset: int64(offset), Data: data[offset:end], SHA256: execprotocol.FileDigest(data[offset:end])}
		for range 2 {
			status, err := engine.WriteFile("agent-one", importRequest.ID, chunk)
			if err != nil || status.Cursor != int64(end) {
				t.Fatal(status, err)
			}
		}
		offset = end
	}
	if _, err := os.Stat(filepath.Join(config.WorkingDirectory, "target.bin")); !os.IsNotExist(err) {
		t.Fatal("uncommitted target visible", err)
	}
	for range 2 {
		if _, err := engine.CommitFile("agent-one", importRequest.ID); err != nil {
			t.Fatal(err)
		}
	}
	result = nativeEventually(t, engine, importRequest.ID, func(s execprotocol.Snapshot) bool { return s.State.Terminal() })
	if result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	if _, err := engine.Submit(importRequest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "target.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("import bytes", err)
	}
}

func TestNativeFileTransferPreservesBinaryAndNeverOverwrites(t *testing.T) {
	directory := t.TempDir()
	data := bytes.Repeat([]byte{0, 255, 13, 10, 128}, (9<<20)/5)
	if err := os.WriteFile(filepath.Join(directory, "source"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var captured bytes.Buffer
	if err := native.RunFileExport(context.Background(), directory, "source", &captured); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(captured.Bytes(), data) {
		t.Fatal("binary capture changed bytes")
	}
	manifest := execprotocol.FileManifest{Size: int64(len(data)), SHA256: execprotocol.FileDigest(data)}
	if err := native.RunFileImport(context.Background(), directory, "target", manifest, bytes.NewReader(captured.Bytes())); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(filepath.Join(directory, "target"))
	if err != nil || !bytes.Equal(result, data) {
		t.Fatal("import changed bytes", err)
	}
	if err := native.RunFileImport(context.Background(), directory, "target", manifest, bytes.NewReader(captured.Bytes())); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("existing destination overwritten", err)
	}
	bad := manifest
	bad.SHA256 = execprotocol.FileDigest([]byte("different"))
	if err := native.RunFileImport(context.Background(), directory, "bad", bad, bytes.NewReader(captured.Bytes())); err == nil {
		t.Fatal("wrong hash imported")
	}
	if _, err := os.Lstat(filepath.Join(directory, "bad")); !os.IsNotExist(err) {
		t.Fatal("bad target exposed", err)
	}
	if err := os.Symlink(filepath.Join(directory, "source"), filepath.Join(directory, "link")); err != nil {
		t.Fatal(err)
	}
	if err := native.RunFileImport(context.Background(), directory, "link", manifest, bytes.NewReader(data)); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("existing symlink followed", err)
	}
	for _, name := range []string{"empty", "空文件"} {
		if err := native.RunFileImport(context.Background(), directory, name, execprotocol.FileManifest{SHA256: execprotocol.FileDigest(nil)}, bytes.NewReader(nil)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 5 {
		t.Fatal("staging file leaked", len(entries), err)
	}
}

type transferWriter func([]byte) (int, error)

func TestNativeFileTransferQuotaRevocationAndBinaryAcknowledgment(t *testing.T) {
	t.Run("failed capture releases reservation", func(t *testing.T) {
		config := nativeConfig(t)
		config.FileStorageLimit = execprotocol.MaxFileBytes + 4096
		engine := openNative(t, config)
		request := nativeRequest(t, "missing-working-directory", "export_file", execprotocol.FileTransferArguments{Path: "source", WorkingDirectory: filepath.Join(config.WorkingDirectory, "missing")})
		result := nativeRun(t, engine, request)
		if result.State != execprotocol.Failed {
			t.Fatal(result)
		}
		if err := engine.Acknowledge(request.AgentID, request.ID, result.OutputBytes); err != nil {
			t.Fatal(err)
		}
		if err := engine.Prune(time.Now().Add(8 * 24 * time.Hour)); err != nil {
			t.Fatal(err)
		}
		request.ID = "second-capture"
		if _, err := engine.Submit(request); err != nil {
			t.Fatal("failed capture leaked file reservation", err)
		}
	})
	t.Run("quota and revocation", func(t *testing.T) {
		config := nativeConfig(t)
		config.FileStorageLimit = 4100
		engine := openNative(t, config)
		manifest := execprotocol.FileManifest{Size: 4, SHA256: execprotocol.FileDigest([]byte("test"))}
		request := nativeRequest(t, "one-upload", "import_file", execprotocol.FileTransferArguments{Path: "target", Manifest: &manifest})
		if _, err := engine.Submit(request); err != nil {
			t.Fatal(err)
		}
		second := request
		second.ID = "second-upload"
		if _, err := engine.Submit(second); !errors.Is(err, execprotocol.ErrQuota) {
			t.Fatal("file reservation ignored", err)
		}
		if err := engine.Restrict(map[string][]execprotocol.Capability{}); err != nil {
			t.Fatal(err)
		}
		if err := engine.Restrict(config.Grants); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.WriteFile("agent-one", request.ID, execprotocol.FileChunk{Data: []byte("test"), SHA256: manifest.SHA256}); !errors.Is(err, execprotocol.ErrConflict) {
			t.Fatal("revoked import revived", err)
		}
		result, err := engine.Snapshot("agent-one", request.ID, 0, 100)
		if err != nil || result.State != execprotocol.Cancelled {
			t.Fatal(result, err)
		}
		if err := engine.Acknowledge("agent-one", request.ID, result.OutputBytes); err != nil {
			t.Fatal(err)
		}
		if err := engine.Prune(time.Now().Add(8 * 24 * time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Submit(second); err != nil {
			t.Fatal("purged file capacity retained", err)
		}
	})
	t.Run("separate file acknowledgement", func(t *testing.T) {
		config := nativeConfig(t)
		engine := openNative(t, config)
		if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "source"), []byte("retained"), 0600); err != nil {
			t.Fatal(err)
		}
		request := nativeRequest(t, "retained-source", "export_file", execprotocol.FileTransferArguments{Path: "source"})
		result := nativeRun(t, engine, request)
		if result.File == nil {
			t.Fatal(result)
		}
		if err := engine.Acknowledge("agent-one", request.ID, result.OutputBytes); err != nil {
			t.Fatal(err)
		}
		if err := engine.Prune(time.Now().Add(8 * 24 * time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.ReadFile("agent-one", request.ID, 0, 100); err != nil {
			t.Fatal("metadata acknowledgment expired unreceived bytes", err)
		}
		if err := engine.Close(); err != nil {
			t.Fatal(err)
		}
		engine = openNative(t, config)
		if _, err := engine.ReadFile("agent-one", request.ID, 0, 100); err != nil {
			t.Fatal("restart lost retained capture", err)
		}
		if err := engine.AcknowledgeFile("agent-one", request.ID, result.File.Manifest); err != nil {
			t.Fatal(err)
		}
		if err := engine.Prune(time.Now().Add(8 * 24 * time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.ReadFile("agent-one", request.ID, 0, 100); !errors.Is(err, execprotocol.ErrNotFound) {
			t.Fatal("acknowledged bytes never expired", err)
		}
		if err := engine.Close(); err != nil {
			t.Fatal(err)
		}
		engine = openNative(t, config)
		retry, err := engine.Submit(request)
		if err != nil || !retry.FileExpired || retry.State != execprotocol.Completed {
			t.Fatal("expired capture replayed", retry, err)
		}
	})
}

func (w transferWriter) Write(data []byte) (int, error) { return w(data) }

func TestNativeFileTransferCrashKeepsUnknownIdentity(t *testing.T) {
	config := nativeConfig(t)
	command := exec.Command(os.Args[0], "-test.run=^TestNativeFileTransferCrashHelper$")
	command.Env = append(os.Environ(), "JUEX_FILE_CRASH_STATE="+config.StateDirectory, "JUEX_FILE_CRASH_WORK="+config.WorkingDirectory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	engine := openNative(t, config)
	manifest := execprotocol.FileManifest{Size: 4, SHA256: execprotocol.FileDigest([]byte("test"))}
	request := nativeRequest(t, "crashed-file-import", "import_file", execprotocol.FileTransferArguments{Path: "target", Manifest: &manifest})
	result, err := engine.Submit(request)
	if err != nil || result.State != execprotocol.Unknown || result.File == nil || result.File.Cursor != 2 {
		t.Fatal("restart replayed an incomplete import", result, err)
	}
	if _, err := engine.WriteFile(request.AgentID, request.ID, execprotocol.FileChunk{Offset: 2, Data: []byte("st"), SHA256: execprotocol.FileDigest([]byte("st"))}); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("unknown import accepted new bytes", err)
	}
	if _, err := engine.CommitFile(request.AgentID, request.ID); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("unknown import published", err)
	}
	if err := engine.Prune(time.Now().Add(30 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(config.WorkingDirectory, "target")); !os.IsNotExist(err) {
		t.Fatal("unknown import created target", err)
	}
}

func TestNativeFileTransferCrashHelper(t *testing.T) {
	state := os.Getenv("JUEX_FILE_CRASH_STATE")
	if state == "" {
		return
	}
	engine, err := native.Open(native.Config{StateDirectory: state, EnvironmentID: "native-test-device", WorkingDirectory: os.Getenv("JUEX_FILE_CRASH_WORK"), Grants: map[string][]execprotocol.Capability{"agent-one": {execprotocol.Files}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := execprotocol.FileManifest{Size: 4, SHA256: execprotocol.FileDigest([]byte("test"))}
	request := nativeRequest(t, "crashed-file-import", "import_file", execprotocol.FileTransferArguments{Path: "target", Manifest: &manifest})
	if _, err := engine.Submit(request); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.WriteFile(request.AgentID, request.ID, execprotocol.FileChunk{Data: []byte("te"), SHA256: execprotocol.FileDigest([]byte("te"))}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestNativeFileExportRejectsChangingSourceAndCancelledImport(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "source")
	if err := os.WriteFile(path, bytes.Repeat([]byte("value"), 1<<16), 0600); err != nil {
		t.Fatal(err)
	}
	changed := false
	err := native.RunFileExport(context.Background(), directory, "source", transferWriter(func(data []byte) (int, error) {
		if !changed {
			changed = true
			now := time.Now().Add(time.Hour)
			if err := os.Chtimes(path, now, now); err != nil {
				t.Fatal(err)
			}
		}
		return len(data), nil
	}))
	if !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("changed source accepted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = native.RunFileImport(ctx, directory, "cancelled", execprotocol.FileManifest{SHA256: execprotocol.FileDigest(nil)}, bytes.NewReader(nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "cancelled")); !os.IsNotExist(err) {
		t.Fatal("cancelled import created target", err)
	}
	if err := native.RunFileExport(context.Background(), directory, ".", io.Discard); err == nil {
		t.Fatal("directory exported as file")
	}
}
