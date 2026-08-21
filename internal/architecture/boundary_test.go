package architecture

import (
	"fmt"
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

const callableCleanupPath = "$call"
const toolRegistrationCallableType = "$tool-registration-callable"
const localFunctionTypePrefix = "$local-function:"

const (
	sliceTypePrefix = "$slice:"
	mapTypePrefix   = "$map:"
	typeSeparator   = "\x00"
	embeddedPrefix  = "$embedded:"
)

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
	modulePath + "/internal/app.sideSessionModule":     {"CloseRuntime": true, "QuiesceRuntime": true},
	modulePath + "/internal/mcp.Manager":               {"Close": true},
	modulePath + "/internal/observable.Manager":        {"Close": true, "StartClose": true, "WaitClose": true},
	modulePath + "/internal/tools.ShellSessionManager": {"Close": true},
}

var featureModuleCleanupMethods = map[string]bool{
	"CloseRuntime":   true,
	"QuiesceRuntime": true,
}

var appFeatureCleanupOwners = map[string]bool{
	modulePath + "/internal/app.sideSessionManager": true,
	modulePath + "/internal/app.sideSessionModule":  true,
}

type compositionTypeIndex struct {
	fields          map[string]map[string]string
	cleanupMethods  map[string]map[string]bool
	functionResults map[string][]string
	namedTypes      map[string]string
	cleanupParams   map[string]map[int]bool
	toolParams      map[string]map[int]bool
	cleanupResults  map[string]map[int]map[string]bool
	toolResults     map[string]map[int]bool
	appFunctionKeys map[string]bool
	appFunctions    []indexedAppFunction
}

type indexedAppFunction struct {
	key     string
	decl    *ast.FuncDecl
	literal *ast.FuncLit
	imports map[string]string
}

func (function indexedAppFunction) receiver() *ast.FieldList {
	if function.decl == nil {
		return nil
	}
	return function.decl.Recv
}

func (function indexedAppFunction) parameters() *ast.FieldList {
	if function.decl != nil {
		return function.decl.Type.Params
	}
	return function.literal.Type.Params
}

func (function indexedAppFunction) results() *ast.FieldList {
	if function.decl != nil {
		return function.decl.Type.Results
	}
	return function.literal.Type.Results
}

func (function indexedAppFunction) body() *ast.BlockStmt {
	if function.decl != nil {
		return function.decl.Body
	}
	return function.literal.Body
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
	"internal/frontmatter",
	"internal/homestore",
	"internal/llm",
	"internal/netbootstrap",
	"internal/provenance",
	"internal/processmetrics",
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
import (
	"io"
	"github.com/juex-ai/juex/internal/mcp"
)
type App struct { renamed *mcp.Manager }
func (application *App) Close() error {
	manager := application.renamed
	_ = manager.Close()
	closer := io.Closer(manager)
	_ = closer.Close()
	return nil
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
	if len(calls) != 2 || calls[0] != "manager.Close" || calls[1] != "closer.Close" {
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
	inspectAppFeatureCleanup(parsed, importPaths(parsed), compositionTypeIndex{}, func(_ *ast.CallExpr, chain string) {
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
type App struct {
	composition runtimeModuleComposition
	runtimeModuleComposition
}
type runtimeModuleComposition struct { builtinTools *builtintools.Module }
func closeFeature(application *App) {
	_ = application.composition.builtinTools.CloseRuntime(nil)
	_ = application.builtinTools.CloseRuntime(nil)
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
	if len(calls) != 2 || calls[0] != "application.composition.builtinTools.CloseRuntime" || calls[1] != "application.builtinTools.CloseRuntime" {
		t.Fatalf("cleanup calls = %v, want nested Feature Module cleanup", calls)
	}
}

func TestAppFeatureCleanupInspectionInfersConstructorsAndClients(t *testing.T) {
	source := `package app
import (
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/observable"
)
type App struct{}
func closeFeature() {
	manager, err := observable.NewManager(observable.ManagerOptions{})
	_ = err
	_ = manager.Close()
	var client *mcp.Client
	_ = client.Close()
	module := mcp.NewModule(nil)
	managerFromModule := module.Manager()
	_ = managerFromModule.Close()
	literal := &mcp.Client{}
	_ = literal.Close()
	_ = mcp.NewModule(nil).CloseRuntime(nil)
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "constructors.go")
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
	want := []string{"manager.Close", "client.Close", "managerFromModule.Close", "literal.Close", "mcp.NewModule.CloseRuntime"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] || calls[3] != want[3] || calls[4] != want[4] {
		t.Fatalf("cleanup calls = %v, want %v", calls, want)
	}
}

func TestAppFeatureCleanupInspectionTracksLocalHelpers(t *testing.T) {
	source := `package app
import "github.com/juex-ai/juex/internal/mcp"
type App struct { manager *mcp.Manager }
type closer interface { Close() error }
type cleanupFunc func() error
func closeOwned(resource closer) { _ = resource.Close() }
func closeTransitively(resource closer) { closeOwned(resource) }
func runCleanup(cleanup cleanupFunc) { _ = cleanup() }
func owned(application *App) closer { return application.manager }
func namedOwned(application *App) (resource closer) {
	resource = application.manager
	return
}
func cleanupFromLiteral(application *App) {
	cleanup := func(resource closer) { _ = resource.Close() }
	cleanup(application.manager)
}
func cleanupThroughAlias(application *App) {
	cleanup := closeOwned
	cleanup(application.manager)
}
func cleanupAfterShadow(application *App, unrelated closer) {
	manager := application.manager
	{
		manager := unrelated
		_ = manager
	}
	_ = manager.Close()
}
func cleanupAfterBranch(application *App, unrelated closer) {
	manager := application.manager
	if true {
		manager = unrelated
	}
	_ = manager.Close()
}
func (application *App) Close() error {
	closeTransitively(application.manager)
	runCleanup(application.manager.Close)
	_ = owned(application).Close()
	_ = namedOwned(application).Close()
	_ = (*mcp.Manager).Close(application.manager)
	return nil
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "helper.go")
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
	want := []string{"cleanup", "cleanup", "manager.Close", "manager.Close", "closeTransitively", "runCleanup", "owned.Close", "namedOwned.Close", "Close"}
	if len(calls) != len(want) {
		t.Fatalf("cleanup calls = %v, want local helper delegation", calls)
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("cleanup calls = %v, want %v", calls, want)
		}
	}
}

func TestAppFeatureCleanupInspectionTracksRangeValues(t *testing.T) {
	source := `package app
import "github.com/juex-ai/juex/internal/mcp"
type App struct{}
type clientList []*mcp.Client
func closeMany(clients []*mcp.Client, indexed map[*mcp.Client]struct{}, byName map[string]*mcp.Client, named clientList) {
	_ = clients[0].Close()
	_ = byName["primary"].Close()
	_ = named[0].Close()
	clients = append(clients, nil)
	_ = clients[0].Close()
	for _, client := range clients { _ = client.Close() }
	for client := range indexed { _ = client.Close() }
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "range.go")
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
	want := []string{"Close", "Close", "Close", "Close", "client.Close", "client.Close"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] || calls[3] != want[3] || calls[4] != want[4] || calls[5] != want[5] {
		t.Fatalf("cleanup calls = %v, want %v", calls, want)
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
type registrar interface{ Register(tools.Tool) error }
type registryList []*tools.Registry
func localRegistry() *tools.Registry { return nil }
func localRegistrar(application *App) registrar { return application.registry }
func namedLocalRegistrar(application *App) (result registrar) {
	result = application.registry
	return
}
func registerFromLiteral(application *App) {
	register := func(registry registrar) { _ = registry.Register(nil) }
	register(application.registry)
}
func registerThroughAlias(application *App) {
	register := registerOwned
	register(application.registry)
}
func registerAfterShadow(application *App, unrelated *router) {
	registry := application.registry
	{
		registry := unrelated
		_ = registry
	}
	registry.Register(nil)
}
func registerAfterBranch(application *App, unrelated *router) {
	registry := application.registry
	if true {
		registry = unrelated
	}
	registry.Register(nil)
}
func registerOwned(registry registrar) { _ = registry.Register(nil) }
func registerTransitively(registry registrar) { registerOwned(registry) }
func runRegistration(register func(tools.Tool) error) { _ = register(nil) }
func configure(application *App, registry *tools.Registry, routes *router) {
	registry.Register(nil)
	register := registry.Register
	register(nil)
	converted := registrar(registry)
	converted.Register(nil)
	routes.Register(nil)
	tools.RegisterBuiltins(registry, tools.BuiltinOptions{})
	bulkRegister := tools.RegisterBuiltins
	bulkRegister(registry, tools.BuiltinOptions{})
	constructed := tools.NewRegistryWithOptions(tools.RegistryOptions{})
	constructed.MustRegister(nil)
	application.registry.Register(nil)
	localRegistry().Register(nil)
	localRegistrar(application).Register(nil)
	namedLocalRegistrar(application).Register(nil)
	registries := []*tools.Registry{registry}
	registries[0].Register(nil)
	registries = append(registries, registry)
	registries[0].Register(nil)
	named := registryList{registry}
	named[0].Register(nil)
	registerTransitively(registry)
	runRegistration(registry.Register)
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
	want := []string{"register", "register", "registry.Register", "registry.Register", "registry.Register", "register", "converted.Register", "tools.RegisterBuiltins", "bulkRegister", "constructed.MustRegister", "application.registry.Register", "localRegistry.Register", "localRegistrar.Register", "namedLocalRegistrar.Register", "Register", "Register", "Register", "registerTransitively", "runRegistration"}
	if len(calls) != len(want) {
		t.Fatalf("Tool registration calls = %v, want %v", calls, want)
	}
	for index := range want {
		if calls[index] == want[index] {
			continue
		}
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
	types := compositionTypeIndex{
		fields:          make(map[string]map[string]string),
		cleanupMethods:  make(map[string]map[string]bool),
		functionResults: make(map[string][]string),
		namedTypes:      make(map[string]string),
		cleanupParams:   make(map[string]map[int]bool),
		toolParams:      make(map[string]map[int]bool),
		cleanupResults:  make(map[string]map[int]map[string]bool),
		toolResults:     make(map[string]map[int]bool),
		appFunctionKeys: make(map[string]bool),
	}
	for typeName, methods := range featureResourceCleanupMethods {
		types.cleanupMethods[typeName] = methods
	}
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
		indexCleanupAndConstructors(parsed, modulePath+"/internal/app", imports, &types)
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
				if types.fields[typeName] == nil {
					types.fields[typeName] = make(map[string]string)
				}
				for _, field := range structure.Fields.List {
					if len(field.Names) == 0 {
						fieldType := canonicalType(field.Type, imports)
						types.fields[typeName][embeddedPrefix+fieldType] = fieldType
						continue
					}
					for _, name := range field.Names {
						types.fields[typeName][name.Name] = canonicalType(field.Type, imports)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return compositionTypeIndex{}, err
	}
	if !foundApp {
		return compositionTypeIndex{}, fs.ErrNotExist
	}
	if err := indexConcreteFeatureSources(repositoryRootPath(), &types); err != nil {
		return compositionTypeIndex{}, err
	}
	indexLocalCleanupParameters(&types)
	indexLocalResultFlows(&types)
	return types, nil
}

func indexConcreteFeatureSources(root string, types *compositionTypeIndex) error {
	for _, relativeDir := range []string{"internal/hooks", "internal/mcp", "internal/modules", "internal/observable", "internal/skills"} {
		dir := filepath.Join(root, filepath.FromSlash(relativeDir))
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
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
			packageDir, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			packagePath := modulePath + "/" + filepath.ToSlash(packageDir)
			indexCleanupAndConstructors(parsed, packagePath, importPaths(parsed), types)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func indexCleanupAndConstructors(file *ast.File, packagePath string, imports map[string]string, types *compositionTypeIndex) {
	for _, declaration := range file.Decls {
		if general, ok := declaration.(*ast.GenDecl); ok && general.Tok == token.TYPE {
			for _, raw := range general.Specs {
				spec, ok := raw.(*ast.TypeSpec)
				if !ok {
					continue
				}
				typeName := packagePath + "." + spec.Name.Name
				structure, ok := spec.Type.(*ast.StructType)
				if !ok {
					types.namedTypes[typeName] = canonicalTypeInPackage(spec.Type, imports, packagePath)
					continue
				}
				if types.fields[typeName] == nil {
					types.fields[typeName] = make(map[string]string)
				}
				for _, field := range structure.Fields.List {
					if len(field.Names) == 0 {
						fieldType := canonicalTypeInPackage(field.Type, imports, packagePath)
						types.fields[typeName][embeddedPrefix+fieldType] = fieldType
						continue
					}
					for _, name := range field.Names {
						types.fields[typeName][name.Name] = canonicalTypeInPackage(field.Type, imports, packagePath)
					}
				}
			}
			continue
		}
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if packagePath == modulePath+"/internal/app" {
			indexed := indexedAppFunction{
				key:     declaredFunctionKey(function, imports, packagePath),
				decl:    function,
				imports: imports,
			}
			types.appFunctions = append(types.appFunctions, indexed)
			types.appFunctionKeys[indexed.key] = true
			indexLocalFunctionLiterals(indexed, types)
		}
		if function.Recv == nil {
			if results := resultTypes(function.Type.Results, imports, packagePath); len(results) != 0 {
				types.functionResults[packagePath+"."+function.Name.Name] = results
			}
			continue
		}
		if len(function.Recv.List) == 0 {
			continue
		}
		receiverType := canonicalTypeInPackage(function.Recv.List[0].Type, imports, packagePath)
		if results := resultTypes(function.Type.Results, imports, packagePath); len(results) != 0 {
			types.functionResults[receiverType+"."+function.Name.Name] = results
		}
		if packagePath == modulePath+"/internal/app" {
			continue
		}
		if !isFeatureCleanupMethodName(function.Name.Name) {
			continue
		}
		if types.cleanupMethods[receiverType] == nil {
			types.cleanupMethods[receiverType] = make(map[string]bool)
		}
		types.cleanupMethods[receiverType][function.Name.Name] = true
	}
}

func indexLocalFunctionLiterals(parent indexedAppFunction, types *compositionTypeIndex) {
	ast.Inspect(parent.body(), func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for index, left := range value.Lhs {
				name, ok := left.(*ast.Ident)
				if !ok {
					continue
				}
				literal := assignedFunctionLiteral(value.Rhs, index)
				if literal == nil {
					continue
				}
				indexLocalFunctionLiteral(parent, name, literal, types)
			}
		case *ast.DeclStmt:
			general, ok := value.Decl.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				break
			}
			for _, raw := range general.Specs {
				spec := raw.(*ast.ValueSpec)
				for index, name := range spec.Names {
					literal := assignedFunctionLiteral(spec.Values, index)
					if literal != nil {
						indexLocalFunctionLiteral(parent, name, literal, types)
					}
				}
			}
		case *ast.FuncLit:
			return false
		}
		return true
	})
}

func indexLocalFunctionLiteral(parent indexedAppFunction, name *ast.Ident, literal *ast.FuncLit, types *compositionTypeIndex) {
	indexed := indexedAppFunction{
		key:     localFunctionKey(parent.key, name),
		literal: literal,
		imports: parent.imports,
	}
	types.appFunctions = append(types.appFunctions, indexed)
	types.appFunctionKeys[indexed.key] = true
	indexLocalFunctionLiterals(indexed, types)
}

func assignedFunctionLiteral(expressions []ast.Expr, index int) *ast.FuncLit {
	var expression ast.Expr
	switch {
	case len(expressions) == 1 && index == 0:
		expression = expressions[0]
	case len(expressions) > 1 && index < len(expressions):
		expression = expressions[index]
	}
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			break
		}
		expression = parenthesized.X
	}
	literal, _ := expression.(*ast.FuncLit)
	return literal
}

func localFunctionKey(parent string, name *ast.Ident) string {
	return parent + "$func@" + strconv.Itoa(int(name.Pos()))
}

func localFunctionType(parent string, name *ast.Ident, expressions []ast.Expr, index int) string {
	if assignedFunctionLiteral(expressions, index) == nil {
		return ""
	}
	return localFunctionTypePrefix + localFunctionKey(parent, name)
}

func declaredFunctionKey(function *ast.FuncDecl, imports map[string]string, packagePath string) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return packagePath + "." + function.Name.Name
	}
	return canonicalTypeInPackage(function.Recv.List[0].Type, imports, packagePath) + "." + function.Name.Name
}

func indexLocalCleanupParameters(types *compositionTypeIndex) {
	changed := true
	for changed {
		changed = false
		for _, function := range types.appFunctions {
			inferred := inferCleanupParameters(function, *types)
			if types.cleanupParams[function.key] == nil {
				types.cleanupParams[function.key] = make(map[int]bool)
			}
			for index := range inferred {
				if !types.cleanupParams[function.key][index] {
					types.cleanupParams[function.key][index] = true
					changed = true
				}
			}
			inferredTools := inferToolRegistrationParameters(function, *types)
			if types.toolParams[function.key] == nil {
				types.toolParams[function.key] = make(map[int]bool)
			}
			for index := range inferredTools {
				if !types.toolParams[function.key][index] {
					types.toolParams[function.key][index] = true
					changed = true
				}
			}
		}
	}
}

func indexLocalResultFlows(types *compositionTypeIndex) {
	changed := true
	for changed {
		changed = false
		for _, function := range types.appFunctions {
			cleanupResults, toolResults := inferLocalResultFlows(function, *types)
			if types.cleanupResults[function.key] == nil {
				types.cleanupResults[function.key] = make(map[int]map[string]bool)
			}
			for index, paths := range cleanupResults {
				if types.cleanupResults[function.key][index] == nil {
					types.cleanupResults[function.key][index] = make(map[string]bool)
				}
				for path := range paths {
					if !types.cleanupResults[function.key][index][path] {
						types.cleanupResults[function.key][index][path] = true
						changed = true
					}
				}
			}
			if types.toolResults[function.key] == nil {
				types.toolResults[function.key] = make(map[int]bool)
			}
			for index := range toolResults {
				if !types.toolResults[function.key][index] {
					types.toolResults[function.key][index] = true
					changed = true
				}
			}
		}
	}
}

func inferLocalResultFlows(function indexedAppFunction, types compositionTypeIndex) (map[int]map[string]bool, map[int]bool) {
	cleanupResults := make(map[int]map[string]bool)
	toolResults := make(map[int]bool)
	values := namedValueTypes(function.receiver(), function.imports)
	for name, typeName := range namedValueTypes(function.parameters(), function.imports) {
		values[name] = typeName
	}
	for name, typeName := range namedValueTypes(function.results(), function.imports) {
		values[name] = typeName
	}
	resources := namedCleanupValues(function.receiver(), function.imports, types)
	for name, paths := range namedCleanupValues(function.parameters(), function.imports, types) {
		resources[name] = paths
	}
	for name, paths := range namedCleanupValues(function.results(), function.imports, types) {
		resources[name] = paths
	}
	recordResult := func(index int, expression ast.Expr) {
		if paths := cleanupPathsForExpression(expression, function.imports, values, resources, types); paths != nil {
			if cleanupResults[index] == nil {
				cleanupResults[index] = make(map[string]bool)
			}
			for path := range paths {
				cleanupResults[index][path] = true
			}
		}
		if isToolRegistryExpression(expression, function.imports, values, types) {
			toolResults[index] = true
		}
	}
	ast.Inspect(function.body(), func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for index, left := range value.Lhs {
				name, ok := left.(*ast.Ident)
				if !ok {
					continue
				}
				key := bindingKey(name)
				if paths := assignedCleanupPaths(value.Rhs, index, function.imports, values, resources, types); paths != nil {
					resources[key] = paths
				}
				typeName := assignedToolExpressionType(value.Rhs, index, function.imports, values, types)
				if localType := localFunctionType(function.key, name, value.Rhs, index); localType != "" {
					typeName = localType
				}
				setMayValueType(values, key, typeName, types)
			}
		case *ast.DeclStmt:
			general, ok := value.Decl.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				break
			}
			for _, raw := range general.Specs {
				spec := raw.(*ast.ValueSpec)
				for index, name := range spec.Names {
					key := bindingKey(name)
					paths := cleanupPathsForType(canonicalType(spec.Type, function.imports), types, nil)
					typeName := canonicalType(spec.Type, function.imports)
					if len(spec.Values) != 0 {
						paths = assignedCleanupPaths(spec.Values, index, function.imports, values, resources, types)
						typeName = assignedToolExpressionType(spec.Values, index, function.imports, values, types)
					}
					if paths != nil {
						resources[key] = paths
					}
					if localType := localFunctionType(function.key, name, spec.Values, index); localType != "" {
						typeName = localType
					}
					setMayValueType(values, key, typeName, types)
				}
			}
		case *ast.RangeStmt:
			keyType, valueType, ok := rangeTypes(expressionType(value.X, function.imports, values, types), types)
			if !ok {
				break
			}
			if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" {
				key := bindingKey(name)
				values[key] = keyType
				if paths := cleanupPathsForType(keyType, types, nil); paths != nil {
					resources[key] = paths
				}
			}
			if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" {
				key := bindingKey(name)
				values[key] = valueType
				if paths := cleanupPathsForType(valueType, types, nil); paths != nil {
					resources[key] = paths
				}
			}
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(value.Results) == 0 {
				for index, name := range namedResultIdentifiers(function.results()) {
					if name != nil {
						recordResult(index, name)
					}
				}
				break
			}
			for index, expression := range value.Results {
				recordResult(index, expression)
			}
		}
		return true
	})
	return cleanupResults, toolResults
}

func inferCleanupParameters(function indexedAppFunction, types compositionTypeIndex) map[int]bool {
	cleaned := make(map[int]bool)
	origins := parameterOrigins(function.parameters())
	values := namedValueTypes(function.receiver(), function.imports)
	for name, typeName := range namedValueTypes(function.parameters(), function.imports) {
		values[name] = typeName
	}
	ast.Inspect(function.body(), func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for index, left := range value.Lhs {
				name, ok := left.(*ast.Ident)
				if !ok {
					continue
				}
				key := bindingKey(name)
				if index < len(value.Rhs) {
					mergeBindingOrigins(origins, key, originsForExpression(value.Rhs[index], origins))
				}
				typeName := assignedExpressionType(value.Rhs, index, function.imports, values, types)
				if localType := localFunctionType(function.key, name, value.Rhs, index); localType != "" {
					typeName = localType
				}
				setMayValueType(values, key, typeName, types)
			}
		case *ast.DeclStmt:
			general, ok := value.Decl.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				break
			}
			for _, raw := range general.Specs {
				spec := raw.(*ast.ValueSpec)
				for index, name := range spec.Names {
					key := bindingKey(name)
					if index < len(spec.Values) {
						mergeBindingOrigins(origins, key, originsForExpression(spec.Values[index], origins))
					}
					typeName := assignedExpressionType(spec.Values, index, function.imports, values, types)
					if localType := localFunctionType(function.key, name, spec.Values, index); localType != "" {
						typeName = localType
					}
					setMayValueType(values, key, typeName, types)
				}
			}
		case *ast.RangeStmt:
			origin := originsForExpression(value.X, origins)
			keyType, valueType, _ := rangeTypes(expressionType(value.X, function.imports, values, types), types)
			if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" {
				key := bindingKey(name)
				mergeBindingOrigins(origins, key, origin)
				values[key] = keyType
			}
			if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" {
				key := bindingKey(name)
				mergeBindingOrigins(origins, key, origin)
				values[key] = valueType
			}
		case *ast.CallExpr:
			mergeOrigins(cleaned, callableOrigins(value.Fun, origins))
			if selector, ok := value.Fun.(*ast.SelectorExpr); ok && isFeatureCleanupMethodName(selector.Sel.Name) {
				if isFeatureCleanupMethodExpression(selector, function.imports, types) && len(value.Args) != 0 {
					mergeOrigins(cleaned, originsForExpression(value.Args[0], origins))
				} else {
					mergeOrigins(cleaned, originsForExpression(selector.X, origins))
				}
			}
			callee := calledFunctionKey(value.Fun, function.imports, values, types)
			for index := range types.cleanupParams[callee] {
				if index < len(value.Args) {
					mergeOrigins(cleaned, originsForExpression(value.Args[index], origins))
				}
			}
		case *ast.FuncLit:
			return false
		}
		return true
	})
	return cleaned
}

func inferToolRegistrationParameters(function indexedAppFunction, types compositionTypeIndex) map[int]bool {
	registered := make(map[int]bool)
	origins := parameterOrigins(function.parameters())
	values := namedValueTypes(function.receiver(), function.imports)
	for name, typeName := range namedValueTypes(function.parameters(), function.imports) {
		values[name] = typeName
	}
	ast.Inspect(function.body(), func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for index, left := range value.Lhs {
				name, ok := left.(*ast.Ident)
				if !ok {
					continue
				}
				key := bindingKey(name)
				if index < len(value.Rhs) {
					mergeBindingOrigins(origins, key, originsForExpression(value.Rhs[index], origins))
				}
				typeName := assignedExpressionType(value.Rhs, index, function.imports, values, types)
				if localType := localFunctionType(function.key, name, value.Rhs, index); localType != "" {
					typeName = localType
				}
				setMayValueType(values, key, typeName, types)
			}
		case *ast.DeclStmt:
			general, ok := value.Decl.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				break
			}
			for _, raw := range general.Specs {
				spec := raw.(*ast.ValueSpec)
				for index, name := range spec.Names {
					key := bindingKey(name)
					if index < len(spec.Values) {
						mergeBindingOrigins(origins, key, originsForExpression(spec.Values[index], origins))
					}
					typeName := assignedExpressionType(spec.Values, index, function.imports, values, types)
					if localType := localFunctionType(function.key, name, spec.Values, index); localType != "" {
						typeName = localType
					}
					setMayValueType(values, key, typeName, types)
				}
			}
		case *ast.RangeStmt:
			origin := originsForExpression(value.X, origins)
			keyType, valueType, _ := rangeTypes(expressionType(value.X, function.imports, values, types), types)
			if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" {
				key := bindingKey(name)
				mergeBindingOrigins(origins, key, origin)
				values[key] = keyType
			}
			if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" {
				key := bindingKey(name)
				mergeBindingOrigins(origins, key, origin)
				values[key] = valueType
			}
		case *ast.CallExpr:
			mergeOrigins(registered, callableOrigins(value.Fun, origins))
			if selector, ok := value.Fun.(*ast.SelectorExpr); ok && isToolRegistrationName(selector.Sel.Name) {
				if qualifier, ok := selector.X.(*ast.Ident); ok && function.imports[qualifier.Name] == modulePath+"/internal/tools" {
					if len(value.Args) != 0 {
						mergeOrigins(registered, originsForExpression(value.Args[0], origins))
					}
				} else {
					mergeOrigins(registered, originsForExpression(selector.X, origins))
				}
			}
			callee := calledFunctionKey(value.Fun, function.imports, values, types)
			for index := range types.toolParams[callee] {
				if index < len(value.Args) {
					mergeOrigins(registered, originsForExpression(value.Args[index], origins))
				}
			}
		case *ast.FuncLit:
			return false
		}
		return true
	})
	return registered
}

func parameterOrigins(fields *ast.FieldList) map[string]map[int]bool {
	origins := make(map[string]map[int]bool)
	if fields == nil {
		return origins
	}
	index := 0
	for _, field := range fields.List {
		for _, name := range field.Names {
			origins[bindingKey(name)] = map[int]bool{index: true}
			index++
		}
		if len(field.Names) == 0 {
			index++
		}
	}
	return origins
}

func originsForExpression(expression ast.Expr, origins map[string]map[int]bool) map[int]bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return origins[bindingKey(value)]
	case *ast.ParenExpr:
		return originsForExpression(value.X, origins)
	case *ast.SelectorExpr:
		return originsForExpression(value.X, origins)
	case *ast.UnaryExpr:
		return originsForExpression(value.X, origins)
	default:
		return nil
	}
}

func callableOrigins(expression ast.Expr, origins map[string]map[int]bool) map[int]bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return origins[bindingKey(value)]
	case *ast.ParenExpr:
		return callableOrigins(value.X, origins)
	default:
		return nil
	}
}

func mergeOrigins(destination, source map[int]bool) {
	for index := range source {
		destination[index] = true
	}
}

func mergeBindingOrigins(bindings map[string]map[int]bool, key string, source map[int]bool) {
	if bindings[key] == nil {
		bindings[key] = make(map[int]bool)
	}
	mergeOrigins(bindings[key], source)
}

func resultTypes(results *ast.FieldList, imports map[string]string, packagePath string) []string {
	if results == nil {
		return nil
	}
	var resolved []string
	for _, field := range results.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		typeName := canonicalTypeInPackage(field.Type, imports, packagePath)
		for range count {
			resolved = append(resolved, typeName)
		}
	}
	return resolved
}

func isFeatureCleanupMethodName(name string) bool {
	switch name {
	case "Close", "StartClose", "StopAll", "WaitClose", "CloseRuntime", "QuiesceRuntime", "CloseSession", "QuiesceSession":
		return true
	default:
		return false
	}
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
		functionKey := declaredFunctionKey(function, imports, modulePath+"/internal/app")
		values := namedValueTypes(function.Recv, imports)
		for name, typeName := range namedValueTypes(function.Type.Params, imports) {
			values[name] = typeName
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
					if !ok {
						continue
					}
					key := bindingKey(name)
					if paths := assignedCleanupPaths(value.Rhs, index, imports, values, resources, types); paths != nil {
						resources[key] = paths
					}
					typeName := assignedExpressionType(value.Rhs, index, imports, values, types)
					if localType := localFunctionType(functionKey, name, value.Rhs, index); localType != "" {
						typeName = localType
					}
					setMayValueType(values, key, typeName, types)
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
						key := bindingKey(name)
						if len(spec.Values) != 0 {
							paths = assignedCleanupPaths(spec.Values, index, imports, values, resources, types)
						}
						if paths != nil {
							resources[key] = paths
						}
						typeName := canonicalType(spec.Type, imports)
						if len(spec.Values) != 0 {
							typeName = assignedExpressionType(spec.Values, index, imports, values, types)
						}
						if localType := localFunctionType(functionKey, name, spec.Values, index); localType != "" {
							typeName = localType
						}
						setMayValueType(values, key, typeName, types)
					}
				}
			case *ast.RangeStmt:
				keyType, valueType, ok := rangeTypes(expressionType(value.X, imports, values, types), types)
				if !ok {
					break
				}
				if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" {
					key := bindingKey(name)
					values[key] = keyType
					if paths := cleanupPathsForType(keyType, types, nil); paths != nil {
						resources[key] = paths
					}
				}
				if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" {
					key := bindingKey(name)
					values[key] = valueType
					if paths := cleanupPathsForType(valueType, types, nil); paths != nil {
						resources[key] = paths
					}
				}
			case *ast.CallExpr:
				callee := calledFunctionKey(value.Fun, imports, values, types)
				for index := range types.cleanupParams[callee] {
					if index < len(value.Args) && cleanupPathsForExpression(value.Args[index], imports, values, resources, types) != nil {
						report(value, selectorChain(value.Fun))
						break
					}
				}
				_, selectorCall := value.Fun.(*ast.SelectorExpr)
				if !selectorCall && cleanupPathsForExpression(value.Fun, imports, values, resources, types)[callableCleanupPath] {
					report(value, selectorChain(value.Fun))
				}
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if !ok {
					break
				}
				if isFeatureCleanupMethodExpression(selector, imports, types) && len(value.Args) != 0 {
					if cleanupPathsForExpression(value.Args[0], imports, values, resources, types) != nil {
						report(value, selectorChain(selector))
					}
					break
				}
				paths := cleanupPathsForExpression(selector.X, imports, values, resources, types)
				if paths[selector.Sel.Name] {
					report(value, selectorChain(selector))
				}
			case *ast.FuncLit:
				return false
			}
			return true
		})
	}
}

func ownsFeatureCleanup(receiver *ast.FieldList, imports map[string]string) bool {
	for _, typeName := range namedValueTypes(receiver, imports) {
		if appFeatureCleanupOwners[typeName] {
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
		functionKey := declaredFunctionKey(function, imports, modulePath+"/internal/app")
		values := namedValueTypes(function.Recv, imports)
		for name, typeName := range namedValueTypes(function.Type.Params, imports) {
			values[name] = typeName
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.AssignStmt:
				for index, left := range value.Lhs {
					name, ok := left.(*ast.Ident)
					if !ok {
						continue
					}
					key := bindingKey(name)
					typeName := assignedToolExpressionType(value.Rhs, index, imports, values, types)
					if localType := localFunctionType(functionKey, name, value.Rhs, index); localType != "" {
						typeName = localType
					}
					setMayValueType(values, key, typeName, types)
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
						if len(spec.Values) != 0 {
							typeName = assignedToolExpressionType(spec.Values, index, imports, values, types)
						}
						if localType := localFunctionType(functionKey, name, spec.Values, index); localType != "" {
							typeName = localType
						}
						setMayValueType(values, bindingKey(name), typeName, types)
					}
				}
			case *ast.RangeStmt:
				keyType, valueType, ok := rangeTypes(expressionType(value.X, imports, values, types), types)
				if !ok {
					break
				}
				if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" {
					values[bindingKey(name)] = keyType
				}
				if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" {
					values[bindingKey(name)] = valueType
				}
			case *ast.CallExpr:
				callee := calledFunctionKey(value.Fun, imports, values, types)
				for index := range types.toolParams[callee] {
					if index >= len(value.Args) {
						continue
					}
					argument := value.Args[index]
					if isToolRegistryExpression(argument, imports, values, types) || isToolRegistrationValueExpression(argument, imports, values, types) {
						report(value, selectorChain(value.Fun))
						break
					}
				}
				if isToolRegistrationCall(value, imports, values, types) {
					report(value, selectorChain(value.Fun))
				}
			case *ast.FuncLit:
				return false
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
	return canonicalTypeInPackage(expression, imports, modulePath+"/internal/app")
}

func canonicalTypeInPackage(expression ast.Expr, imports map[string]string, packagePath string) string {
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
		case *ast.ArrayType:
			return sliceTypePrefix + canonicalTypeInPackage(value.Elt, imports, packagePath)
		case *ast.MapType:
			return mapTypePrefix + canonicalTypeInPackage(value.Key, imports, packagePath) + typeSeparator + canonicalTypeInPackage(value.Value, imports, packagePath)
		case *ast.Ident:
			return packagePath + "." + value.Name
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
			values[bindingKey(name)] = typeName
		}
	}
	return values
}

func namedResultIdentifiers(fields *ast.FieldList) []*ast.Ident {
	if fields == nil {
		return nil
	}
	var names []*ast.Ident
	for _, field := range fields.List {
		if len(field.Names) == 0 {
			names = append(names, nil)
			continue
		}
		names = append(names, field.Names...)
	}
	return names
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
			resources[bindingKey(name)] = paths
		}
	}
	return resources
}

func bindingKey(identifier *ast.Ident) string {
	if identifier.Obj == nil {
		return identifier.Name
	}
	return fmt.Sprintf("%s@%p", identifier.Name, identifier.Obj)
}

func setMayValueType(values map[string]string, key, typeName string, types compositionTypeIndex) {
	if typeName == "" {
		return
	}
	existing := values[key]
	if isTrackedMayType(existing, types) && !isTrackedMayType(typeName, types) {
		return
	}
	values[key] = typeName
}

func isTrackedMayType(typeName string, types compositionTypeIndex) bool {
	return resolveNamedType(typeName, types) == modulePath+"/internal/tools.Registry" ||
		typeName == toolRegistrationCallableType ||
		strings.HasPrefix(typeName, localFunctionTypePrefix)
}

func featureCleanupMethods(typeName string, types compositionTypeIndex) map[string]bool {
	if methods := types.cleanupMethods[typeName]; methods != nil {
		return methods
	}
	const moduleType = ".Module"
	if strings.HasSuffix(typeName, moduleType) && isConcreteFeatureImport(strings.TrimSuffix(typeName, moduleType)) {
		return featureModuleCleanupMethods
	}
	return nil
}

func cleanupPathsForType(typeName string, types compositionTypeIndex, visiting map[string]bool) map[string]bool {
	typeName = resolveNamedType(typeName, types)
	if keyType, valueType, ok := rangeTypes(typeName, types); ok {
		_ = keyType
		return cleanupPathsForType(valueType, types, visiting)
	}
	if typeName == modulePath+"/internal/app.App" && len(visiting) != 0 {
		return nil
	}
	if methods := featureCleanupMethods(typeName, types); methods != nil {
		return methods
	}
	fields := types.fields[typeName]
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
			if strings.HasPrefix(field, embeddedPrefix) {
				paths[path] = true
			} else {
				paths[field+"."+path] = true
			}
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return paths
}

func resolveNamedType(typeName string, types compositionTypeIndex) string {
	seen := make(map[string]bool)
	for typeName != "" && !seen[typeName] {
		seen[typeName] = true
		underlying := types.namedTypes[typeName]
		if underlying == "" {
			return typeName
		}
		typeName = underlying
	}
	return typeName
}

func rangeTypes(typeName string, types compositionTypeIndex) (string, string, bool) {
	typeName = resolveNamedType(typeName, types)
	if strings.HasPrefix(typeName, sliceTypePrefix) {
		return "", strings.TrimPrefix(typeName, sliceTypePrefix), true
	}
	if strings.HasPrefix(typeName, mapTypePrefix) {
		parts := strings.SplitN(strings.TrimPrefix(typeName, mapTypePrefix), typeSeparator, 2)
		if len(parts) == 2 {
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

func cleanupPathsForExpression(expression ast.Expr, imports map[string]string, values map[string]string, resources map[string]map[string]bool, types compositionTypeIndex) map[string]bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return resources[bindingKey(value)]
	case *ast.ParenExpr:
		return cleanupPathsForExpression(value.X, imports, values, resources, types)
	case *ast.SelectorExpr:
		if isFeatureCleanupMethodExpression(value, imports, types) {
			return map[string]bool{callableCleanupPath: true}
		}
		paths := cleanupPathsForExpression(value.X, imports, values, resources, types)
		prefix := value.Sel.Name + "."
		trimmed := make(map[string]bool)
		if paths[value.Sel.Name] {
			trimmed[callableCleanupPath] = true
		}
		for path := range paths {
			if strings.HasPrefix(path, prefix) {
				trimmed[strings.TrimPrefix(path, prefix)] = true
			}
		}
		if len(trimmed) != 0 {
			return trimmed
		}
	case *ast.IndexExpr:
		if _, valueType, ok := rangeTypes(expressionType(value.X, imports, values, types), types); ok {
			return cleanupPathsForType(valueType, types, nil)
		}
		return cleanupPathsForExpression(value.X, imports, values, resources, types)
	case *ast.CallExpr:
		callee := calledFunctionKey(value.Fun, imports, values, types)
		if paths := types.cleanupResults[callee][0]; paths != nil {
			return paths
		}
		if results := expressionResultTypes(value, imports, values, types); len(results) != 0 {
			return cleanupPathsForType(results[0], types, nil)
		}
		if len(value.Args) == 1 {
			return cleanupPathsForExpression(value.Args[0], imports, values, resources, types)
		}
	case *ast.UnaryExpr:
		if value.Op == token.AND {
			return cleanupPathsForExpression(value.X, imports, values, resources, types)
		}
	case *ast.CompositeLit:
		return cleanupPathsForType(canonicalType(value.Type, imports), types, nil)
	}
	return nil
}

func isFeatureCleanupMethodExpression(selector *ast.SelectorExpr, imports map[string]string, types compositionTypeIndex) bool {
	typeName := canonicalType(selector.X, imports)
	return typeName != "" && featureCleanupMethods(typeName, types)[selector.Sel.Name]
}

func assignedCleanupPaths(expressions []ast.Expr, index int, imports map[string]string, values map[string]string, resources map[string]map[string]bool, types compositionTypeIndex) map[string]bool {
	if len(expressions) == 1 {
		if call, ok := expressions[0].(*ast.CallExpr); ok {
			callee := calledFunctionKey(call.Fun, imports, values, types)
			if paths := types.cleanupResults[callee][index]; paths != nil {
				return paths
			}
		}
		if results := expressionResultTypes(expressions[0], imports, values, types); index < len(results) {
			return cleanupPathsForType(results[index], types, nil)
		}
		if index == 0 {
			return cleanupPathsForExpression(expressions[0], imports, values, resources, types)
		}
		return nil
	}
	if index >= len(expressions) {
		return nil
	}
	return cleanupPathsForExpression(expressions[index], imports, values, resources, types)
}

func expressionResultTypes(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) []string {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		if typeName := expressionType(expression, imports, values, types); typeName != "" {
			return []string{typeName}
		}
		return nil
	}
	if results := types.functionResults[calledFunctionKey(call.Fun, imports, values, types)]; len(results) != 0 {
		return results
	}
	if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "append" && len(call.Args) != 0 {
		return []string{expressionType(call.Args[0], imports, values, types)}
	}
	if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "new" && len(call.Args) == 1 {
		return []string{canonicalType(call.Args[0], imports)}
	}
	if typeName := canonicalType(call.Fun, imports); cleanupPathsForType(typeName, types, nil) != nil {
		return []string{typeName}
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return nil
	}
	switch imports[qualifier.Name] {
	case modulePath + "/internal/tools":
		if selector.Sel.Name == "NewRegistry" || selector.Sel.Name == "NewRegistryWithOptions" {
			return []string{modulePath + "/internal/tools.Registry"}
		}
	case modulePath + "/internal/runtime/module":
		if selector.Sel.Name == "BuildToolRegistry" {
			return []string{modulePath + "/internal/tools.Registry", modulePath + "/internal/app.error"}
		}
	}
	return nil
}

func assignedExpressionType(expressions []ast.Expr, index int, imports map[string]string, values map[string]string, types compositionTypeIndex) string {
	if len(expressions) == 1 {
		results := expressionResultTypes(expressions[0], imports, values, types)
		if index < len(results) {
			return results[index]
		}
		return ""
	}
	if index >= len(expressions) {
		return ""
	}
	return expressionType(expressions[index], imports, values, types)
}

func assignedToolExpressionType(expressions []ast.Expr, index int, imports map[string]string, values map[string]string, types compositionTypeIndex) string {
	var expression ast.Expr
	switch {
	case len(expressions) == 1 && index == 0:
		expression = expressions[0]
	case len(expressions) > 1 && index < len(expressions):
		expression = expressions[index]
	}
	if expression != nil && isToolRegistrationCallableExpression(expression, imports, values, types) {
		return toolRegistrationCallableType
	}
	if expression != nil && isToolRegistryExpression(expression, imports, values, types) {
		return modulePath + "/internal/tools.Registry"
	}
	if call, ok := expression.(*ast.CallExpr); ok && len(call.Args) == 1 && isToolRegistryExpression(call.Args[0], imports, values, types) {
		return modulePath + "/internal/tools.Registry"
	}
	return assignedExpressionType(expressions, index, imports, values, types)
}

func calledFunctionKey(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) string {
	switch function := expression.(type) {
	case *ast.Ident:
		if typeName := values[bindingKey(function)]; strings.HasPrefix(typeName, localFunctionTypePrefix) {
			return strings.TrimPrefix(typeName, localFunctionTypePrefix)
		}
		packagePath := imports["."]
		if packagePath == "" {
			packagePath = modulePath + "/internal/app"
		}
		return packagePath + "." + function.Name
	case *ast.SelectorExpr:
		if qualifier, ok := function.X.(*ast.Ident); ok {
			if packagePath := imports[qualifier.Name]; packagePath != "" {
				return packagePath + "." + function.Sel.Name
			}
		}
		return expressionType(function.X, imports, values, types) + "." + function.Sel.Name
	default:
		return ""
	}
}

func expressionType(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) string {
	switch value := expression.(type) {
	case *ast.Ident:
		if typeName := values[bindingKey(value)]; typeName != "" {
			return typeName
		}
		functionKey := modulePath + "/internal/app." + value.Name
		if types.appFunctionKeys[functionKey] {
			return localFunctionTypePrefix + functionKey
		}
		return ""
	case *ast.ParenExpr:
		return expressionType(value.X, imports, values, types)
	case *ast.StarExpr:
		return expressionType(value.X, imports, values, types)
	case *ast.UnaryExpr:
		if value.Op == token.AND {
			return expressionType(value.X, imports, values, types)
		}
	case *ast.CompositeLit:
		return canonicalType(value.Type, imports)
	case *ast.SelectorExpr:
		receiverType := expressionType(value.X, imports, values, types)
		return compositionFieldType(receiverType, value.Sel.Name, types, nil)
	case *ast.IndexExpr:
		_, valueType, _ := rangeTypes(expressionType(value.X, imports, values, types), types)
		return valueType
	case *ast.CallExpr:
		callee := calledFunctionKey(value.Fun, imports, values, types)
		if types.toolResults[callee][0] {
			return modulePath + "/internal/tools.Registry"
		}
		if results := expressionResultTypes(value, imports, values, types); len(results) != 0 {
			return results[0]
		}
	}
	return ""
}

func compositionFieldType(typeName, fieldName string, types compositionTypeIndex, visiting map[string]bool) string {
	if visiting[typeName] {
		return ""
	}
	if visiting == nil {
		visiting = make(map[string]bool)
	}
	visiting[typeName] = true
	defer delete(visiting, typeName)
	fields := types.fields[typeName]
	if fieldType := fields[fieldName]; fieldType != "" {
		return fieldType
	}
	for field, embeddedType := range fields {
		if !strings.HasPrefix(field, embeddedPrefix) {
			continue
		}
		if fieldType := compositionFieldType(embeddedType, fieldName, types, visiting); fieldType != "" {
			return fieldType
		}
	}
	return ""
}

func isToolRegistrationCall(call *ast.CallExpr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	const registryType = modulePath + "/internal/tools.Registry"
	switch function := call.Fun.(type) {
	case *ast.Ident:
		return values[bindingKey(function)] == toolRegistrationCallableType || imports["."] == modulePath+"/internal/tools" && isToolRegistrationName(function.Name)
	case *ast.SelectorExpr:
		if qualifier, ok := function.X.(*ast.Ident); ok && imports[qualifier.Name] == modulePath+"/internal/tools" {
			return isToolRegistrationName(function.Sel.Name)
		}
		return resolveNamedType(expressionType(function.X, imports, values, types), types) == registryType && isToolRegistrationName(function.Sel.Name)
	default:
		return false
	}
}

func isToolRegistryExpression(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	const registryType = modulePath + "/internal/tools.Registry"
	if call, ok := expression.(*ast.CallExpr); ok {
		callee := calledFunctionKey(call.Fun, imports, values, types)
		if types.toolResults[callee][0] {
			return true
		}
	}
	if resolveNamedType(expressionType(expression, imports, values, types), types) == registryType {
		return true
	}
	results := expressionResultTypes(expression, imports, values, types)
	return len(results) != 0 && resolveNamedType(results[0], types) == registryType
}

func isToolRegistrationValueExpression(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || !isToolRegistrationName(selector.Sel.Name) {
		return false
	}
	if qualifier, ok := selector.X.(*ast.Ident); ok && imports[qualifier.Name] == modulePath+"/internal/tools" {
		return true
	}
	return isToolRegistryExpression(selector.X, imports, values, types)
}

func isToolRegistrationCallableExpression(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return values[bindingKey(value)] == toolRegistrationCallableType
	case *ast.ParenExpr:
		return isToolRegistrationCallableExpression(value.X, imports, values, types)
	default:
		return isToolRegistrationValueExpression(expression, imports, values, types)
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
	case *ast.CallExpr:
		return selectorChain(value.Fun)
	default:
		return ""
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root := repositoryRootPath()
	if root == "" {
		t.Fatal("resolve boundary test source path")
	}
	return root
}

func repositoryRootPath() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
