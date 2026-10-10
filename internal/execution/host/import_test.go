package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func importedFile(name, content string) execution.HostImportFile {
	digest := sha256.Sum256([]byte(content))
	return execution.HostImportFile{Path: name, Data: []byte(content), SHA256: hex.EncodeToString(digest[:]), Mode: 0600}
}

func TestHostOfflineFileImportPreservesCompletePrivateSetAndRejectsOverwrite(t *testing.T) {
	b, r, probe := hostFixture(t)
	thread, binding := uuid.NewString(), uuid.NewString()
	request := execution.HostImport{SourceSHA256: strings.Repeat("a", 64), Threads: map[string][]execution.HostImportFile{thread: {importedFile("draft/retained.txt", "working material")}}, Extensions: map[string]execution.HostExtensionImport{binding: {Installation: []execution.HostImportFile{importedFile("juex.extension.json", "manifest")}, Private: []execution.HostImportFile{importedFile("credentials.json", "private credential"), importedFile("users.json", "private users"), importedFile("context-guard/attempted.json", "already attempted")}}}}
	ctx := context.Background()
	receipt, err := b.RestoreFiles(ctx, r, request)
	if err != nil {
		t.Fatal(err)
	}
	if probe.starts != 0 {
		t.Fatal("offline import started executor")
	}
	if _, err := os.Lstat(filepath.Join(b.config.ControlRoot, r.EnvironmentID)); !os.IsNotExist(err) {
		t.Fatal("offline import created enrollment", err)
	}
	if _, err := b.RestoreFiles(ctx, r, request); err != nil {
		t.Fatal("lost-response retry failed", err)
	}
	values, err := native.ExtensionProcessEnvironment(r.WorkingDirectory, ".juex-extensions", r.EnvironmentID, r.AgentID, &execprotocol.ExtensionContext{BindingID: binding, Directory: receipt.ExtensionDirectories[binding]}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var private string
	for _, value := range values {
		if strings.HasPrefix(value, "JUEX_EXT_DATA_DIR=") {
			private = strings.TrimPrefix(value, "JUEX_EXT_DATA_DIR=")
		}
	}
	for _, file := range request.Extensions[binding].Private {
		data, err := os.ReadFile(filepath.Join(private, file.Path))
		if err != nil || string(data) != string(file.Data) {
			t.Fatal("runtime extension path lost private state", file.Path, err)
		}
	}
	path := filepath.Join(receipt.ThreadDirectories[thread], "draft/retained.txt")
	if err := os.WriteFile(path, []byte("new live work"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RestoreFiles(ctx, r, request); err == nil {
		t.Fatal("overwrote changed import")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "new live work" {
		t.Fatal("live work was replaced", err)
	}
	if err := b.Ensure(ctx, r, "credential"); err != nil || probe.starts != 1 {
		t.Fatal("normal startup failed", err, probe.starts)
	}
	if _, err := b.RestoreFiles(ctx, r, request); err == nil {
		t.Fatal("import accepted after enrollment")
	}
}

func TestHostOfflineFileImportRejectsUnsafeAndIncompleteDestinations(t *testing.T) {
	for _, mode := range []string{"traversal", "parent", "case", "different-source", "symlink", "missing", "owner"} {
		t.Run(mode, func(t *testing.T) {
			b, r, _ := hostFixture(t)
			thread := uuid.NewString()
			request := execution.HostImport{SourceSHA256: strings.Repeat("a", 64), Threads: map[string][]execution.HostImportFile{thread: {importedFile("keep", "original")}}}
			if mode == "traversal" || mode == "parent" || mode == "case" {
				switch mode {
				case "traversal":
					request.Threads[thread][0].Path = "../../escape"
				case "parent":
					request.Threads[thread][0].Path = ".."
				case "case":
					request.Threads[thread] = append(request.Threads[thread], importedFile("KEEP", "other"))
				}
				if _, err := b.RestoreFiles(context.Background(), r, request); err == nil {
					t.Fatal("unsafe request accepted")
				}
				if _, err := os.Lstat(filepath.Join(b.config.Root, r.EnvironmentID)); !os.IsNotExist(err) {
					t.Fatal("invalid request published data")
				}
				return
			}
			receipt, err := b.RestoreFiles(context.Background(), r, request)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(receipt.ThreadDirectories[thread], "keep")
			switch mode {
			case "different-source":
				request.SourceSHA256 = strings.Repeat("b", 64)
			case "owner":
				r.AgentID = uuid.NewString()
			case "missing", "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if mode == "symlink" {
					if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := b.RestoreFiles(context.Background(), r, request); err == nil {
				t.Fatal("changed destination accepted")
			}
		})
	}
}
