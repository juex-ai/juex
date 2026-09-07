package toolcontracts

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/sandbox"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	"github.com/juex-ai/juex/tests/testsupport/toolset"
)

func TestBuiltins_FileWritesDoNotMutateExternalHardLinks(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("hard-link count enforcement is supported by the sandbox platforms")
	}
	tests := []struct {
		name string
		call func(t *testing.T, r *toolcore.Registry, target string) error
	}{
		{
			name: "write",
			call: func(t *testing.T, r *toolcore.Registry, target string) error {
				t.Helper()
				_, err := r.Call(context.Background(), "write", map[string]any{"path": target, "content": "new\n"})
				return err
			},
		},
		{
			name: "edit",
			call: func(t *testing.T, r *toolcore.Registry, target string) error {
				t.Helper()
				_, err := r.Call(context.Background(), "edit", map[string]any{"path": target, "old": "old\n", "new": "new\n"})
				return err
			},
		},
		{
			name: "apply_patch",
			call: func(t *testing.T, r *toolcore.Registry, target string) error {
				t.Helper()
				patch := strings.Join([]string{
					"*** Begin Patch",
					"*** Update File: " + filepath.Base(target),
					"@@",
					"-old",
					"+new",
					"*** End Patch",
				}, "\n")
				_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": patch})
				return err
			},
		},
		{
			name: "chunked_write",
			call: func(t *testing.T, r *toolcore.Registry, target string) error {
				t.Helper()
				_, err := r.Call(context.Background(), "write_begin", map[string]any{"path": target})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			outsideDir := t.TempDir()
			outsideFile := filepath.Join(outsideDir, "outside.txt")
			if err := os.WriteFile(outsideFile, []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(workDir, "inside.txt")
			if err := os.Link(outsideFile, target); err != nil {
				t.Skipf("hard links unavailable: %v", err)
			}

			r := toolcore.NewRegistry()
			toolset.Register(r, toolset.Options{
				WorkDir:       workDir,
				AgentStateDir: t.TempDir(),

				Sandbox: sandbox.DefaultPolicyForOS("linux"),
			})
			if err := tt.call(t, r, target); err == nil || !strings.Contains(err.Error(), "multiple hard links") {
				t.Fatalf("write err = %v, want multiple hard links rejection", err)
			}

			if got := string(readContractFile(t, outsideFile)); got != "old\n" {
				t.Fatalf("outside hard-link target = %q, want unchanged", got)
			}
			if got := string(readContractFile(t, target)); got != "old\n" {
				t.Fatalf("workspace hard link = %q, want unchanged", got)
			}
			outsideInfo, err := os.Stat(outsideFile)
			if err != nil {
				t.Fatal(err)
			}
			targetInfo, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(outsideInfo, targetInfo) {
				t.Fatal("rejected write unexpectedly replaced the workspace hard link")
			}
		})
	}
}

func TestRegisterBuiltinsChunkedWriteSchema(t *testing.T) {
	r := toolcore.NewRegistry()
	toolset.Register(r, toolset.Options{WorkDir: t.TempDir()})
	for _, name := range []string{"write_begin", "write_chunk", "write_commit", "write_abort"} {
		tool, ok := r.Get(name)
		if !ok {
			t.Fatalf("%s should be registered", name)
		}
		if tool.Schema["type"] != "object" {
			t.Fatalf("%s schema = %+v", name, tool.Schema)
		}
	}
	begin, _ := r.Get("write_begin")
	if !strings.Contains(begin.Description, "write_id") || !strings.Contains(begin.Description, "write_abort") {
		t.Fatalf("write_begin description missing standalone workflow: %q", begin.Description)
	}
	chunk, _ := r.Get("write_chunk")
	if !strings.Contains(chunk.Description, "Index starts at 0") || !strings.Contains(chunk.Description, "4000 bytes") {
		t.Fatalf("write_chunk description missing standalone constraints: %q", chunk.Description)
	}
	if chunk.Schema["additionalProperties"] != false {
		t.Fatalf("write_chunk schema should reject additional properties: %+v", chunk.Schema)
	}
	contentSchema, ok := chunk.Schema["properties"].(map[string]any)["content"].(map[string]any)
	if !ok || contentSchema["type"] != "string" || len(contentSchema) != 1 {
		t.Fatalf("write_chunk content schema should keep only its type: %+v", contentSchema)
	}

	write, _ := r.Get("write")
	if !strings.Contains(write.Description, "write_begin/write_chunk/write_commit") {
		t.Fatalf("write description should steer long content to chunked write: %q", write.Description)
	}
	writeContentSchema, ok := write.Schema["properties"].(map[string]any)["content"].(map[string]any)
	if !ok || writeContentSchema["maxLength"] != 2000 {
		t.Fatalf("write content schema should cap provider-visible content length: %+v", writeContentSchema)
	}
}
func readContractFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
