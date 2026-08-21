package architecture

import (
	"go/ast"
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

// These are the business-agnostic technical primitives identified as
// Foundation by ARCHITECTURE.md. Keep the list explicit so moving a package
// across the boundary requires an architectural review, not a glob change.
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
	"internal/homestore",
	"internal/llm",
	"internal/provenance",
	"internal/sandbox",
	"internal/session",
	"internal/statusstream",
	"internal/toolevents",
	"internal/tools",
}

func TestFoundationAndFrameworkDoNotImportConcreteFeatures(t *testing.T) {
	root := repositoryRoot(t)
	for _, dir := range append([]string{"internal/runtime"}, foundationDirs...) {
		layer := "Foundation"
		if dir == "internal/runtime" {
			layer = "Framework"
		}
		checkImports(t, root, dir, layer)
	}
}

func TestAppCompositionDoesNotBypassModuleCatalogOrLifecycle(t *testing.T) {
	root := repositoryRoot(t)
	for _, relative := range []string{
		"internal/app/app.go",
		"internal/app/runtime_modules.go",
		"internal/app/session_runtime.go",
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		files := token.NewFileSet()
		parsed, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			position := files.Position(call.Pos())
			switch function := call.Fun.(type) {
			case *ast.Ident:
				if strings.HasPrefix(function.Name, "Register") && strings.Contains(function.Name, "Tool") {
					t.Errorf("%s:%d directly calls %s; serving Tools must come from sealed Module catalogs", relative, position.Line, function.Name)
				}
			case *ast.SelectorExpr:
				chain := selectorChain(function)
				if function.Sel.Name == "Register" || function.Sel.Name == "MustRegister" {
					t.Errorf("%s:%d directly calls %s; internal/app must not mutate a serving Tool registry", relative, position.Line, chain)
				}
				if isFeatureCleanupCall(chain) {
					t.Errorf("%s:%d directly calls Feature cleanup %s; cleanup ordering belongs to Module sets", relative, position.Line, chain)
				}
			}
			return true
		})
	}
}

func checkImports(t *testing.T, root, relativeDir, layer string) {
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
			if !isConcreteFeatureImport(importPath) {
				continue
			}
			file, err := filepath.Rel(root, path)
			if err != nil {
				file = path
			}
			t.Errorf("%s package %s imports concrete Feature %s", layer, filepath.ToSlash(file), importPath)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", relativeDir, err)
	}
}

func isConcreteFeatureImport(importPath string) bool {
	for _, forbidden := range concreteFeatureImports {
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

func isFeatureCleanupCall(chain string) bool {
	parts := strings.Split(chain, ".")
	if len(parts) < 2 {
		return false
	}
	receiver, method := parts[len(parts)-2], parts[len(parts)-1]
	methods := map[string]map[string]bool{
		"mcpManager":    {"Close": true},
		"obsv":          {"Close": true, "StartClose": true, "WaitClose": true},
		"shellSessions": {"Close": true},
		"sideSessions":  {"Close": true, "StartClose": true, "StopAll": true, "WaitClose": true},
	}
	return methods[receiver][method]
}

func selectorChain(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		prefix := selectorChain(value.X)
		if prefix == "" {
			return value.Sel.Name
		}
		return prefix + "." + value.Sel.Name
	default:
		return ""
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve boundary test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
