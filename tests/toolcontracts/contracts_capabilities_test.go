package toolcontracts

import (
	"reflect"
	"strings"
	"testing"

	chunkedwritefeature "github.com/juex-ai/juex/internal/features/chunkedwrite"
	filetoolsfeature "github.com/juex-ai/juex/internal/features/filetools"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func TestResolvedWriteRequiresCompleteChunkWorkflow(t *testing.T) {
	for _, missing := range []string{"", "write_begin", "write_chunk", "write_commit", "write_abort"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			input := append(filetoolsfeature.Contributions(filetoolsfeature.Options{WorkDir: t.TempDir()}), chunkedwritefeature.Contributions(chunkedwritefeature.Options{WorkDir: t.TempDir()})...)
			var candidate []toolcore.Tool
			for _, tool := range input {
				if tool.Name != missing {
					candidate = append(candidate, tool)
				}
			}
			before := candidate[1].Clone()
			resolved, err := toolcore.ResolveTools(candidate)
			if err != nil {
				t.Fatal(err)
			}
			write := resolved[1]
			_, limited := write.Schema["properties"].(map[string]any)["content"].(map[string]any)["maxLength"]
			if limited != (missing == "") || strings.Contains(write.Description, "write_begin") != (missing == "") {
				t.Fatalf("write guidance with missing %q: %s, schema=%v", missing, write.Description, write.Schema)
			}
			if !reflect.DeepEqual(before.Definition(), candidate[1].Definition()) {
				t.Fatal("resolution mutated shared contribution")
			}
		})
	}
}
