package builtintools

import (
	"context"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

func TestIndependentToolModules(t *testing.T) {
	opts := tools.BuiltinOptions{WorkDir: t.TempDir()}
	for _, tc := range []struct {
		module *Module
		id     string
		names  []string
	}{
		{NewBasicFiles(opts), "basic-file-tools", []string{"read", "write", "edit"}},
		{NewApplyPatch(opts), "apply-patch", []string{"apply_patch"}},
		{NewFileSearch(opts), "file-search", []string{"grep"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			if string(tc.module.ID()) != tc.id {
				t.Fatalf("id = %s", tc.module.ID())
			}
			provided, err := tc.module.Tools(context.Background(), runtimemodule.ToolContext{})
			if err != nil {
				t.Fatal(err)
			}
			if len(provided) != len(tc.names) {
				t.Fatalf("tool count=%d", len(provided))
			}
			for i, name := range tc.names {
				if provided[i].Name != name {
					t.Fatalf("tool[%d]=%s", i, provided[i].Name)
				}
			}
		})
	}
}
