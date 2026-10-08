//go:build linux || darwin

package native

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"golang.org/x/sys/unix"
)

func TestInstructionSnapshotReadsOrderedSourcesAndRetainsOriginalBytes(t *testing.T) {
	directory := t.TempDir()
	global := filepath.Join(t.TempDir(), "AGENTS.md")
	paths := []string{global, filepath.Join(directory, "AGENTS.md"), filepath.Join(directory, ".agents", "AGENTS.md")}
	contents := []string{"global guidance\n", "workspace guidance\n", "  local guidance\n"}
	for i, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents[i]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := RunFileOperation(context.Background(), directory, "read_agent_instructions", FileArguments{Path: global}, &output); err != nil {
		t.Fatal(err)
	}
	var snapshot execprotocol.InstructionSnapshot
	if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sources) != len(paths) {
		t.Fatal("instruction sources were dropped", snapshot)
	}
	for i, source := range snapshot.Sources {
		if source.Path != paths[i] || source.State != "loaded" || source.Text != contents[i] || source.Size != int64(len(contents[i])) || source.SHA256 != execprotocol.FileDigest([]byte(contents[i])) {
			t.Fatal("instruction provenance or exact bytes changed", source)
		}
	}
}

func TestInstructionSnapshotMissingEmptyAndDuplicateSources(t *testing.T) {
	directory := t.TempDir()
	root := filepath.Join(directory, "AGENTS.md")
	if err := os.WriteFile(root, []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunFileOperation(context.Background(), directory, "read_agent_instructions", FileArguments{Path: root}, &output); err != nil {
		t.Fatal(err)
	}
	var snapshot execprotocol.InstructionSnapshot
	if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sources) != 2 || snapshot.Sources[0].Path != root || snapshot.Sources[0].State != "empty" || snapshot.Sources[0].Text != " \n" || snapshot.Sources[1].State != "missing" {
		t.Fatal("missing/empty sources or duplicate path were misrepresented", snapshot)
	}
}

func TestInstructionSnapshotRejectsPartialAndInvalidContent(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		directory     bool
	}{
		{name: "invalid UTF-8", content: string([]byte{0xff})},
		{name: "binary NUL", content: "before\x00after"},
		{name: "oversized", content: strings.Repeat("x", (64<<10)+1)},
		{name: "directory", directory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "AGENTS.md")
			var err error
			if tc.directory {
				err = os.Mkdir(path, 0700)
			} else {
				err = os.WriteFile(path, []byte(tc.content), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := RunFileOperation(context.Background(), directory, "read_agent_instructions", FileArguments{}, &output); err == nil || output.Len() != 0 {
				t.Fatal("invalid source returned a successful or partial snapshot", err, output.String())
			}
		})
	}
}

func TestInstructionSnapshotUsesAuthorizedFileSemanticsAndRejectsFIFO(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(t.TempDir(), "guidance")
	if err := os.WriteFile(target, []byte("linked guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunFileOperation(context.Background(), directory, "read_agent_instructions", FileArguments{}, &output); err != nil || !strings.Contains(output.String(), "linked guidance") {
		t.Fatal("native Files authority permits symlinks", err, output.String())
	}
	if err := os.Mkdir(filepath.Join(directory, ".agents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(directory, ".agents", "AGENTS.md"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := RunFileOperation(context.Background(), directory, "read_agent_instructions", FileArguments{}, &output); err == nil || output.Len() != 0 {
		t.Fatal("FIFO returned a partial snapshot", err)
	}
}

func TestInstructionSnapshotEnforcesFileAndSerializedBounds(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "AGENTS.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", execprotocol.MaxInstructionFileBytes)), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunFileOperation(context.Background(), directory, "read_agent_instructions", FileArguments{}, &output); err != nil {
		t.Fatal("exact file boundary rejected", err)
	}
	var snapshot execprotocol.InstructionSnapshot
	if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil || len(snapshot.Sources) != 2 || snapshot.Sources[0].Size != execprotocol.MaxInstructionFileBytes {
		t.Fatal("exact file boundary truncated", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("<", execprotocol.MaxInstructionFileBytes)), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := RunFileOperation(context.Background(), directory, "read_agent_instructions", FileArguments{}, &output); err == nil || output.Len() != 0 {
		t.Fatal("JSON escaping exceeded the snapshot bound", err)
	}
}
