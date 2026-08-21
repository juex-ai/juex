package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
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

// Framework package roots are explicit for the same reason as Foundation
// directories: adding a new Framework root must be an architectural decision.
var frameworkImports = []string{
	modulePath + "/internal/prompt",
	modulePath + "/internal/runtime",
}

var frameworkDirs = []string{
	"internal/prompt",
	"internal/runtime",
}

var featureResourceCleanupMethods = map[string]map[string]bool{
	modulePath + "/internal/app.sideSessionManager":    {"Close": true, "StartClose": true, "StopAll": true, "WaitClose": true},
	modulePath + "/internal/mcp.Manager":               {"Close": true},
	modulePath + "/internal/observable.Manager":        {"Close": true, "StartClose": true, "WaitClose": true},
	modulePath + "/internal/tools.ShellSessionManager": {"Close": true},
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

func TestAppCompositionDoesNotBypassModuleCatalogOrLifecycle(t *testing.T) {
	root := repositoryRoot(t)
	appDir := filepath.Join(root, "internal", "app")
	resourceFields, err := appFeatureResourceFields(appDir)
	if err != nil {
		t.Fatalf("resolve App Feature resources: %v", err)
	}
	err = filepath.WalkDir(appDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		files := token.NewFileSet()
		parsed, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			position := files.Position(call.Pos())
			switch function := call.Fun.(type) {
			case *ast.Ident:
				if isToolRegistrationName(function.Name) {
					t.Errorf("%s:%d directly calls %s; serving Tools must come from sealed Module catalogs", relative, position.Line, function.Name)
				}
			case *ast.SelectorExpr:
				chain := selectorChain(function)
				if isToolRegistrationName(function.Sel.Name) {
					t.Errorf("%s:%d directly calls %s; internal/app must not mutate a serving Tool registry", relative, position.Line, chain)
				}
			}
			return true
		})
		inspectAppFeatureCleanup(parsed, importPaths(parsed), resourceFields, func(call *ast.CallExpr, chain string) {
			position := files.Position(call.Pos())
			t.Errorf("%s:%d directly calls Feature cleanup %s; cleanup ordering belongs to Module sets", relative, position.Line, chain)
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan internal/app: %v", err)
	}
}

func TestAppFeatureCleanupInspectionUsesDeclaredTypes(t *testing.T) {
	source := `package app
import "github.com/juex-ai/juex/internal/mcp"
type App struct { renamed *mcp.Manager }
func (application *App) closeFeature() {
	manager := application.renamed
	_ = manager.Close()
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "renamed.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	resourceFields, err := appFeatureResourceFields(dir)
	if err != nil {
		t.Fatal(err)
	}
	if resourceFields["renamed"] == nil {
		t.Fatal("renamed MCP Manager field was not classified as a Feature resource")
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	inspectAppFeatureCleanup(parsed, importPaths(parsed), resourceFields, func(_ *ast.CallExpr, chain string) {
		calls = append(calls, chain)
	})
	if len(calls) != 1 || calls[0] != "manager.Close" {
		t.Fatalf("cleanup calls = %v, want type-derived manager.Close", calls)
	}
}

func TestImportBoundaryClassifiesPromptContextAsConcreteFeature(t *testing.T) {
	if !isConcreteFeatureImport(modulePath + "/internal/modules/promptcontext") {
		t.Fatal("prompt context Module package is not classified as a concrete Feature")
	}
	if !isFrameworkOrConcreteFeatureImport(modulePath + "/internal/runtime/module") {
		t.Fatal("runtime Module package is not classified as Framework")
	}
	if !isFrameworkOrConcreteFeatureImport(modulePath + "/internal/prompt") {
		t.Fatal("prompt assembly package is not classified as Framework")
	}
}

func TestToolRegistrationNameClassificationIncludesBulkHelpers(t *testing.T) {
	for _, name := range []string{"Register", "MustRegister", "RegisterTool", "MustRegisterTools", "RegisterBuiltins"} {
		if !isToolRegistrationName(name) {
			t.Errorf("%s was not classified as a Tool registration call", name)
		}
	}
	for _, name := range []string{"Registration", "RegisterHook", "RegisterSession"} {
		if isToolRegistrationName(name) {
			t.Errorf("%s was incorrectly classified as a Tool registration call", name)
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

func isToolRegistrationName(name string) bool {
	if name == "Register" || name == "MustRegister" {
		return true
	}
	if !strings.HasPrefix(name, "Register") && !strings.HasPrefix(name, "MustRegister") {
		return false
	}
	return strings.Contains(name, "Tool") || strings.Contains(name, "Builtin")
}

func appFeatureResourceFields(appDir string) (map[string]map[string]bool, error) {
	fields := make(map[string]map[string]bool)
	foundApp := false
	err := filepath.WalkDir(appDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		imports := importPaths(parsed)
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, raw := range general.Specs {
				spec, ok := raw.(*ast.TypeSpec)
				if !ok || spec.Name.Name != "App" {
					continue
				}
				structure, ok := spec.Type.(*ast.StructType)
				if !ok {
					return nil
				}
				foundApp = true
				for _, field := range structure.Fields.List {
					methods := featureResourceCleanupMethods[canonicalType(field.Type, imports)]
					for _, name := range field.Names {
						if methods != nil {
							fields[name.Name] = methods
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !foundApp {
		return nil, fs.ErrNotExist
	}
	return fields, nil
}

func inspectAppFeatureCleanup(file *ast.File, imports map[string]string, resourceFields map[string]map[string]bool, report func(*ast.CallExpr, string)) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		appValues := namedValuesOfType(function.Recv, imports, modulePath+"/internal/app.App")
		for name := range namedValuesOfType(function.Type.Params, imports, modulePath+"/internal/app.App") {
			appValues[name] = true
		}
		resources := make(map[string]map[string]bool)
		for name, methods := range namedFeatureResources(function.Type.Params, imports) {
			resources[name] = methods
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.AssignStmt:
				for index, left := range value.Lhs {
					name, ok := left.(*ast.Ident)
					if !ok || index >= len(value.Rhs) {
						continue
					}
					if methods := resourceMethods(value.Rhs[index], appValues, resources, resourceFields); methods != nil {
						resources[name.Name] = methods
					} else {
						delete(resources, name.Name)
					}
				}
			case *ast.DeclStmt:
				general, ok := value.Decl.(*ast.GenDecl)
				if !ok || general.Tok != token.VAR {
					break
				}
				for _, raw := range general.Specs {
					spec := raw.(*ast.ValueSpec)
					methods := featureResourceCleanupMethods[canonicalType(spec.Type, imports)]
					for index, name := range spec.Names {
						if index < len(spec.Values) {
							methods = resourceMethods(spec.Values[index], appValues, resources, resourceFields)
						}
						if methods != nil {
							resources[name.Name] = methods
						}
					}
				}
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if !ok {
					break
				}
				methods := resourceMethods(selector.X, appValues, resources, resourceFields)
				if methods[selector.Sel.Name] {
					report(value, selectorChain(selector))
				}
			}
			return true
		})
	}
}

func importPaths(file *ast.File) map[string]string {
	paths := make(map[string]string)
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		paths[name] = path
	}
	return paths
}

func canonicalType(expression ast.Expr, imports map[string]string) string {
	for {
		switch value := expression.(type) {
		case *ast.StarExpr:
			expression = value.X
		case *ast.ParenExpr:
			expression = value.X
		case *ast.SelectorExpr:
			qualifier, ok := value.X.(*ast.Ident)
			if !ok {
				return ""
			}
			return imports[qualifier.Name] + "." + value.Sel.Name
		case *ast.Ident:
			return modulePath + "/internal/app." + value.Name
		default:
			return ""
		}
	}
}

func namedValuesOfType(fields *ast.FieldList, imports map[string]string, wanted string) map[string]bool {
	values := make(map[string]bool)
	if fields == nil {
		return values
	}
	for _, field := range fields.List {
		if canonicalType(field.Type, imports) != wanted {
			continue
		}
		for _, name := range field.Names {
			values[name.Name] = true
		}
	}
	return values
}

func namedFeatureResources(fields *ast.FieldList, imports map[string]string) map[string]map[string]bool {
	resources := make(map[string]map[string]bool)
	if fields == nil {
		return resources
	}
	for _, field := range fields.List {
		methods := featureResourceCleanupMethods[canonicalType(field.Type, imports)]
		if methods == nil {
			continue
		}
		for _, name := range field.Names {
			resources[name.Name] = methods
		}
	}
	return resources
}

func resourceMethods(expression ast.Expr, appValues map[string]bool, resources, resourceFields map[string]map[string]bool) map[string]bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return resources[value.Name]
	case *ast.ParenExpr:
		return resourceMethods(value.X, appValues, resources, resourceFields)
	case *ast.SelectorExpr:
		receiver, ok := value.X.(*ast.Ident)
		if ok && appValues[receiver.Name] {
			return resourceFields[value.Sel.Name]
		}
	}
	return nil
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
