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

var featureModuleCleanupMethods = map[string]bool{
	"CloseRuntime":   true,
	"QuiesceRuntime": true,
}

type compositionTypeIndex map[string]map[string]string

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
	types, err := appCompositionTypes(appDir)
	if err != nil {
		t.Fatalf("resolve App composition types: %v", err)
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
		inspectAppToolRegistration(parsed, importPaths(parsed), types, func(call *ast.CallExpr, chain string) {
			position := files.Position(call.Pos())
			t.Errorf("%s:%d directly calls %s; internal/app must not mutate a serving Tool registry", relative, position.Line, chain)
		})
		inspectAppFeatureCleanup(parsed, importPaths(parsed), types, func(call *ast.CallExpr, chain string) {
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
	types, err := appCompositionTypes(dir)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	inspectAppFeatureCleanup(parsed, importPaths(parsed), types, func(_ *ast.CallExpr, chain string) {
		calls = append(calls, chain)
	})
	if len(calls) != 1 || calls[0] != "manager.Close" {
		t.Fatalf("cleanup calls = %v, want type-derived manager.Close", calls)
	}
}

func TestAppFeatureCleanupInspectionClassifiesConcreteModules(t *testing.T) {
	source := `package app
import "github.com/juex-ai/juex/internal/mcp"
func closeFeature(module *mcp.Module) {
	_ = module.QuiesceRuntime(nil)
	_ = module.CloseRuntime(nil)
}`
	parsed, err := parser.ParseFile(token.NewFileSet(), "module.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	inspectAppFeatureCleanup(parsed, importPaths(parsed), nil, func(_ *ast.CallExpr, chain string) {
		calls = append(calls, chain)
	})
	want := []string{"module.QuiesceRuntime", "module.CloseRuntime"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("cleanup calls = %v, want %v", calls, want)
	}
}

func TestAppFeatureCleanupInspectionResolvesNestedSelectors(t *testing.T) {
	source := `package app
import "github.com/juex-ai/juex/internal/modules/builtintools"
type App struct { composition runtimeModuleComposition }
type runtimeModuleComposition struct { builtinTools *builtintools.Module }
func closeFeature(application *App) {
	_ = application.composition.builtinTools.CloseRuntime(nil)
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "nested.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	types, err := appCompositionTypes(dir)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	inspectAppFeatureCleanup(parsed, importPaths(parsed), types, func(_ *ast.CallExpr, chain string) {
		calls = append(calls, chain)
	})
	if len(calls) != 1 || calls[0] != "application.composition.builtinTools.CloseRuntime" {
		t.Fatalf("cleanup calls = %v, want nested Feature Module cleanup", calls)
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

func TestAppToolRegistrationInspectionUsesRegistryTypes(t *testing.T) {
	source := `package app
import "github.com/juex-ai/juex/internal/tools"
type App struct{ registry *tools.Registry }
type router struct{}
func configure(application *App, registry *tools.Registry, routes *router) {
	registry.Register(nil)
	routes.Register(nil)
	tools.RegisterBuiltins(registry, tools.BuiltinOptions{})
	constructed := tools.NewRegistryWithOptions(tools.RegistryOptions{})
	constructed.MustRegister(nil)
	application.registry.Register(nil)
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "registration.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	types, err := appCompositionTypes(dir)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	inspectAppToolRegistration(parsed, importPaths(parsed), types, func(_ *ast.CallExpr, chain string) {
		calls = append(calls, chain)
	})
	want := []string{"registry.Register", "tools.RegisterBuiltins", "constructed.MustRegister", "application.registry.Register"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] || calls[3] != want[3] {
		t.Fatalf("Tool registration calls = %v, want %v", calls, want)
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

func appCompositionTypes(appDir string) (compositionTypeIndex, error) {
	types := make(compositionTypeIndex)
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
				if !ok {
					continue
				}
				structure, ok := spec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				typeName := modulePath + "/internal/app." + spec.Name.Name
				if spec.Name.Name == "App" {
					foundApp = true
				}
				if types[typeName] == nil {
					types[typeName] = make(map[string]string)
				}
				for _, field := range structure.Fields.List {
					for _, name := range field.Names {
						types[typeName][name.Name] = canonicalType(field.Type, imports)
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
	return types, nil
}

func inspectAppFeatureCleanup(file *ast.File, imports map[string]string, types compositionTypeIndex, report func(*ast.CallExpr, string)) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		if ownsFeatureCleanup(function.Recv, imports) {
			continue
		}
		resources := namedCleanupValues(function.Recv, imports, types)
		for name, paths := range namedCleanupValues(function.Type.Params, imports, types) {
			resources[name] = paths
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.AssignStmt:
				for index, left := range value.Lhs {
					name, ok := left.(*ast.Ident)
					if !ok || index >= len(value.Rhs) {
						continue
					}
					if paths := cleanupPathsForExpression(value.Rhs[index], resources); paths != nil {
						resources[name.Name] = paths
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
					paths := cleanupPathsForType(canonicalType(spec.Type, imports), types, nil)
					for index, name := range spec.Names {
						if index < len(spec.Values) {
							paths = cleanupPathsForExpression(spec.Values[index], resources)
						}
						if paths != nil {
							resources[name.Name] = paths
						}
					}
				}
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if !ok {
					break
				}
				paths := cleanupPathsForExpression(selector.X, resources)
				if paths[selector.Sel.Name] {
					report(value, selectorChain(selector))
				}
			}
			return true
		})
	}
}

func ownsFeatureCleanup(receiver *ast.FieldList, imports map[string]string) bool {
	for _, typeName := range namedValueTypes(receiver, imports) {
		if featureCleanupMethods(typeName) != nil || typeName == modulePath+"/internal/app.sideSessionModule" {
			return true
		}
	}
	return false
}

func inspectAppToolRegistration(file *ast.File, imports map[string]string, types compositionTypeIndex, report func(*ast.CallExpr, string)) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		values := namedValueTypes(function.Recv, imports)
		for name, typeName := range namedValueTypes(function.Type.Params, imports) {
			values[name] = typeName
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.AssignStmt:
				for index, left := range value.Lhs {
					name, ok := left.(*ast.Ident)
					if !ok || index >= len(value.Rhs) {
						continue
					}
					if typeName := expressionType(value.Rhs[index], imports, values, types); typeName != "" {
						values[name.Name] = typeName
					} else {
						delete(values, name.Name)
					}
				}
			case *ast.DeclStmt:
				general, ok := value.Decl.(*ast.GenDecl)
				if !ok || general.Tok != token.VAR {
					break
				}
				for _, raw := range general.Specs {
					spec := raw.(*ast.ValueSpec)
					typeName := canonicalType(spec.Type, imports)
					for index, name := range spec.Names {
						if index < len(spec.Values) {
							typeName = expressionType(spec.Values[index], imports, values, types)
						}
						if typeName != "" {
							values[name.Name] = typeName
						}
					}
				}
			case *ast.CallExpr:
				if isToolRegistrationCall(value, imports, values, types) {
					report(value, selectorChain(value.Fun))
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

func namedValueTypes(fields *ast.FieldList, imports map[string]string) map[string]string {
	values := make(map[string]string)
	if fields == nil {
		return values
	}
	for _, field := range fields.List {
		typeName := canonicalType(field.Type, imports)
		for _, name := range field.Names {
			values[name.Name] = typeName
		}
	}
	return values
}

func namedCleanupValues(fields *ast.FieldList, imports map[string]string, types compositionTypeIndex) map[string]map[string]bool {
	resources := make(map[string]map[string]bool)
	if fields == nil {
		return resources
	}
	for _, field := range fields.List {
		paths := cleanupPathsForType(canonicalType(field.Type, imports), types, nil)
		if paths == nil {
			continue
		}
		for _, name := range field.Names {
			resources[name.Name] = paths
		}
	}
	return resources
}

func featureCleanupMethods(typeName string) map[string]bool {
	if methods := featureResourceCleanupMethods[typeName]; methods != nil {
		return methods
	}
	const moduleType = ".Module"
	if strings.HasSuffix(typeName, moduleType) && isConcreteFeatureImport(strings.TrimSuffix(typeName, moduleType)) {
		return featureModuleCleanupMethods
	}
	return nil
}

func cleanupPathsForType(typeName string, types compositionTypeIndex, visiting map[string]bool) map[string]bool {
	if methods := featureCleanupMethods(typeName); methods != nil {
		return methods
	}
	fields := types[typeName]
	if len(fields) == 0 || visiting[typeName] {
		return nil
	}
	if visiting == nil {
		visiting = make(map[string]bool)
	}
	visiting[typeName] = true
	defer delete(visiting, typeName)
	paths := make(map[string]bool)
	for field, fieldType := range fields {
		for path := range cleanupPathsForType(fieldType, types, visiting) {
			paths[field+"."+path] = true
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return paths
}

func cleanupPathsForExpression(expression ast.Expr, resources map[string]map[string]bool) map[string]bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return resources[value.Name]
	case *ast.ParenExpr:
		return cleanupPathsForExpression(value.X, resources)
	case *ast.SelectorExpr:
		paths := cleanupPathsForExpression(value.X, resources)
		prefix := value.Sel.Name + "."
		trimmed := make(map[string]bool)
		for path := range paths {
			if strings.HasPrefix(path, prefix) {
				trimmed[strings.TrimPrefix(path, prefix)] = true
			}
		}
		if len(trimmed) != 0 {
			return trimmed
		}
	}
	return nil
}

func expressionType(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return values[value.Name]
	case *ast.ParenExpr:
		return expressionType(value.X, imports, values, types)
	case *ast.SelectorExpr:
		receiverType := expressionType(value.X, imports, values, types)
		return types[receiverType][value.Sel.Name]
	case *ast.CallExpr:
		selector, ok := value.Fun.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return ""
		}
		switch imports[qualifier.Name] {
		case modulePath + "/internal/tools":
			if selector.Sel.Name == "NewRegistry" || selector.Sel.Name == "NewRegistryWithOptions" {
				return modulePath + "/internal/tools.Registry"
			}
		case modulePath + "/internal/runtime/module":
			if selector.Sel.Name == "BuildToolRegistry" {
				return modulePath + "/internal/tools.Registry"
			}
		}
	}
	return ""
}

func isToolRegistrationCall(call *ast.CallExpr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	const registryType = modulePath + "/internal/tools.Registry"
	switch function := call.Fun.(type) {
	case *ast.Ident:
		return imports["."] == modulePath+"/internal/tools" && isToolRegistrationName(function.Name)
	case *ast.SelectorExpr:
		if qualifier, ok := function.X.(*ast.Ident); ok && imports[qualifier.Name] == modulePath+"/internal/tools" {
			return isToolRegistrationName(function.Sel.Name)
		}
		return expressionType(function.X, imports, values, types) == registryType && isToolRegistrationName(function.Sel.Name)
	default:
		return false
	}
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
