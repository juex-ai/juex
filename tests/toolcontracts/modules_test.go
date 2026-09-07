package toolcontracts

import (
	"context"
	"testing"

	"github.com/juex-ai/juex/internal/features/applypatch"
	"github.com/juex-ai/juex/internal/features/filesearch"
	"github.com/juex-ai/juex/internal/features/filetools"
	m "github.com/juex-ai/juex/internal/framework/module"
)

func TestIndependentToolModules(t *testing.T) {
	for _, tc := range []struct {
		module m.Module
		id     string
		names  []string
	}{
		{filetools.New(filetools.Options{WorkDir: t.TempDir()}), "basic-file-tools", []string{"read", "write", "edit"}},
		{applypatch.New(applypatch.Options{WorkDir: t.TempDir()}), "apply-patch", []string{"apply_patch"}},
		{filesearch.New(filesearch.Options{WorkDir: t.TempDir()}), "file-search", []string{"grep"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			if string(tc.module.ID()) != tc.id {
				t.Fatal(tc.module.ID())
			}
			provided, err := tc.module.(m.ToolProvider).Tools(context.Background(), m.ToolContext{})
			if err != nil {
				t.Fatal(err)
			}
			if len(provided) != len(tc.names) {
				t.Fatal(provided)
			}
			for i, n := range tc.names {
				if provided[i].Name != n {
					t.Fatal(provided[i].Name)
				}
			}
		})
	}
}
