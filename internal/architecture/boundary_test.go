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
	modulePath + "/internal/hooks",
	modulePath + "/internal/mcp",
	modulePath + "/internal/modules/",
	modulePath + "/internal/observable",
	modulePath + "/internal/skills",
}

// Framework package roots are explicit so adding a new root remains an
// architectural decision rather than an accidental glob expansion.
var frameworkImports = []string{
	modulePath + "/internal/prompt",
	modulePath + "/internal/runtime",
}

var frameworkDirs = []string{
	"internal/prompt",
	"internal/runtime",
}

// These are the business-agnostic technical primitives identified as
// Foundation by ARCHITECTURE.md. Keep the list explicit so moving a package
// across the boundary requires an architectural review.
var foundationDirs = []string{
	"internal/agentstate",
	"internal/artifact",
	"internal/cancellation",
	"internal/chunkedwrite",
	"internal/endpoint",
	"internal/environment",
	"internal/errorclass",
	"internal/eventmedia",
	"internal/events",
	"internal/frontmatter",
	"internal/homestore",
	"internal/llm",
	"internal/netbootstrap",
	"internal/provenance",
	"internal/processmetrics",
	"internal/sandbox",
	"internal/statusstream",
	"internal/thread",
	"internal/toolevents",
	"internal/tools",
	"internal/version",
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
		modulePath + "/internal/cli",
		modulePath + "/internal/fleet",
		modulePath + "/internal/fleetweb",
		modulePath + "/internal/mcp",
		modulePath + "/internal/observable",
		modulePath + "/internal/web",
	}
	checkImports(t, root, "internal/thread", "Thread storage", func(importPath string) bool {
		return matchesImportRoot(importPath, forbidden)
	})
}

func TestImportBoundaryClassifiesCurrentFrameworkAndFeatureRoots(t *testing.T) {
	for _, importPath := range []string{
		modulePath + "/internal/hooks",
		modulePath + "/internal/mcp",
		modulePath + "/internal/modules/promptcontext",
		modulePath + "/internal/observable",
		modulePath + "/internal/skills",
	} {
		if !isConcreteFeatureImport(importPath) {
			t.Errorf("%s is not classified as a concrete Feature", importPath)
		}
	}
	for _, importPath := range []string{
		modulePath + "/internal/prompt",
		modulePath + "/internal/runtime/module",
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
