package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetProductionCodeDoesNotDependOnRuntimeDomain(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "github.com/juex-ai/juex/internal/runtime") {
			t.Fatalf("%s imports the runtime domain instead of the L4 status contract", name)
		}
	}
}
