package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/juex-ai/juex"

var concreteFeatureImports = []string{
	modulePath + "/internal/features/",
	modulePath + "/internal/features/hooks",
	modulePath + "/internal/features/mcp",
	modulePath + "/internal/modules/",
	modulePath + "/internal/features/observables",
	modulePath + "/internal/features/skills",
}

// Framework package roots are explicit so adding a new root remains an
// architectural decision rather than an accidental glob expansion.
var frameworkImports = []string{
	modulePath + "/internal/framework/module",
	modulePath + "/internal/framework/prompt",
	modulePath + "/internal/framework/runtime",
}

var frameworkDirs = []string{
	"internal/framework/prompt",
	"internal/framework/runtime",
}

// These are the business-agnostic technical primitives identified as
// Foundation by ARCHITECTURE.md. Keep the list explicit so moving a package
// across the boundary requires an architectural review.
var foundationDirs = []string{
	"internal/framework/agentstate",
	"internal/foundation/artifact",
	"internal/foundation/cancellation",
	"internal/chunkedwrite",
	"internal/framework/endpoint",
	"internal/foundation/environment",
	"internal/foundation/errorclass",
	"internal/framework/observationmedia",
	"internal/foundation/events",
	"internal/features/skills/internal/frontmatter",
	"internal/foundation/homestore",
	"internal/foundation/jsonl",
	"internal/llm",
	"internal/foundation/netbootstrap",
	"internal/framework/provenance",
	"internal/foundation/processmetrics",
	"internal/foundation/sandbox",
	"internal/foundation/statusstream",
	"internal/framework/thread",
	"internal/foundation/toolevents",
	"internal/tools",
	"internal/foundation/version",
}

func TestFoundationDoesNotImportFrameworkOrConcreteFeatures(t *testing.T) {
	root := repositoryRoot(t)
	for _, dir := range foundationDirs {
		checkImports(t, root, dir, "Foundation", isFrameworkOrConcreteFeatureImport)
	}
}

func TestFrameworkDoesNotImportConcreteFeatures(t *testing.T) {
	root := repositoryRoot(t)
	for _, dir := range frameworkDirs {
		checkImports(t, root, dir, "Framework", isConcreteFeatureImport)
	}
}

func TestThreadStorageDoesNotImportAdaptersOrFeatureTransports(t *testing.T) {
	root := repositoryRoot(t)
	forbidden := []string{
		modulePath + "/internal/app",
		modulePath + "/internal/entrypoints/cli",
		modulePath + "/internal/fleet",
		modulePath + "/internal/entrypoints/fleethttp",
		modulePath + "/internal/features/mcp",
		modulePath + "/internal/features/observables",
		modulePath + "/internal/entrypoints/agenthttp",
	}
	checkImports(t, root, "internal/framework/thread", "Thread storage", func(importPath string) bool {
		return matchesImportRoot(importPath, forbidden)
	})
}

func TestImportBoundaryClassifiesCurrentFrameworkAndFeatureRoots(t *testing.T) {
	for _, importPath := range []string{
		modulePath + "/internal/features/hooks",
		modulePath + "/internal/features/mcp",
		modulePath + "/internal/features/scratchpad",
		modulePath + "/internal/features/observables",
		modulePath + "/internal/features/skills",
	} {
		if !isConcreteFeatureImport(importPath) {
			t.Errorf("%s is not classified as a concrete Feature", importPath)
		}
	}
	for _, importPath := range []string{
		modulePath + "/internal/framework/prompt",
		modulePath + "/internal/framework/module",
	} {
		if !isFrameworkOrConcreteFeatureImport(importPath) {
			t.Errorf("%s is not classified as Framework", importPath)
		}
	}
}

func checkImports(t *testing.T, root, relativeDir, layer string, forbidden func(string) bool) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(relativeDir))
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files := token.NewFileSet()
		parsed, err := parser.ParseFile(files, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if !forbidden(importPath) {
				continue
			}
			file, err := filepath.Rel(root, path)
			if err != nil {
				file = path
			}
			t.Errorf("%s package %s imports forbidden higher-layer package %s", layer, filepath.ToSlash(file), importPath)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", relativeDir, err)
	}
}

func isConcreteFeatureImport(importPath string) bool {
	return matchesImportRoot(importPath, concreteFeatureImports)
}

func isFrameworkOrConcreteFeatureImport(importPath string) bool {
	return matchesImportRoot(importPath, frameworkImports) || isConcreteFeatureImport(importPath)
}

func matchesImportRoot(importPath string, roots []string) bool {
	for _, forbidden := range roots {
		if strings.HasSuffix(forbidden, "/") {
			if strings.HasPrefix(importPath, forbidden) {
				return true
			}
			continue
		}
		if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
			return true
		}
	}
	return false
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve boundary test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
