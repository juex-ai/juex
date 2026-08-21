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
const toolRegistryCollectionType = "$tool-registry-collection"
const toolRegistryMapKeyCollectionType = "$tool-registry-map-key-collection"
const toolRegistryMapKeyValueCollectionType = "$tool-registry-map-key-value-collection"
const localFunctionTypePrefix = "$local-function:"
const genericTypePrefix = "$generic:"
const genericTypeSeparator = "\x01"
const receiverParameterIndex = -1
const packageFunctionScopeKey = modulePath + "/internal/app.$package"

const (
	sliceTypePrefix   = "$slice:"
	mapTypePrefix     = "$map:"
	channelTypePrefix = "$channel:"
	typeSeparator     = "\x00"
	embeddedPrefix    = "$embedded:"
	mapKeyPathPrefix  = "$map-key:"
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
	fields           map[string]map[string]string
	fieldOrder       map[string][]string
	cleanupMethods   map[string]map[string]bool
	functionResults  map[string][]string
	namedTypes       map[string]string
	typeParameters   map[string][]string
	cleanupParams    map[string]map[int]bool
	toolParams       map[string]map[int]bool
	cleanupResults   map[string]map[int]map[string]bool
	toolResults      map[string]map[int]bool
	toolCallResults  map[string]map[int]bool
	toolCallPaths    map[string]map[int]map[string]bool
	resultParams     map[string]map[int]map[int]bool
	resultParamPaths map[string]map[int]map[int]map[string]bool
	parameterWrites  map[string]map[int]map[int]map[string]bool
	variadicParams   map[string]int
	appFunctionKeys  map[string]bool
	appFunctions     []indexedAppFunction
	packageValues    map[string]string
	packageResources map[string]map[string]bool
	interfaceMethods map[string]methodShape
	interfaceEmbeds  map[string][]string
	concreteMethods  map[string]methodShape
	methodImpls      map[string][]string
}

type indexedAppSource struct {
	file    *ast.File
	imports map[string]string
}

type methodShape struct {
	parameters []string
	results    []string
	variadic   bool
}

type indexedAppFunction struct {
	key         string
	decl        *ast.FuncDecl
	literal     *ast.FuncLit
	imports     map[string]string
	seedOrigins map[string]map[int]bool
	seedValues  map[string]string
	captureOnly bool
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
import (
	"github.com/juex-ai/juex/internal/mcp"
)
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
import (
	"maps"
	"slices"
	"github.com/juex-ai/juex/internal/mcp"
)
type App struct {
	manager *mcp.Manager
	resources genericResources[*mcp.Manager]
	holder genericHolder[*mcp.Manager]
}
type closer interface { Close() error }
type cleanupFunc func() error
type cleanupCallbacks struct{ cleanup cleanupFunc }
type cleanupConsumer func(closer)
type cleanupConsumers struct{ cleanup cleanupConsumer }
type closerList []closer
type ownedHolder struct{ resource closer }
type nestedOwnedHolder struct{ owned ownedHolder }
type genericResources[T any] []T
type genericHolder[T any] struct{ resource T }
type cleanupRunner struct{ resource closer }
var packageManager *mcp.Manager
var packageCleanup = func(resource closer) { _ = resource.Close() }
func cleanupFromPackage() { _ = packageManager.Close() }
func cleanupFromPackageCallback(application *App) { packageCleanup(application.manager) }
func (runner cleanupRunner) Run() { _ = runner.resource.Close() }
func cleanupFromReceiver(application *App) {
	cleanupRunner{resource: application.manager}.Run()
}
func cleanupFromReceiverExpression(application *App) {
	cleanupRunner.Run(cleanupRunner{resource: application.manager})
}
func closeOwned(resource closer) { _ = resource.Close() }
func closeTransitively(resource closer) { closeOwned(resource) }
func runCleanup(cleanup cleanupFunc) { _ = cleanup() }
func owned(application *App) closer { return application.manager }
func namedOwned(application *App) (resource closer) {
	resource = application.manager
	return
}
func identityOwned(resource closer) closer { return resource }
func cleanupFromIdentity(application *App) {
	_ = identityOwned(application.manager).Close()
}
func cleanupGeneric[T closer](resource T) { _ = resource.Close() }
func cleanupFromGeneric(application *App) {
	cleanupGeneric[*mcp.Manager](application.manager)
}
func ownedPair(application *App) (error, closer) { return nil, application.manager }
func ownedRelay(application *App) (error, closer) { return ownedPair(application) }
func cleanupFromRelay(application *App) {
	_, resource := ownedRelay(application)
	_ = resource.Close()
}
func cleanupFromLiteral(application *App) {
	cleanup := func(resource closer) { _ = resource.Close() }
	cleanup(application.manager)
}
func cleanupThroughAlias(application *App) {
	cleanup := closeOwned
	cleanup(application.manager)
}
func cleanupThroughParentheses(application *App) {
	(closeOwned)(application.manager)
}
func cleanupFromIIFE(application *App) {
	func() { _ = application.manager.Close() }()
	func(resource closer) { _ = resource.Close() }(application.manager)
}
func withResource(resource closer, callback func(closer)) { callback(resource) }
func cleanupFromCallback(application *App) {
	withResource(application.manager, func(resource closer) { _ = resource.Close() })
}
func cleanupFromAssertion(application *App) {
	resource, _ := any(application.manager).(closer)
	_ = resource.Close()
}
func cleanupFromDereference(application *App) {
	manager := application.manager
	_ = (*manager).Close()
}
func cleanupFromInterfaceCollection(application *App) {
	resources := []closer{application.manager}
	_ = resources[0].Close()
	for _, resource := range resources {
		_ = resource.Close()
	}
	window := resources[:]
	_ = window[0].Close()
}
func cleanupFromAppend(application *App) {
	var resources []closer
	resources = append(resources, application.manager)
	_ = resources[0].Close()
}
func cleanupFromHolder(application *App) {
	owned := ownedHolder{resource: application.manager}
	_ = owned.resource.Close()
}
func cleanupFromPositionalHolder(application *App) {
	owned := ownedHolder{application.manager}
	_ = owned.resource.Close()
}
func wrapOwned(resource closer) ownedHolder { return ownedHolder{resource} }
func cleanupFromWrappedHolder(application *App) {
	_ = wrapOwned(application.manager).resource.Close()
}
func wrapNestedOwned(resource closer) nestedOwnedHolder {
	return nestedOwnedHolder{owned: ownedHolder{resource: resource}}
}
func cleanupFromNestedWrappedHolder(application *App) {
	_ = wrapNestedOwned(application.manager).owned.resource.Close()
}
func cleanupFromAssignedHolder(application *App) {
	owned := ownedHolder{}
	owned.resource = application.manager
	_ = owned.resource.Close()
}
func cleanupFromPointerAssignment(application *App) {
	owned := &ownedHolder{}
	(*owned).resource = application.manager
	_ = (*owned).resource.Close()
}
func cleanupFromAnonymousHolder(application *App) {
	owned := struct{ resource closer }{resource: application.manager}
	_ = owned.resource.Close()
}
func cleanupFromChannel(application *App) {
	resources := make(chan closer, 1)
	resources <- application.manager
	_ = (<-resources).Close()
}
func cleanupFromChannelAlias(application *App) {
	resources := make(chan closer, 1)
	alias := resources
	alias <- application.manager
	_ = (<-resources).Close()
}
func cleanupFromCallbackField(application *App) {
	callbacks := cleanupCallbacks{}
	callbacks.cleanup = application.manager.Close
	_ = callbacks.cleanup()
}
func cleanupFromLiteralCallbackField(application *App) {
	callbacks := cleanupConsumers{}
	callbacks.cleanup = func(resource closer) { _ = resource.Close() }
	callbacks.cleanup(application.manager)
}
func cleanupFromLiteralCallbackIndex(application *App) {
	callbacks := []cleanupConsumer{nil}
	callbacks[0] = func(resource closer) { _ = resource.Close() }
	callbacks[0](application.manager)
}
func cleanupFromKeyedCompositeCallback(application *App) {
	callbacks := cleanupConsumers{cleanup: func(resource closer) { _ = resource.Close() }}
	callbacks.cleanup(application.manager)
}
func cleanupFromPositionalCompositeCallback(application *App) {
	callbacks := cleanupConsumers{func(resource closer) { _ = resource.Close() }}
	callbacks.cleanup(application.manager)
}
func cleanupFromIndexedCompositeCallback(application *App) {
	callbacks := []cleanupConsumer{func(resource closer) { _ = resource.Close() }}
	callbacks[0](application.manager)
}
func wrapCleanup(resource closer) cleanupCallbacks {
	return cleanupCallbacks{cleanup: func() error { return resource.Close() }}
}
func cleanupFromReturnedComposite(application *App) {
	_ = wrapCleanup(application.manager).cleanup()
}
func cleanupFromGenericCollection(application *App) {
	resources := genericResources[*mcp.Manager]{application.manager}
	_ = resources[0].Close()
}
func cleanupFromGenericField(application *App) {
	_ = application.resources[0].Close()
}
func cleanupFromGenericHolder(application *App) {
	_ = application.holder.resource.Close()
}
func cleanupFromCopy(application *App) {
	var resources []closer
	copy(resources, []closer{application.manager})
	_ = resources[0].Close()
}
func cleanupFromCopyAlias(application *App) {
	resources := make([]closer, 1)
	alias := resources
	copy(alias, []closer{application.manager})
	_ = resources[0].Close()
}
func cleanupFromIndexedAssignment(application *App) {
	resources := map[string]closer{}
	resources["mcp"] = application.manager
	_ = resources["mcp"].Close()
}
func cleanupFromReferenceAlias(application *App) {
	resources := map[string]closer{}
	alias := resources
	alias["mcp"] = application.manager
	_ = resources["mcp"].Close()
}
func cleanupFromNamedReferenceAlias(application *App) {
	resources := closerList{nil}
	alias := resources
	alias[0] = application.manager
	_ = resources[0].Close()
}
func cleanupFromSlicedReferenceAlias(application *App) {
	resources := make([]closer, 1)
	alias := resources[:]
	alias[0] = application.manager
	_ = resources[0].Close()
}
func cleanupFromPointerAlias(application *App) {
	owned := &ownedHolder{}
	alias := owned
	alias.resource = application.manager
	_ = owned.resource.Close()
}
func stashCloser(resources []closer, resource closer) { resources[0] = resource }
func cleanupFromHelperWrite(application *App) {
	resources := make([]closer, 1)
	stashCloser(resources, application.manager)
	_ = resources[0].Close()
}
func stashNamedCloser(resources closerList, resource closer) { resources[0] = resource }
func cleanupFromNamedHelperWrite(application *App) {
	resources := make(closerList, 1)
	stashNamedCloser(resources, application.manager)
	_ = resources[0].Close()
}
func cleanupFromChannelRange(application *App) {
	resources := make(chan closer, 1)
	resources <- application.manager
	for resource := range resources {
		_ = resource.Close()
		break
	}
}
func cleanupFromIteratorRange(application *App) {
	for resource := range maps.Values(map[string]closer{"mcp": application.manager}) {
		_ = resource.Close()
		break
	}
}
func cleanupFromIteratorPairRange(application *App) {
	for _, resource := range maps.All(map[string]closer{"mcp": application.manager}) {
		_ = resource.Close()
		break
	}
}
func cleanupFromBackwardIterator(application *App) {
	for _, resource := range slices.Backward([]closer{application.manager}) {
		_ = resource.Close()
		break
	}
}
func cleanupFromConcat(application *App) {
	resources := slices.Concat([]closer{nil}, []closer{application.manager})
	_ = resources[1].Close()
}
func cleanupFromSliceAssignment(application *App) {
	resources := make([]closer, 1)
	resources[:][0] = application.manager
	_ = resources[0].Close()
}
func cleanupFromFieldAddress(application *App) {
	owned := ownedHolder{}
	alias := &owned.resource
	*alias = application.manager
	_ = owned.resource.Close()
}
func cleanupFromMapKey(application *App) {
	resources := map[closer]struct{}{application.manager: {}}
	for resource := range resources {
		_ = resource.Close()
		break
	}
}
func cleanupFromMapKeyAssignment(application *App) {
	resources := map[closer]struct{}{}
	resources[application.manager] = struct{}{}
	for resource := range resources {
		_ = resource.Close()
		break
	}
}
func cleanupFromMapKeysIterator(application *App) {
	for resource := range maps.Keys(map[closer]struct{}{application.manager: {}}) {
		_ = resource.Close()
		break
	}
}
func cleanupFromMapAllKey(application *App) {
	for resource := range maps.All(map[closer]struct{}{application.manager: {}}) {
		_ = resource.Close()
		break
	}
}
func stashCloserKey(resources map[closer]struct{}, resource closer) { resources[resource] = struct{}{} }
func cleanupFromMapKeyHelper(application *App) {
	resources := map[closer]struct{}{}
	stashCloserKey(resources, application.manager)
	for resource := range resources {
		_ = resource.Close()
		break
	}
}
func cleanupMapKeyDoesNotOwnValue(application *App, unrelated closer) {
	resources := map[closer]closer{application.manager: unrelated}
	for _, resource := range resources {
		_ = resource.Close()
		break
	}
}
func cleanupFromMapsCopyAlias(application *App) {
	resources := map[string]closer{}
	alias := resources
	maps.Copy(alias, map[string]closer{"mcp": application.manager})
	_ = resources["mcp"].Close()
}
func cleanupFromMapsInsertAlias(application *App) {
	resources := map[string]closer{}
	alias := resources
	maps.Insert(alias, maps.All(map[string]closer{"mcp": application.manager}))
	_ = resources["mcp"].Close()
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
	_ = closer.Close(application.manager)
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
	want := []string{"packageManager.Close", "packageCleanup", "Run", "cleanupRunner.Run", "identityOwned.Close", "cleanupGeneric", "resource.Close", "cleanup", "cleanup", "closeOwned", "application.manager.Close", "func", "withResource", "resource.Close", "Close", "resources.Close", "resource.Close", "window.Close", "resources.Close", "owned.resource.Close", "owned.resource.Close", "wrapOwned.resource.Close", "wrapNestedOwned.owned.resource.Close", "owned.resource.Close", "resource.Close", "owned.resource.Close", "Close", "Close", "callbacks.cleanup", "callbacks.cleanup", "callbacks", "callbacks.cleanup", "callbacks.cleanup", "callbacks", "wrapCleanup.cleanup", "resources.Close", "application.resources.Close", "application.holder.resource.Close", "resources.Close", "resources.Close", "resources.Close", "resources.Close", "resources.Close", "resources.Close", "owned.resource.Close", "resources.Close", "resources.Close", "resource.Close", "resource.Close", "resource.Close", "resource.Close", "resources.Close", "resources.Close", "owned.resource.Close", "resource.Close", "resource.Close", "resource.Close", "resource.Close", "resource.Close", "resources.Close", "resources.Close", "manager.Close", "manager.Close", "closeTransitively", "runCleanup", "owned.Close", "namedOwned.Close", "closer.Close", "Close"}
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
	want := []string{"clients.Close", "byName.Close", "named.Close", "clients.Close", "client.Close", "client.Close"}
	if len(calls) != len(want) {
		t.Fatalf("cleanup calls = %v, want %v", calls, want)
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("cleanup calls = %v, want %v", calls, want)
		}
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
import (
	"maps"
	"slices"
	"github.com/juex-ai/juex/internal/tools"
)
type App struct{
	registry *tools.Registry
	registries genericRegistries[*tools.Registry]
	holder genericRegistryHolder[*tools.Registry]
}
type router struct{}
type registrar interface{ Register(tools.Tool) error }
type registrationConsumer func(registrar)
type registrationConsumers struct{ register registrationConsumer }
type registrarList []registrar
type registryList []*tools.Registry
type registryHolder struct{ registry registrar }
type nestedRegistryHolder struct{ owned registryHolder }
type genericRegistries[T any] []T
type genericRegistryHolder[T any] struct{ registry T }
type registrationRunner struct{ registry registrar }
type registrationFunc func(tools.Tool) error
type returnedRegistration struct{ register registrationFunc }
var packageRegistry = tools.NewRegistryWithOptions(tools.RegistryOptions{})
var packageRegistration = func(registry registrar) { _ = registry.Register(nil) }
func registerFromPackage() { packageRegistry.Register(nil) }
func registerFromPackageCallback(application *App) { packageRegistration(application.registry) }
func (runner registrationRunner) Run() { _ = runner.registry.Register(nil) }
func registerFromReceiver(application *App) {
	registrationRunner{registry: application.registry}.Run()
}
func registerFromReceiverExpression(application *App) {
	registrationRunner.Run(registrationRunner{registry: application.registry})
}
func localRegistry() *tools.Registry { return nil }
func localRegistrar(application *App) registrar { return application.registry }
func namedLocalRegistrar(application *App) (result registrar) {
	result = application.registry
	return
}
func identityRegistrar(registry registrar) registrar { return registry }
func registerFromIdentity(application *App) {
	identityRegistrar(application.registry).Register(nil)
}
func registerGeneric[T registrar](registry T) { _ = registry.Register(nil) }
func registerFromGeneric(application *App) {
	registerGeneric[*tools.Registry](application.registry)
}
func registryPair(application *App) (error, registrar) { return nil, application.registry }
func registryRelay(application *App) (error, registrar) { return registryPair(application) }
func registerFromRelay(application *App) {
	_, registry := registryRelay(application)
	registry.Register(nil)
}
func registerFromLiteral(application *App) {
	register := func(registry registrar) { _ = registry.Register(nil) }
	register(application.registry)
}
func registerThroughAlias(application *App) {
	register := registerOwned
	register(application.registry)
}
func registerThroughParentheses(application *App) {
	(registerOwned)(application.registry)
}
func registerFromIIFE(application *App) {
	func() { _ = application.registry.Register(nil) }()
	func(registry registrar) { _ = registry.Register(nil) }(application.registry)
}
func withRegistrar(registry registrar, callback func(registrar)) { callback(registry) }
func registerFromCallback(application *App) {
	withRegistrar(application.registry, func(registry registrar) { _ = registry.Register(nil) })
}
func registerFromLiteralCallbackField(application *App) {
	callbacks := registrationConsumers{}
	callbacks.register = func(registry registrar) { _ = registry.Register(nil) }
	callbacks.register(application.registry)
}
func registerFromLiteralCallbackIndex(application *App) {
	callbacks := []registrationConsumer{nil}
	callbacks[0] = func(registry registrar) { _ = registry.Register(nil) }
	callbacks[0](application.registry)
}
func registerFromKeyedCompositeCallback(application *App) {
	callbacks := registrationConsumers{register: func(registry registrar) { _ = registry.Register(nil) }}
	callbacks.register(application.registry)
}
func registerFromPositionalCompositeCallback(application *App) {
	callbacks := registrationConsumers{func(registry registrar) { _ = registry.Register(nil) }}
	callbacks.register(application.registry)
}
func registerFromIndexedCompositeCallback(application *App) {
	callbacks := []registrationConsumer{func(registry registrar) { _ = registry.Register(nil) }}
	callbacks[0](application.registry)
}
func wrapRegistration(registry registrar) returnedRegistration {
	return returnedRegistration{register: func(tool tools.Tool) error { return registry.Register(tool) }}
}
func registerFromReturnedComposite(application *App) {
	_ = wrapRegistration(application.registry).register(nil)
}
func registerFromAssertion(application *App) {
	registry, _ := any(application.registry).(registrar)
	registry.Register(nil)
}
func registerFromInterfaceCollection(application *App) {
	registries := []registrar{application.registry}
	registries[0].Register(nil)
	for _, registry := range registries {
		registry.Register(nil)
	}
	window := registries[:]
	window[0].Register(nil)
}
func registerFromAppend(application *App) {
	var registries []registrar
	registries = append(registries, application.registry)
	registries[0].Register(nil)
}
func registerFromHolder(application *App) {
	owned := registryHolder{registry: application.registry}
	owned.registry.Register(nil)
}
func registerFromPositionalHolder(application *App) {
	owned := registryHolder{application.registry}
	owned.registry.Register(nil)
}
func wrapRegistry(registry registrar) registryHolder { return registryHolder{registry} }
func registerFromWrappedHolder(application *App) {
	wrapRegistry(application.registry).registry.Register(nil)
}
func wrapNestedRegistry(registry registrar) nestedRegistryHolder {
	return nestedRegistryHolder{owned: registryHolder{registry: registry}}
}
func registerFromNestedWrappedHolder(application *App) {
	wrapNestedRegistry(application.registry).owned.registry.Register(nil)
}
func registerFromAssignedHolder(application *App) {
	owned := registryHolder{}
	owned.registry = application.registry
	owned.registry.Register(nil)
}
func registerFromPointerAssignment(application *App) {
	owned := &registryHolder{}
	(*owned).registry = application.registry
	(*owned).registry.Register(nil)
}
func registerFromAnonymousHolder(application *App) {
	owned := struct{ registry registrar }{registry: application.registry}
	owned.registry.Register(nil)
}
func registerFromChannel(application *App) {
	registries := make(chan registrar, 1)
	registries <- application.registry
	(<-registries).Register(nil)
}
func registerFromChannelAlias(application *App) {
	registries := make(chan registrar, 1)
	alias := registries
	alias <- application.registry
	(<-registries).Register(nil)
}
func registerFromGenericCollection(application *App) {
	registries := genericRegistries[*tools.Registry]{application.registry}
	registries[0].Register(nil)
}
func registerFromGenericField(application *App) {
	application.registries[0].Register(nil)
}
func registerFromGenericHolder(application *App) {
	application.holder.registry.Register(nil)
}
func registerFromCopy(application *App) {
	var registries []registrar
	copy(registries, []registrar{application.registry})
	registries[0].Register(nil)
}
func registerFromCopyAlias(application *App) {
	registries := make([]registrar, 1)
	alias := registries
	copy(alias, []registrar{application.registry})
	registries[0].Register(nil)
}
func registerFromIndexedAssignment(application *App) {
	registries := map[string]registrar{}
	registries["tools"] = application.registry
	registries["tools"].Register(nil)
}
func registerFromReferenceAlias(application *App) {
	registries := map[string]registrar{}
	alias := registries
	alias["tools"] = application.registry
	registries["tools"].Register(nil)
}
func registerFromNamedReferenceAlias(application *App) {
	registries := registrarList{nil}
	alias := registries
	alias[0] = application.registry
	registries[0].Register(nil)
}
func registerFromSlicedReferenceAlias(application *App) {
	registries := make([]registrar, 1)
	alias := registries[:]
	alias[0] = application.registry
	registries[0].Register(nil)
}
func registerFromPointerAlias(application *App) {
	owned := &registryHolder{}
	alias := owned
	alias.registry = application.registry
	owned.registry.Register(nil)
}
func stashRegistrar(registries []registrar, registry registrar) { registries[0] = registry }
func registerFromHelperWrite(application *App) {
	registries := make([]registrar, 1)
	stashRegistrar(registries, application.registry)
	registries[0].Register(nil)
}
func stashNamedRegistrar(registries registrarList, registry registrar) { registries[0] = registry }
func registerFromNamedHelperWrite(application *App) {
	registries := make(registrarList, 1)
	stashNamedRegistrar(registries, application.registry)
	registries[0].Register(nil)
}
func registerFromChannelRange(application *App) {
	registries := make(chan registrar, 1)
	registries <- application.registry
	for registry := range registries {
		registry.Register(nil)
		break
	}
}
func registerFromIteratorRange(application *App) {
	for registry := range maps.Values(map[string]registrar{"tools": application.registry}) {
		registry.Register(nil)
		break
	}
}
func registerFromIteratorPairRange(application *App) {
	for _, registry := range maps.All(map[string]registrar{"tools": application.registry}) {
		registry.Register(nil)
		break
	}
}
func registerFromBackwardIterator(application *App) {
	for _, registry := range slices.Backward([]registrar{application.registry}) {
		registry.Register(nil)
		break
	}
}
func registerFromConcat(application *App) {
	registries := slices.Concat([]registrar{nil}, []registrar{application.registry})
	registries[1].Register(nil)
}
func registerFromSliceAssignment(application *App) {
	registries := make([]registrar, 1)
	registries[:][0] = application.registry
	registries[0].Register(nil)
}
func registerFromFieldAddress(application *App) {
	owned := registryHolder{}
	alias := &owned.registry
	*alias = application.registry
	owned.registry.Register(nil)
}
func registerFromMapKey(application *App) {
	registries := map[registrar]struct{}{application.registry: {}}
	for registry := range registries {
		registry.Register(nil)
		break
	}
}
func registerFromMapKeyAssignment(application *App) {
	registries := map[registrar]struct{}{}
	registries[application.registry] = struct{}{}
	for registry := range registries {
		registry.Register(nil)
		break
	}
}
func registerFromMapKeysIterator(application *App) {
	for registry := range maps.Keys(map[registrar]struct{}{application.registry: {}}) {
		registry.Register(nil)
		break
	}
}
func registerFromMapAllKey(application *App) {
	for registry := range maps.All(map[registrar]struct{}{application.registry: {}}) {
		registry.Register(nil)
		break
	}
}
func stashRegistrarKey(registries map[registrar]struct{}, registry registrar) { registries[registry] = struct{}{} }
func registerFromMapKeyHelper(application *App) {
	registries := map[registrar]struct{}{}
	stashRegistrarKey(registries, application.registry)
	for registry := range registries {
		registry.Register(nil)
		break
	}
}
func registrationMapKeyDoesNotOwnValue(application *App, unrelated registrar) {
	registries := map[registrar]registrar{application.registry: unrelated}
	for _, registry := range registries {
		registry.Register(nil)
		break
	}
}
func registerFromMapsCopyAlias(application *App) {
	registries := map[string]registrar{}
	alias := registries
	maps.Copy(alias, map[string]registrar{"tools": application.registry})
	registries["tools"].Register(nil)
}
func registerFromMapsInsertAlias(application *App) {
	registries := map[string]registrar{}
	alias := registries
	maps.Insert(alias, maps.All(map[string]registrar{"tools": application.registry}))
	registries["tools"].Register(nil)
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
	(*tools.Registry).Register(registry, nil)
	registrar.Register(application.registry, nil)
	registerExpression := (*tools.Registry).Register
	registerExpression(registry, nil)
	registry.Register(nil)
	(registry.Register)(nil)
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
	want := []string{"packageRegistry.Register", "packageRegistration", "Run", "registrationRunner.Run", "identityRegistrar.Register", "registerGeneric", "registry.Register", "register", "register", "registerOwned", "application.registry.Register", "func", "withRegistrar", "callbacks.register", "callbacks", "callbacks.register", "callbacks.register", "callbacks", "wrapRegistration.register", "registry.Register", "registries.Register", "registry.Register", "window.Register", "registries.Register", "owned.registry.Register", "owned.registry.Register", "wrapRegistry.registry.Register", "wrapNestedRegistry.owned.registry.Register", "owned.registry.Register", "registry.Register", "owned.registry.Register", "Register", "Register", "registries.Register", "application.registries.Register", "application.holder.registry.Register", "registries.Register", "registries.Register", "registries.Register", "registries.Register", "registries.Register", "registries.Register", "owned.registry.Register", "registries.Register", "registries.Register", "registry.Register", "registry.Register", "registry.Register", "registry.Register", "registries.Register", "registries.Register", "owned.registry.Register", "registry.Register", "registry.Register", "registry.Register", "registry.Register", "registry.Register", "registries.Register", "registries.Register", "registry.Register", "registry.Register", "Register", "registrar.Register", "registerExpression", "registry.Register", "registry.Register", "register", "converted.Register", "tools.RegisterBuiltins", "bulkRegister", "constructed.MustRegister", "application.registry.Register", "localRegistry.Register", "localRegistrar.Register", "namedLocalRegistrar.Register", "registries.Register", "registries.Register", "named.Register", "registerTransitively", "runRegistration"}
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

func TestAppCompositionInspectionTracksMultiResultAndVariadicParameters(t *testing.T) {
	source := `package app
import (
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/tools"
)
type App struct {
	manager *mcp.Manager
	registry *tools.Registry
}
type closer interface { Close() error }
type registrar interface { Register(tools.Tool) error }
func closerPair(unrelated closer, resource closer) (error, closer) { return nil, resource }
func closePair(unrelated closer, resource closer) {
	_, owned := closerPair(unrelated, resource)
	_ = owned.Close()
}
func closeVariadic(resources ...closer) { _ = resources[1].Close() }
func cleanupCallable(resource closer) func() error { return resource.Close }
func relayCleanupCallable(resource closer) func() error { return cleanupCallable(resource) }
func cleanupClosure(resource closer) func() { return func() { _ = resource.Close() } }
func registrarPair(unrelated registrar, registry registrar) (error, registrar) { return nil, registry }
func registerPair(unrelated registrar, registry registrar) {
	_, owned := registrarPair(unrelated, registry)
	_ = owned.Register(nil)
}
func registerVariadic(registries ...registrar) { _ = registries[1].Register(nil) }
func registrationCallable(registry registrar) func(tools.Tool) error { return registry.Register }
func relayRegistrationCallable(registry registrar) func(tools.Tool) error {
	return registrationCallable(registry)
}
func registrationClosure(registry registrar) func(tools.Tool) error {
	return func(tool tools.Tool) error { return registry.Register(tool) }
}
func configure(application *App, unrelatedCloser closer, unrelatedRegistrar registrar) {
	closePair(unrelatedCloser, application.manager)
	closeVariadic(unrelatedCloser, application.manager)
	_ = cleanupCallable(application.manager)()
	_ = relayCleanupCallable(application.manager)()
	cleanupClosure(application.manager)()
	cleanup := cleanupCallable(application.manager)
	_ = cleanup()
	registerPair(unrelatedRegistrar, application.registry)
	registerVariadic(unrelatedRegistrar, application.registry)
	_ = registrationCallable(application.registry)(nil)
	_ = relayRegistrationCallable(application.registry)(nil)
	_ = registrationClosure(application.registry)(nil)
	register := registrationCallable(application.registry)
	_ = register(nil)
}`
	dir := t.TempDir()
	path := filepath.Join(dir, "multi_variadic.go")
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
	var cleanupCalls []string
	inspectAppFeatureCleanup(parsed, importPaths(parsed), types, func(_ *ast.CallExpr, chain string) {
		cleanupCalls = append(cleanupCalls, chain)
	})
	cleanup := "," + strings.Join(cleanupCalls, ",") + ","
	if !strings.Contains(cleanup, ",closePair,") || !strings.Contains(cleanup, ",closeVariadic,") || !strings.Contains(cleanup, ",cleanupCallable,") || !strings.Contains(cleanup, ",relayCleanupCallable,") || !strings.Contains(cleanup, ",cleanupClosure,") || !strings.Contains(cleanup, ",cleanup,") {
		t.Fatalf("cleanup calls = %v, want multi-result, variadic, and callable helper calls", cleanupCalls)
	}
	var registrationCalls []string
	inspectAppToolRegistration(parsed, importPaths(parsed), types, func(_ *ast.CallExpr, chain string) {
		registrationCalls = append(registrationCalls, chain)
	})
	registration := "," + strings.Join(registrationCalls, ",") + ","
	if !strings.Contains(registration, ",registerPair,") || !strings.Contains(registration, ",registerVariadic,") || !strings.Contains(registration, ",registrationCallable,") || !strings.Contains(registration, ",relayRegistrationCallable,") || !strings.Contains(registration, ",registrationClosure,") || !strings.Contains(registration, ",register,") {
		t.Fatalf("Tool registration calls = %v, want multi-result, variadic, and callable helper calls", registrationCalls)
	}
}

func TestAppCompositionInspectionTracksCrossFilePackageBindings(t *testing.T) {
	state := `package app
import (
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/tools"
)
var managerSource *mcp.Manager
var packageManager = managerSource
var packageRegistry = tools.NewRegistryWithOptions(tools.RegistryOptions{})
var packageCleanup = func(resource closer) { _ = resource.Close() }
var packageRegistration = func(registry registrar) { _ = registry.Register(nil) }
`
	shutdown := `package app
import "github.com/juex-ai/juex/internal/tools"
type App struct{}
type closer interface { Close() error }
type registrar interface { Register(tools.Tool) error }
func shutdown() {
	_ = packageManager.Close()
	packageCleanup(packageManager)
	packageRegistry.Register(nil)
	packageRegistration(packageRegistry)
}
`
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.go")
	shutdownPath := filepath.Join(dir, "shutdown.go")
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shutdownPath, []byte(shutdown), 0o600); err != nil {
		t.Fatal(err)
	}
	types, err := appCompositionTypes(dir)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), shutdownPath, shutdown, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cleanupCalls []string
	inspectAppFeatureCleanup(parsed, importPaths(parsed), types, func(_ *ast.CallExpr, chain string) {
		cleanupCalls = append(cleanupCalls, chain)
	})
	if cleanup := "," + strings.Join(cleanupCalls, ",") + ","; !strings.Contains(cleanup, ",packageManager.Close,") || !strings.Contains(cleanup, ",packageCleanup,") {
		t.Fatalf("cleanup calls = %v, want cross-file package manager and callback", cleanupCalls)
	}
	var registrationCalls []string
	inspectAppToolRegistration(parsed, importPaths(parsed), types, func(_ *ast.CallExpr, chain string) {
		registrationCalls = append(registrationCalls, chain)
	})
	if registration := "," + strings.Join(registrationCalls, ",") + ","; !strings.Contains(registration, ",packageRegistry.Register,") || !strings.Contains(registration, ",packageRegistration,") {
		t.Fatalf("Tool registration calls = %v, want cross-file package registry and callback", registrationCalls)
	}
}

func TestAppCompositionInspectionTracksInterfaceDispatch(t *testing.T) {
	source := `package app
import (
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/tools"
)
type App struct {
	manager *mcp.Manager
	registry *tools.Registry
}
type closer interface { Close() error }
type registrar interface { Register(tools.Tool) error }
type closerAlias = closer
type registrarAlias = registrar
type cleanupDispatcher interface { Run(closer) }
type registrationDispatcher interface { Run(registrar) }
type embeddedCleanupDispatcher interface { cleanupDispatcher }
type embeddedRegistrationDispatcher interface { registrationDispatcher }
type cleanupDispatchImpl struct{}
type registrationDispatchImpl struct{}
type cleanupWrapper struct{ cleanupDispatchImpl }
type registrationWrapper struct{ registrationDispatchImpl }
func (cleanupDispatchImpl) Run(resource closerAlias) { _ = resource.Close() }
func (registrationDispatchImpl) Run(registry registrarAlias) { _ = registry.Register(nil) }
func dispatchCleanup(dispatcher cleanupDispatcher, resource closer) { dispatcher.Run(resource) }
func dispatchRegistration(dispatcher registrationDispatcher, registry registrar) { dispatcher.Run(registry) }
func dispatchEmbeddedCleanup(dispatcher embeddedCleanupDispatcher, resource closer) { dispatcher.Run(resource) }
func dispatchEmbeddedRegistration(dispatcher embeddedRegistrationDispatcher, registry registrar) { dispatcher.Run(registry) }
func configure(application *App) {
	dispatchCleanup(cleanupDispatchImpl{}, application.manager)
	dispatchRegistration(registrationDispatchImpl{}, application.registry)
	dispatchEmbeddedCleanup(cleanupDispatchImpl{}, application.manager)
	dispatchEmbeddedRegistration(registrationDispatchImpl{}, application.registry)
	cleanupWrapper{}.Run(application.manager)
	registrationWrapper{}.Run(application.registry)
	cleanup := cleanupDispatchImpl{}.Run
	cleanup(application.manager)
	registration := registrationDispatchImpl{}.Run
	registration(application.registry)
	promotedCleanup := cleanupWrapper{}.Run
	promotedCleanup(application.manager)
	promotedRegistration := registrationWrapper{}.Run
	promotedRegistration(application.registry)
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "dispatch.go")
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
	var cleanupCalls []string
	inspectAppFeatureCleanup(parsed, importPaths(parsed), types, func(_ *ast.CallExpr, chain string) {
		cleanupCalls = append(cleanupCalls, chain)
	})
	if cleanup := "," + strings.Join(cleanupCalls, ",") + ","; !strings.Contains(cleanup, ",dispatchCleanup,") || !strings.Contains(cleanup, ",dispatchEmbeddedCleanup,") || !strings.Contains(cleanup, ",Run,") || !strings.Contains(cleanup, ",cleanup,") || !strings.Contains(cleanup, ",promotedCleanup,") {
		t.Fatalf("cleanup calls = %v, want interface/concrete promotion and helper method values", cleanupCalls)
	}
	var registrationCalls []string
	inspectAppToolRegistration(parsed, importPaths(parsed), types, func(_ *ast.CallExpr, chain string) {
		registrationCalls = append(registrationCalls, chain)
	})
	if registration := "," + strings.Join(registrationCalls, ",") + ","; !strings.Contains(registration, ",dispatchRegistration,") || !strings.Contains(registration, ",dispatchEmbeddedRegistration,") || !strings.Contains(registration, ",Run,") || !strings.Contains(registration, ",registration,") || !strings.Contains(registration, ",promotedRegistration,") {
		t.Fatalf("Tool registration calls = %v, want interface/concrete promotion and helper method values", registrationCalls)
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
		fields:           make(map[string]map[string]string),
		fieldOrder:       make(map[string][]string),
		cleanupMethods:   make(map[string]map[string]bool),
		functionResults:  make(map[string][]string),
		namedTypes:       make(map[string]string),
		typeParameters:   make(map[string][]string),
		cleanupParams:    make(map[string]map[int]bool),
		toolParams:       make(map[string]map[int]bool),
		cleanupResults:   make(map[string]map[int]map[string]bool),
		toolResults:      make(map[string]map[int]bool),
		toolCallResults:  make(map[string]map[int]bool),
		toolCallPaths:    make(map[string]map[int]map[string]bool),
		resultParams:     make(map[string]map[int]map[int]bool),
		resultParamPaths: make(map[string]map[int]map[int]map[string]bool),
		parameterWrites:  make(map[string]map[int]map[int]map[string]bool),
		variadicParams:   make(map[string]int),
		appFunctionKeys:  make(map[string]bool),
		interfaceMethods: make(map[string]methodShape),
		interfaceEmbeds:  make(map[string][]string),
		concreteMethods:  make(map[string]methodShape),
		methodImpls:      make(map[string][]string),
	}
	var appSources []indexedAppSource
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
		appSources = append(appSources, indexedAppSource{file: parsed, imports: imports})
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
	indexEmbeddedInterfaceMethods(&types)
	indexPromotedConcreteMethods(&types)
	indexInterfaceMethodImplementations(&types)
	indexLocalFlows(&types)
	types.packageValues, types.packageResources = packageBindingStateForSources(appSources, types)
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
		if general, ok := declaration.(*ast.GenDecl); ok {
			if packagePath == modulePath+"/internal/app" && general.Tok == token.VAR {
				parent := indexedAppFunction{key: packageFunctionScopeKey, imports: imports}
				for _, raw := range general.Specs {
					spec := raw.(*ast.ValueSpec)
					for index, name := range spec.Names {
						if literal := assignedFunctionLiteral(spec.Values, index); literal != nil {
							indexLocalFunctionLiteral(parent, name, literal, types)
						}
						for _, embedded := range assignedCompositeFunctionLiterals(spec.Values, index, imports, *types) {
							indexLocalFunctionLiteral(parent, embedded.literal, embedded.literal, types)
						}
					}
				}
				continue
			}
			if general.Tok != token.TYPE {
				continue
			}
			for _, raw := range general.Specs {
				spec, ok := raw.(*ast.TypeSpec)
				if !ok {
					continue
				}
				typeName := packagePath + "." + spec.Name.Name
				indexTypeParameters(spec, typeName, packagePath, types)
				if contract, ok := spec.Type.(*ast.InterfaceType); ok {
					if packagePath == modulePath+"/internal/app" {
						indexInterfaceMethods(contract, typeName, imports, packagePath, types)
					}
					continue
				}
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
						fieldName := embeddedPrefix + fieldType
						types.fields[typeName][fieldName] = fieldType
						types.fieldOrder[typeName] = append(types.fieldOrder[typeName], fieldName)
						continue
					}
					for _, name := range field.Names {
						types.fields[typeName][name.Name] = canonicalTypeInPackage(field.Type, imports, packagePath)
						types.fieldOrder[typeName] = append(types.fieldOrder[typeName], name.Name)
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
			if function.Recv != nil {
				types.concreteMethods[indexed.key] = methodShapeForFunction(function.Type, imports, packagePath)
			}
			indexVariadicParameter(indexed, types)
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

func indexTypeParameters(spec *ast.TypeSpec, typeName, packagePath string, types *compositionTypeIndex) {
	if spec.TypeParams == nil {
		return
	}
	for _, field := range spec.TypeParams.List {
		for _, name := range field.Names {
			types.typeParameters[typeName] = append(types.typeParameters[typeName], packagePath+"."+name.Name)
		}
	}
}

func indexInterfaceMethods(contract *ast.InterfaceType, typeName string, imports map[string]string, packagePath string, types *compositionTypeIndex) {
	for _, field := range contract.Methods.List {
		function, ok := field.Type.(*ast.FuncType)
		if !ok {
			if embedded := canonicalTypeInPackage(field.Type, imports, packagePath); embedded != "" {
				types.interfaceEmbeds[typeName] = append(types.interfaceEmbeds[typeName], embedded)
			}
			continue
		}
		for _, name := range field.Names {
			key := typeName + "." + name.Name
			shape := methodShapeForFunction(function, imports, packagePath)
			types.interfaceMethods[key] = shape
			types.functionResults[key] = append([]string(nil), shape.results...)
			if index, variadic := variadicParameterIndex(function.Params); variadic {
				types.variadicParams[key] = index
			}
		}
	}
}

func methodShapeForFunction(function *ast.FuncType, imports map[string]string, packagePath string) methodShape {
	parameters, variadic := methodFieldTypes(function.Params, imports, packagePath, true)
	results, _ := methodFieldTypes(function.Results, imports, packagePath, false)
	return methodShape{parameters: parameters, results: results, variadic: variadic}
}

func methodFieldTypes(fields *ast.FieldList, imports map[string]string, packagePath string, allowVariadic bool) ([]string, bool) {
	if fields == nil {
		return nil, false
	}
	var resolved []string
	variadic := false
	for index, field := range fields.List {
		expression := field.Type
		if ellipsis, ok := expression.(*ast.Ellipsis); ok && allowVariadic && index == len(fields.List)-1 {
			variadic = true
			expression = ellipsis.Elt
		}
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		typeName := canonicalTypeInPackage(expression, imports, packagePath)
		for range count {
			resolved = append(resolved, typeName)
		}
	}
	return resolved, variadic
}

func indexEmbeddedInterfaceMethods(types *compositionTypeIndex) {
	for {
		changed := false
		for interfaceType, embeddedTypes := range types.interfaceEmbeds {
			for _, embeddedType := range embeddedTypes {
				prefix := embeddedType + "."
				for embeddedKey, shape := range types.interfaceMethods {
					if !strings.HasPrefix(embeddedKey, prefix) {
						continue
					}
					key := interfaceType + "." + strings.TrimPrefix(embeddedKey, prefix)
					if _, exists := types.interfaceMethods[key]; exists {
						continue
					}
					types.interfaceMethods[key] = shape
					types.functionResults[key] = append([]string(nil), shape.results...)
					if shape.variadic {
						types.variadicParams[key] = len(shape.parameters) - 1
					}
					changed = true
				}
			}
		}
		if !changed {
			return
		}
	}
}

func indexPromotedConcreteMethods(types *compositionTypeIndex) {
	for {
		changed := false
		for outerType, fields := range types.fields {
			for field, embeddedType := range fields {
				if !strings.HasPrefix(field, embeddedPrefix) {
					continue
				}
				prefix := embeddedType + "."
				for sourceKey, shape := range types.concreteMethods {
					if !strings.HasPrefix(sourceKey, prefix) {
						continue
					}
					if promoteConcreteMethod(types, outerType, strings.TrimPrefix(sourceKey, prefix), sourceKey, shape) {
						changed = true
					}
				}
				for sourceKey, shape := range types.interfaceMethods {
					if !strings.HasPrefix(sourceKey, prefix) {
						continue
					}
					if promoteConcreteMethod(types, outerType, strings.TrimPrefix(sourceKey, prefix), sourceKey, shape) {
						changed = true
					}
				}
			}
		}
		if !changed {
			return
		}
	}
}

func promoteConcreteMethod(types *compositionTypeIndex, outerType, methodName, sourceKey string, shape methodShape) bool {
	key := outerType + "." + methodName
	if _, exists := types.concreteMethods[key]; exists {
		return false
	}
	types.concreteMethods[key] = shape
	types.appFunctionKeys[key] = true
	types.functionResults[key] = append([]string(nil), shape.results...)
	if shape.variadic {
		types.variadicParams[key] = len(shape.parameters) - 1
	}
	types.methodImpls[key] = append(types.methodImpls[key], sourceKey)
	return true
}

func indexInterfaceMethodImplementations(types *compositionTypeIndex) {
	for interfaceKey, contract := range types.interfaceMethods {
		methodName := interfaceKey[strings.LastIndex(interfaceKey, ".")+1:]
		for concreteKey, implementation := range types.concreteMethods {
			if concreteKey[strings.LastIndex(concreteKey, ".")+1:] == methodName && equalMethodShape(contract, implementation, *types) {
				types.methodImpls[interfaceKey] = append(types.methodImpls[interfaceKey], concreteKey)
			}
		}
	}
}

func equalMethodShape(left, right methodShape, types compositionTypeIndex) bool {
	if left.variadic != right.variadic || len(left.parameters) != len(right.parameters) || len(left.results) != len(right.results) {
		return false
	}
	for index := range left.parameters {
		if resolveNamedType(left.parameters[index], types) != resolveNamedType(right.parameters[index], types) {
			return false
		}
	}
	for index := range left.results {
		if resolveNamedType(left.results[index], types) != resolveNamedType(right.results[index], types) {
			return false
		}
	}
	return true
}

func indexLocalFunctionLiterals(parent indexedAppFunction, types *compositionTypeIndex) {
	ast.Inspect(parent.body(), func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for index, left := range value.Lhs {
				if assignmentValueKey(left) == "" {
					continue
				}
				literal := assignedFunctionLiteral(value.Rhs, index)
				if literal != nil {
					indexLocalFunctionLiteral(parent, left, literal, types)
				}
				for _, embedded := range assignedCompositeFunctionLiterals(value.Rhs, index, parent.imports, *types) {
					indexLocalFunctionLiteral(parent, embedded.literal, embedded.literal, types)
				}
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
					for _, embedded := range assignedCompositeFunctionLiterals(spec.Values, index, parent.imports, *types) {
						indexLocalFunctionLiteral(parent, embedded.literal, embedded.literal, types)
					}
				}
			}
		case *ast.FuncLit:
			return false
		}
		return true
	})
}

func indexLocalFunctionLiteral(parent indexedAppFunction, target ast.Expr, literal *ast.FuncLit, types *compositionTypeIndex) {
	indexed := indexedAppFunction{
		key:     localFunctionKey(parent.key, target),
		literal: literal,
		imports: parent.imports,
	}
	types.appFunctions = append(types.appFunctions, indexed)
	types.appFunctionKeys[indexed.key] = true
	indexVariadicParameter(indexed, types)
	indexLocalFunctionLiterals(indexed, types)
}

func indexVariadicParameter(function indexedAppFunction, types *compositionTypeIndex) {
	index, ok := variadicParameterIndex(function.parameters())
	if !ok {
		return
	}
	types.variadicParams[function.key] = index
}

func variadicParameterIndex(parameters *ast.FieldList) (int, bool) {
	if parameters == nil || len(parameters.List) == 0 {
		return 0, false
	}
	last := len(parameters.List) - 1
	if _, ok := parameters.List[last].Type.(*ast.Ellipsis); !ok {
		return 0, false
	}
	index := 0
	for _, field := range parameters.List[:last] {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		index += count
	}
	return index, true
}

func assignedFunctionLiteral(expressions []ast.Expr, index int) *ast.FuncLit {
	var expression ast.Expr
	switch {
	case len(expressions) == 1 && index == 0:
		expression = expressions[0]
	case len(expressions) > 1 && index < len(expressions):
		expression = expressions[index]
	}
	return functionLiteralExpression(expression)
}

type compositeFunctionLiteral struct {
	suffix  string
	literal *ast.FuncLit
}

func assignedCompositeFunctionLiterals(expressions []ast.Expr, index int, imports map[string]string, types compositionTypeIndex) []compositeFunctionLiteral {
	expression := assignedExpression(expressions, index)
	return compositeFunctionLiterals(expression, "", imports, types)
}

func compositeFunctionLiterals(expression ast.Expr, prefix string, imports map[string]string, types compositionTypeIndex) []compositeFunctionLiteral {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			break
		}
		expression = parenthesized.X
	}
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	typeName := canonicalType(literal.Type, imports)
	fieldOrder, structured := compositeFieldOrder(literal.Type, typeName, imports, types)
	var result []compositeFunctionLiteral
	for index, element := range literal.Elts {
		suffix := "[]"
		if pair, ok := element.(*ast.KeyValueExpr); ok {
			if field, ok := pair.Key.(*ast.Ident); structured && ok {
				suffix = "." + field.Name
			}
			element = pair.Value
		} else if structured && index < len(fieldOrder) && !strings.HasPrefix(fieldOrder[index], embeddedPrefix) {
			suffix = "." + fieldOrder[index]
		}
		if callback := functionLiteralExpression(element); callback != nil {
			result = append(result, compositeFunctionLiteral{suffix: prefix + suffix, literal: callback})
			continue
		}
		result = append(result, compositeFunctionLiterals(element, prefix+suffix, imports, types)...)
	}
	return result
}

func trackCompositeFunctionTypes(values map[string]string, parent string, target ast.Expr, expressions []ast.Expr, index int, imports map[string]string, types compositionTypeIndex) {
	root := assignmentValueKey(target)
	if root == "" {
		return
	}
	for _, embedded := range assignedCompositeFunctionLiterals(expressions, index, imports, types) {
		setMayValueType(values, root+embedded.suffix, localFunctionTypePrefix+localFunctionKey(parent, embedded.literal), types)
	}
}

func compositeCallablePath(suffix string) string {
	path := strings.ReplaceAll(suffix, "[]", "")
	return strings.TrimPrefix(path, ".")
}

func functionLiteralExpression(expression ast.Expr) *ast.FuncLit {
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

func localFunctionKey(parent string, target ast.Expr) string {
	return parent + "$func@" + strconv.Itoa(int(target.Pos()))
}

func localFunctionType(parent string, target ast.Expr, expressions []ast.Expr, index int) string {
	if assignedFunctionLiteral(expressions, index) == nil {
		return ""
	}
	return localFunctionTypePrefix + localFunctionKey(parent, target)
}

func declaredFunctionKey(function *ast.FuncDecl, imports map[string]string, packagePath string) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return packagePath + "." + function.Name.Name
	}
	return canonicalTypeInPackage(function.Recv.List[0].Type, imports, packagePath) + "." + function.Name.Name
}

func indexLocalFlows(types *compositionTypeIndex) {
	for {
		before := localFlowCount(*types)
		indexLocalResultFlows(types)
		indexLocalCleanupParameters(types)
		propagateInterfaceMethodFlows(types)
		if localFlowCount(*types) == before {
			return
		}
	}
}

func propagateInterfaceMethodFlows(types *compositionTypeIndex) {
	for interfaceKey, implementations := range types.methodImpls {
		for _, implementation := range implementations {
			types.cleanupParams[interfaceKey] = mergeIntSet(types.cleanupParams[interfaceKey], types.cleanupParams[implementation])
			types.toolParams[interfaceKey] = mergeIntSet(types.toolParams[interfaceKey], types.toolParams[implementation])
			types.cleanupResults[interfaceKey] = mergeIndexedPathSets(types.cleanupResults[interfaceKey], types.cleanupResults[implementation])
			types.toolResults[interfaceKey] = mergeIntSet(types.toolResults[interfaceKey], types.toolResults[implementation])
			types.toolCallResults[interfaceKey] = mergeIntSet(types.toolCallResults[interfaceKey], types.toolCallResults[implementation])
			types.toolCallPaths[interfaceKey] = mergeIndexedPathSets(types.toolCallPaths[interfaceKey], types.toolCallPaths[implementation])
			types.resultParams[interfaceKey] = mergeIndexedIntSets(types.resultParams[interfaceKey], types.resultParams[implementation])
			types.resultParamPaths[interfaceKey] = mergeDoubleIndexedPathSets(types.resultParamPaths[interfaceKey], types.resultParamPaths[implementation])
			types.parameterWrites[interfaceKey] = mergeDoubleIndexedPathSets(types.parameterWrites[interfaceKey], types.parameterWrites[implementation])
		}
	}
}

func mergeIntSet(destination, source map[int]bool) map[int]bool {
	if len(source) == 0 {
		return destination
	}
	if destination == nil {
		destination = make(map[int]bool)
	}
	for index := range source {
		destination[index] = true
	}
	return destination
}

func mergeIndexedPathSets(destination, source map[int]map[string]bool) map[int]map[string]bool {
	if len(source) == 0 {
		return destination
	}
	if destination == nil {
		destination = make(map[int]map[string]bool)
	}
	for index, paths := range source {
		if destination[index] == nil {
			destination[index] = make(map[string]bool)
		}
		for path := range paths {
			destination[index][path] = true
		}
	}
	return destination
}

func mergeIndexedIntSets(destination, source map[int]map[int]bool) map[int]map[int]bool {
	if len(source) == 0 {
		return destination
	}
	if destination == nil {
		destination = make(map[int]map[int]bool)
	}
	for index, values := range source {
		destination[index] = mergeIntSet(destination[index], values)
	}
	return destination
}

func mergeDoubleIndexedPathSets(destination, source map[int]map[int]map[string]bool) map[int]map[int]map[string]bool {
	if len(source) == 0 {
		return destination
	}
	if destination == nil {
		destination = make(map[int]map[int]map[string]bool)
	}
	for outer, values := range source {
		if destination[outer] == nil {
			destination[outer] = make(map[int]map[string]bool)
		}
		for inner, paths := range values {
			if destination[outer][inner] == nil {
				destination[outer][inner] = make(map[string]bool)
			}
			for path := range paths {
				destination[outer][inner][path] = true
			}
		}
	}
	return destination
}

func localFlowCount(types compositionTypeIndex) int {
	count := 0
	for _, parameters := range types.cleanupParams {
		count += len(parameters)
	}
	for _, parameters := range types.toolParams {
		count += len(parameters)
	}
	for _, results := range types.cleanupResults {
		for _, paths := range results {
			count += len(paths)
		}
	}
	for _, results := range types.toolResults {
		count += len(results)
	}
	for _, results := range types.toolCallResults {
		count += len(results)
	}
	for _, results := range types.toolCallPaths {
		for _, paths := range results {
			count += len(paths)
		}
	}
	for _, results := range types.resultParams {
		for _, parameters := range results {
			count += len(parameters)
		}
	}
	for _, results := range types.resultParamPaths {
		for _, parameters := range results {
			for _, paths := range parameters {
				count += len(paths)
			}
		}
	}
	for _, destinations := range types.parameterWrites {
		for _, sources := range destinations {
			for _, paths := range sources {
				count += len(paths)
			}
		}
	}
	return count
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
			cleanupResults, toolResults, toolCallResults, toolCallPaths, resultParams, resultParamPaths, parameterWrites := inferLocalResultFlows(function, *types)
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
			if types.toolCallResults[function.key] == nil {
				types.toolCallResults[function.key] = make(map[int]bool)
			}
			for index := range toolCallResults {
				if !types.toolCallResults[function.key][index] {
					types.toolCallResults[function.key][index] = true
					changed = true
				}
			}
			if types.toolCallPaths[function.key] == nil {
				types.toolCallPaths[function.key] = make(map[int]map[string]bool)
			}
			for index, paths := range toolCallPaths {
				if types.toolCallPaths[function.key][index] == nil {
					types.toolCallPaths[function.key][index] = make(map[string]bool)
				}
				for path := range paths {
					if !types.toolCallPaths[function.key][index][path] {
						types.toolCallPaths[function.key][index][path] = true
						changed = true
					}
				}
			}
			if types.resultParams[function.key] == nil {
				types.resultParams[function.key] = make(map[int]map[int]bool)
			}
			for resultIndex, parameters := range resultParams {
				if types.resultParams[function.key][resultIndex] == nil {
					types.resultParams[function.key][resultIndex] = make(map[int]bool)
				}
				for parameterIndex := range parameters {
					if !types.resultParams[function.key][resultIndex][parameterIndex] {
						types.resultParams[function.key][resultIndex][parameterIndex] = true
						changed = true
					}
				}
			}
			if types.resultParamPaths[function.key] == nil {
				types.resultParamPaths[function.key] = make(map[int]map[int]map[string]bool)
			}
			for resultIndex, parameters := range resultParamPaths {
				if types.resultParamPaths[function.key][resultIndex] == nil {
					types.resultParamPaths[function.key][resultIndex] = make(map[int]map[string]bool)
				}
				for parameterIndex, paths := range parameters {
					if types.resultParamPaths[function.key][resultIndex][parameterIndex] == nil {
						types.resultParamPaths[function.key][resultIndex][parameterIndex] = make(map[string]bool)
					}
					for path := range paths {
						if !types.resultParamPaths[function.key][resultIndex][parameterIndex][path] {
							types.resultParamPaths[function.key][resultIndex][parameterIndex][path] = true
							changed = true
						}
					}
				}
			}
			if types.parameterWrites[function.key] == nil {
				types.parameterWrites[function.key] = make(map[int]map[int]map[string]bool)
			}
			for destinationIndex, sources := range parameterWrites {
				if types.parameterWrites[function.key][destinationIndex] == nil {
					types.parameterWrites[function.key][destinationIndex] = make(map[int]map[string]bool)
				}
				for sourceIndex, paths := range sources {
					if types.parameterWrites[function.key][destinationIndex][sourceIndex] == nil {
						types.parameterWrites[function.key][destinationIndex][sourceIndex] = make(map[string]bool)
					}
					for path := range paths {
						if !types.parameterWrites[function.key][destinationIndex][sourceIndex][path] {
							types.parameterWrites[function.key][destinationIndex][sourceIndex][path] = true
							changed = true
						}
					}
				}
			}
		}
	}
}

func inferLocalResultFlows(function indexedAppFunction, types compositionTypeIndex) (map[int]map[string]bool, map[int]bool, map[int]bool, map[int]map[string]bool, map[int]map[int]bool, map[int]map[int]map[string]bool, map[int]map[int]map[string]bool) {
	cleanupResults := make(map[int]map[string]bool)
	toolResults := make(map[int]bool)
	toolCallResults := make(map[int]bool)
	toolCallPaths := make(map[int]map[string]bool)
	resultParams := make(map[int]map[int]bool)
	resultParamPaths := make(map[int]map[int]map[string]bool)
	parameterWrites := make(map[int]map[int]map[string]bool)
	concreteTypes := types
	concreteTypes.resultParams = nil
	origins := functionParameterOrigins(function)
	values := namedValueTypes(function.receiver(), function.imports)
	for name, typeName := range namedValueTypes(function.parameters(), function.imports) {
		values[name] = typeName
	}
	for name, typeName := range namedValueTypes(function.results(), function.imports) {
		values[name] = typeName
	}
	resources := namedCleanupValues(function.receiver(), function.imports, concreteTypes)
	for name, paths := range namedCleanupValues(function.parameters(), function.imports, concreteTypes) {
		resources[name] = paths
	}
	for name, paths := range namedCleanupValues(function.results(), function.imports, concreteTypes) {
		resources[name] = paths
	}
	references := functionReferenceValues(function, types)
	aliases := make(map[string]map[string]bool)
	recordParameterWrite := func(destinations, sources map[int]bool, prefix string) {
		for destination := range destinations {
			if parameterWrites[destination] == nil {
				parameterWrites[destination] = make(map[int]map[string]bool)
			}
			for source := range sources {
				if parameterWrites[destination][source] == nil {
					parameterWrites[destination][source] = make(map[string]bool)
				}
				parameterWrites[destination][source][prefix] = true
			}
		}
	}
	recordResult := func(index int, expression ast.Expr, expressionResultIndex int) {
		var capturedCleanup, capturedTools map[int]bool
		if literal := functionLiteralExpression(expression); literal != nil {
			closure := indexedAppFunction{
				literal:     literal,
				imports:     function.imports,
				seedOrigins: origins,
				seedValues:  values,
				captureOnly: true,
			}
			capturedCleanup = inferCleanupParameters(closure, types)
			capturedTools = inferToolRegistrationParameters(closure, types)
		}
		for _, embedded := range compositeFunctionLiterals(expression, "", function.imports, types) {
			closure := indexedAppFunction{
				literal:     embedded.literal,
				imports:     function.imports,
				seedOrigins: origins,
				seedValues:  values,
				captureOnly: true,
			}
			embeddedCleanup := inferCleanupParameters(closure, types)
			embeddedTools := inferToolRegistrationParameters(closure, types)
			path := compositeCallablePath(embedded.suffix)
			if len(embeddedCleanup) != 0 {
				if cleanupResults[index] == nil {
					cleanupResults[index] = make(map[string]bool)
				}
				callPath := callableCleanupPath
				if path != "" {
					callPath = path + "." + callableCleanupPath
				}
				cleanupResults[index][callPath] = true
			}
			if len(embeddedTools) != 0 {
				if toolCallPaths[index] == nil {
					toolCallPaths[index] = make(map[string]bool)
				}
				toolCallPaths[index][path] = true
			}
			if resultParams[index] == nil {
				resultParams[index] = make(map[int]bool)
			}
			if resultParamPaths[index] == nil {
				resultParamPaths[index] = make(map[int]map[string]bool)
			}
			for parameterIndex := range embeddedCleanup {
				resultParams[index][parameterIndex] = true
				if resultParamPaths[index][parameterIndex] == nil {
					resultParamPaths[index][parameterIndex] = make(map[string]bool)
				}
				resultParamPaths[index][parameterIndex][path] = true
			}
			for parameterIndex := range embeddedTools {
				resultParams[index][parameterIndex] = true
				if resultParamPaths[index][parameterIndex] == nil {
					resultParamPaths[index][parameterIndex] = make(map[string]bool)
				}
				resultParamPaths[index][parameterIndex][path] = true
			}
		}
		if paths := assignedCleanupPaths([]ast.Expr{expression}, expressionResultIndex, function.imports, values, resources, concreteTypes); paths != nil {
			if cleanupResults[index] == nil {
				cleanupResults[index] = make(map[string]bool)
			}
			for path := range paths {
				cleanupResults[index][path] = true
			}
		}
		if len(capturedCleanup) != 0 || isCleanupCallableResultExpression(expression, expressionResultIndex, function.imports, values, origins, types) {
			if cleanupResults[index] == nil {
				cleanupResults[index] = make(map[string]bool)
			}
			cleanupResults[index][callableCleanupPath] = true
		}
		if isToolRegistryResultExpression(expression, expressionResultIndex, function.imports, values, concreteTypes) {
			toolResults[index] = true
		}
		if len(capturedTools) != 0 || isToolRegistrationCallableResultExpression(expression, expressionResultIndex, function.imports, values, origins, types) {
			toolCallResults[index] = true
		}
		if resultParams[index] == nil {
			resultParams[index] = make(map[int]bool)
		}
		mergeOrigins(resultParams[index], resultParameterOrigins(expression, function.imports, values, origins, types, expressionResultIndex))
		mergeOrigins(resultParams[index], capturedCleanup)
		mergeOrigins(resultParams[index], capturedTools)
		paths := resultParameterPaths(expression, function.imports, values, origins, types, expressionResultIndex)
		if resultParamPaths[index] == nil {
			resultParamPaths[index] = make(map[int]map[string]bool)
		}
		for parameterIndex, prefixes := range paths {
			resultParams[index][parameterIndex] = true
			if resultParamPaths[index][parameterIndex] == nil {
				resultParamPaths[index][parameterIndex] = make(map[string]bool)
			}
			for prefix := range prefixes {
				resultParamPaths[index][parameterIndex][prefix] = true
			}
		}
	}
	ast.Inspect(function.body(), func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for index, left := range value.Lhs {
				trackReferenceAssignment(references, aliases, left, nil, value.Rhs, index, function.imports, types)
				name, indexed := assignmentBinding(left)
				if name == nil {
					continue
				}
				key := bindingKey(name)
				assignedOrigins := assignedResultParameterOrigins(value.Rhs, index, function.imports, values, origins, types)
				if isMutatingAssignmentTarget(left) && references[key] {
					recordParameterWrite(originsForExpression(left, origins), assignedOrigins, assignmentFieldPrefix(left))
					if mapKey := mapAssignmentKey(left, function.imports, values, types); mapKey != nil {
						recordParameterWrite(originsForExpression(left, origins), originsForExpression(mapKey, origins), assignmentFieldPrefix(left)+mapKeyPathPrefix)
					}
				}
				for alias := range referenceAliasKeys(aliases, key) {
					mergeBindingOrigins(origins, alias, assignedOrigins)
				}
				trackCompositeFunctionTypes(values, function.key, left, value.Rhs, index, function.imports, concreteTypes)
				if localType := localFunctionType(function.key, left, value.Rhs, index); localType != "" {
					setMayValueType(values, assignmentValueKey(left), localType, concreteTypes)
				} else if !indexed {
					if paths := assignedCleanupPaths(value.Rhs, index, function.imports, values, resources, concreteTypes); paths != nil {
						resources[key] = paths
					}
					typeName := assignedToolExpressionType(value.Rhs, index, function.imports, values, concreteTypes)
					setMayValueType(values, key, typeName, concreteTypes)
				}
			}
		case *ast.DeclStmt:
			general, ok := value.Decl.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				break
			}
			for _, raw := range general.Specs {
				spec := raw.(*ast.ValueSpec)
				for index, name := range spec.Names {
					trackReferenceAssignment(references, aliases, name, spec.Type, spec.Values, index, function.imports, types)
					key := bindingKey(name)
					assignedOrigins := assignedResultParameterOrigins(spec.Values, index, function.imports, values, origins, types)
					for alias := range referenceAliasKeys(aliases, key) {
						mergeBindingOrigins(origins, alias, assignedOrigins)
					}
					paths := cleanupPathsForType(canonicalType(spec.Type, function.imports), concreteTypes, nil)
					typeName := canonicalType(spec.Type, function.imports)
					if len(spec.Values) != 0 {
						paths = assignedCleanupPaths(spec.Values, index, function.imports, values, resources, concreteTypes)
						typeName = assignedToolExpressionType(spec.Values, index, function.imports, values, concreteTypes)
					}
					if paths != nil {
						resources[key] = paths
					}
					trackCompositeFunctionTypes(values, function.key, name, spec.Values, index, function.imports, concreteTypes)
					if localType := localFunctionType(function.key, name, spec.Values, index); localType != "" {
						typeName = localType
					}
					setMayValueType(values, key, typeName, concreteTypes)
				}
			}
		case *ast.RangeStmt:
			origin := resultParameterOrigins(value.X, function.imports, values, origins, types, 0)
			collectionPaths := cleanupPathsForExpression(value.X, function.imports, values, resources, concreteTypes)
			collectionType := resolveNamedType(expressionType(value.X, function.imports, values, concreteTypes), concreteTypes)
			keyType, valueType, ok := rangeTypes(collectionType, concreteTypes)
			if !ok {
				keyPaths, valuePaths, iterator := iteratorRangeCleanupPaths(value.X, collectionPaths, function.imports)
				if iterator {
					if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" {
						key := bindingKey(name)
						mergeBindingOrigins(origins, key, origin)
						if keyPaths != nil {
							resources[key] = keyPaths
						}
					}
					if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" {
						key := bindingKey(name)
						mergeBindingOrigins(origins, key, origin)
						if valuePaths != nil {
							resources[key] = valuePaths
						}
					}
				}
				break
			}
			if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" {
				key := bindingKey(name)
				mergeBindingOrigins(origins, key, origin)
				values[key] = keyType
				if paths := cleanupPathsForType(keyType, concreteTypes, nil); paths != nil {
					resources[key] = paths
				} else if strings.HasPrefix(collectionType, mapTypePrefix) {
					if paths := cleanupMapKeyPaths(collectionPaths); paths != nil {
						resources[key] = paths
					}
				} else if strings.HasPrefix(collectionType, channelTypePrefix) && collectionPaths != nil {
					resources[key] = collectionPaths
				}
			}
			if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" {
				key := bindingKey(name)
				mergeBindingOrigins(origins, key, origin)
				values[key] = valueType
				if paths := cleanupPathsForType(valueType, concreteTypes, nil); paths != nil {
					resources[key] = paths
				} else if paths := cleanupMapValuePaths(collectionPaths); paths != nil {
					resources[key] = paths
				}
			}
		case *ast.CallExpr:
			if isCollectionWrite(value, function.imports) {
				destination := value.Args[0]
				source := value.Args[1]
				recordParameterWrite(originsForExpression(destination, origins), originsForExpression(source, origins), assignmentFieldPrefix(destination))
				if destinationName, _ := assignmentBinding(destination); destinationName != nil {
					paths := cleanupPathsForExpression(source, function.imports, values, resources, concreteTypes)
					mergeAliasedCleanupPaths(resources, aliases, bindingKey(destinationName), paths)
				}
			}
			callee := calledFunctionKey(value.Fun, function.imports, values, types)
			for destinationIndex, sources := range types.parameterWrites[callee] {
				for _, destination := range callArgumentsForParameter(value, callee, destinationIndex, function.imports, types) {
					destinationName, _ := assignmentBinding(destination)
					if destinationName == nil {
						continue
					}
					destinationKey := bindingKey(destinationName)
					for sourceIndex, prefixes := range sources {
						for _, source := range callArgumentsForParameter(value, callee, sourceIndex, function.imports, types) {
							sourceOrigins := originsForExpression(source, origins)
							for prefix := range prefixes {
								recordParameterWrite(originsForExpression(destination, origins), sourceOrigins, assignmentFieldPrefix(destination)+prefix)
							}
							for alias := range referenceAliasKeys(aliases, destinationKey) {
								mergeBindingOrigins(origins, alias, sourceOrigins)
							}
						}
					}
				}
			}
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(value.Results) == 0 {
				for index, name := range namedResultIdentifiers(function.results()) {
					if name != nil {
						recordResult(index, name, 0)
					}
				}
				break
			}
			if len(value.Results) == 1 {
				if _, ok := value.Results[0].(*ast.CallExpr); ok && resultCount(function.results()) > 1 {
					for index := range resultCount(function.results()) {
						recordResult(index, value.Results[0], index)
					}
					break
				}
			}
			for index, expression := range value.Results {
				recordResult(index, expression, 0)
			}
		}
		return true
	})
	return cleanupResults, toolResults, toolCallResults, toolCallPaths, resultParams, resultParamPaths, parameterWrites
}

func inferenceState(function indexedAppFunction) (map[string]map[int]bool, map[string]string) {
	origins := make(map[string]map[int]bool)
	if function.seedOrigins == nil {
		origins = functionParameterOrigins(function)
	} else {
		for key, source := range function.seedOrigins {
			origins[key] = make(map[int]bool, len(source))
			mergeOrigins(origins[key], source)
		}
	}
	values := make(map[string]string)
	for key, typeName := range function.seedValues {
		values[key] = typeName
	}
	for name, typeName := range namedValueTypes(function.receiver(), function.imports) {
		values[name] = typeName
	}
	for name, typeName := range namedValueTypes(function.parameters(), function.imports) {
		values[name] = typeName
	}
	return origins, values
}

func inferCleanupParameters(function indexedAppFunction, types compositionTypeIndex) map[int]bool {
	cleaned := make(map[int]bool)
	origins, values := inferenceState(function)
	references := functionReferenceValues(function, types)
	aliases := make(map[string]map[string]bool)
	ast.Inspect(function.body(), func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for index, left := range value.Lhs {
				trackReferenceAssignment(references, aliases, left, nil, value.Rhs, index, function.imports, types)
				name, indexed := assignmentBinding(left)
				if name == nil {
					continue
				}
				key := bindingKey(name)
				assignedOrigins := assignedResultParameterOrigins(value.Rhs, index, function.imports, values, origins, types)
				for alias := range referenceAliasKeys(aliases, key) {
					mergeBindingOrigins(origins, alias, assignedOrigins)
				}
				trackCompositeFunctionTypes(values, function.key, left, value.Rhs, index, function.imports, types)
				if localType := localFunctionType(function.key, left, value.Rhs, index); localType != "" {
					setMayValueType(values, assignmentValueKey(left), localType, types)
				} else if !indexed {
					typeName := assignedExpressionType(value.Rhs, index, function.imports, values, types)
					setMayValueType(values, key, typeName, types)
				}
			}
		case *ast.DeclStmt:
			general, ok := value.Decl.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				break
			}
			for _, raw := range general.Specs {
				spec := raw.(*ast.ValueSpec)
				for index, name := range spec.Names {
					trackReferenceAssignment(references, aliases, name, spec.Type, spec.Values, index, function.imports, types)
					key := bindingKey(name)
					assignedOrigins := assignedResultParameterOrigins(spec.Values, index, function.imports, values, origins, types)
					for alias := range referenceAliasKeys(aliases, key) {
						mergeBindingOrigins(origins, alias, assignedOrigins)
					}
					typeName := assignedExpressionType(spec.Values, index, function.imports, values, types)
					trackCompositeFunctionTypes(values, function.key, name, spec.Values, index, function.imports, types)
					if localType := localFunctionType(function.key, name, spec.Values, index); localType != "" {
						typeName = localType
					}
					setMayValueType(values, key, typeName, types)
				}
			}
		case *ast.RangeStmt:
			origin := resultParameterOrigins(value.X, function.imports, values, origins, types, 0)
			keyType, valueType, ok := rangeTypes(expressionType(value.X, function.imports, values, types), types)
			if !ok {
				arity := valueIteratorArity(value.X, function.imports)
				if arity == 0 {
					break
				}
				target := value.Key
				if arity == 2 {
					target = value.Value
				}
				if name, ok := target.(*ast.Ident); ok && name.Name != "_" {
					mergeBindingOrigins(origins, bindingKey(name), origin)
				}
				break
			}
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
			callee := calledFunctionKey(value.Fun, function.imports, values, types)
			for destinationIndex, sources := range types.parameterWrites[callee] {
				for _, destination := range callArgumentsForParameter(value, callee, destinationIndex, function.imports, types) {
					destinationName, _ := assignmentBinding(destination)
					if destinationName == nil {
						continue
					}
					for sourceIndex := range sources {
						for _, source := range callArgumentsForParameter(value, callee, sourceIndex, function.imports, types) {
							for alias := range referenceAliasKeys(aliases, bindingKey(destinationName)) {
								mergeBindingOrigins(origins, alias, originsForExpression(source, origins))
							}
						}
					}
				}
			}
			callbackOrigins := callableOrigins(value.Fun, origins)
			if !function.captureOnly {
				mergeOrigins(cleaned, callbackOrigins)
			}
			mergeOrigins(cleaned, callbackArgumentOrigins(value, origins))
			if selector, ok := value.Fun.(*ast.SelectorExpr); ok && isFeatureCleanupMethodName(selector.Sel.Name) {
				if isFeatureCleanupMethodExpression(selector, function.imports, types) && len(value.Args) != 0 {
					mergeOrigins(cleaned, originsForExpression(value.Args[0], origins))
				} else {
					mergeOrigins(cleaned, originsForExpression(selector.X, origins))
				}
			}
			for index := range types.cleanupParams[callee] {
				for _, argument := range callArgumentsForParameter(value, callee, index, function.imports, types) {
					mergeOrigins(cleaned, originsForExpression(argument, origins))
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
	origins, values := inferenceState(function)
	references := functionReferenceValues(function, types)
	aliases := make(map[string]map[string]bool)
	ast.Inspect(function.body(), func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for index, left := range value.Lhs {
				trackReferenceAssignment(references, aliases, left, nil, value.Rhs, index, function.imports, types)
				name, indexed := assignmentBinding(left)
				if name == nil {
					continue
				}
				key := bindingKey(name)
				assignedOrigins := assignedResultParameterOrigins(value.Rhs, index, function.imports, values, origins, types)
				for alias := range referenceAliasKeys(aliases, key) {
					mergeBindingOrigins(origins, alias, assignedOrigins)
				}
				trackCompositeFunctionTypes(values, function.key, left, value.Rhs, index, function.imports, types)
				if localType := localFunctionType(function.key, left, value.Rhs, index); localType != "" {
					setMayValueType(values, assignmentValueKey(left), localType, types)
				} else if !indexed {
					typeName := assignedExpressionType(value.Rhs, index, function.imports, values, types)
					setMayValueType(values, key, typeName, types)
				}
			}
		case *ast.DeclStmt:
			general, ok := value.Decl.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				break
			}
			for _, raw := range general.Specs {
				spec := raw.(*ast.ValueSpec)
				for index, name := range spec.Names {
					trackReferenceAssignment(references, aliases, name, spec.Type, spec.Values, index, function.imports, types)
					key := bindingKey(name)
					assignedOrigins := assignedResultParameterOrigins(spec.Values, index, function.imports, values, origins, types)
					for alias := range referenceAliasKeys(aliases, key) {
						mergeBindingOrigins(origins, alias, assignedOrigins)
					}
					typeName := assignedExpressionType(spec.Values, index, function.imports, values, types)
					trackCompositeFunctionTypes(values, function.key, name, spec.Values, index, function.imports, types)
					if localType := localFunctionType(function.key, name, spec.Values, index); localType != "" {
						typeName = localType
					}
					setMayValueType(values, key, typeName, types)
				}
			}
		case *ast.RangeStmt:
			origin := resultParameterOrigins(value.X, function.imports, values, origins, types, 0)
			keyType, valueType, ok := rangeTypes(expressionType(value.X, function.imports, values, types), types)
			if !ok {
				keyType, valueType, iterator := iteratorRangeToolTypes(value.X, function.imports, values, types)
				if !iterator {
					break
				}
				if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" {
					key := bindingKey(name)
					mergeBindingOrigins(origins, key, origin)
					if keyType != "" {
						values[key] = keyType
					}
				}
				if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" {
					key := bindingKey(name)
					mergeBindingOrigins(origins, key, origin)
					if valueType != "" {
						values[key] = valueType
					}
				}
				break
			}
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
			callee := calledFunctionKey(value.Fun, function.imports, values, types)
			for destinationIndex, sources := range types.parameterWrites[callee] {
				for _, destination := range callArgumentsForParameter(value, callee, destinationIndex, function.imports, types) {
					destinationName, _ := assignmentBinding(destination)
					if destinationName == nil {
						continue
					}
					for sourceIndex := range sources {
						for _, source := range callArgumentsForParameter(value, callee, sourceIndex, function.imports, types) {
							for alias := range referenceAliasKeys(aliases, bindingKey(destinationName)) {
								mergeBindingOrigins(origins, alias, originsForExpression(source, origins))
							}
						}
					}
				}
			}
			callbackOrigins := callableOrigins(value.Fun, origins)
			if !function.captureOnly {
				mergeOrigins(registered, callbackOrigins)
			}
			mergeOrigins(registered, callbackArgumentOrigins(value, origins))
			if selector, ok := value.Fun.(*ast.SelectorExpr); ok && isToolRegistrationName(selector.Sel.Name) {
				if isToolRegistrationMethodExpression(selector, function.imports, types) {
					if len(value.Args) != 0 {
						mergeOrigins(registered, originsForExpression(value.Args[0], origins))
					}
				} else if qualifier, ok := selector.X.(*ast.Ident); ok && function.imports[qualifier.Name] == modulePath+"/internal/tools" {
					if len(value.Args) != 0 {
						mergeOrigins(registered, originsForExpression(value.Args[0], origins))
					}
				} else {
					mergeOrigins(registered, originsForExpression(selector.X, origins))
				}
			}
			for index := range types.toolParams[callee] {
				for _, argument := range callArgumentsForParameter(value, callee, index, function.imports, types) {
					mergeOrigins(registered, originsForExpression(argument, origins))
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

func functionParameterOrigins(function indexedAppFunction) map[string]map[int]bool {
	origins := parameterOrigins(function.parameters())
	if function.receiver() == nil {
		return origins
	}
	for _, field := range function.receiver().List {
		receiverType := canonicalType(field.Type, function.imports)
		if receiverType == modulePath+"/internal/app.App" || appFeatureCleanupOwners[receiverType] {
			continue
		}
		for _, name := range field.Names {
			origins[bindingKey(name)] = map[int]bool{receiverParameterIndex: true}
		}
	}
	return origins
}

func functionReferenceValues(function indexedAppFunction, types compositionTypeIndex) map[string]bool {
	references := namedReferenceValues(function.receiver(), function.imports, types)
	for key := range namedReferenceValues(function.parameters(), function.imports, types) {
		references[key] = true
	}
	return references
}

func namedReferenceValues(fields *ast.FieldList, imports map[string]string, types compositionTypeIndex) map[string]bool {
	references := make(map[string]bool)
	if fields == nil {
		return references
	}
	for _, field := range fields.List {
		typeName := resolveNamedType(canonicalType(field.Type, imports), types)
		if !isReferenceTypeExpression(field.Type) && !isReferenceTypeName(typeName) {
			continue
		}
		for _, name := range field.Names {
			references[bindingKey(name)] = true
		}
	}
	return references
}

func isReferenceTypeName(typeName string) bool {
	return strings.HasPrefix(typeName, sliceTypePrefix) || strings.HasPrefix(typeName, mapTypePrefix) || strings.HasPrefix(typeName, channelTypePrefix)
}

func originsForExpression(expression ast.Expr, origins map[string]map[int]bool) map[int]bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return origins[bindingKey(value)]
	case *ast.ParenExpr:
		return originsForExpression(value.X, origins)
	case *ast.SelectorExpr:
		return originsForExpression(value.X, origins)
	case *ast.IndexExpr:
		return originsForExpression(value.X, origins)
	case *ast.SliceExpr:
		return originsForExpression(value.X, origins)
	case *ast.UnaryExpr:
		return originsForExpression(value.X, origins)
	case *ast.StarExpr:
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

func callbackArgumentOrigins(call *ast.CallExpr, origins map[string]map[int]bool) map[int]bool {
	if len(callableOrigins(call.Fun, origins)) == 0 {
		return nil
	}
	// A function-valued parameter may clean or mutate every helper argument it receives.
	result := make(map[int]bool)
	for _, argument := range call.Args {
		mergeOrigins(result, originsForExpression(argument, origins))
	}
	return result
}

func callArgumentsForParameter(call *ast.CallExpr, callee string, parameterIndex int, imports map[string]string, types compositionTypeIndex) []ast.Expr {
	variadicIndex, variadic := types.variadicParams[callee]
	return callArgumentsForParameterAt(call, parameterIndex, variadicIndex, variadic, imports)
}

func callArgumentsForParameterAt(call *ast.CallExpr, parameterIndex, variadicIndex int, variadic bool, imports map[string]string) []ast.Expr {
	selector := calledSelector(call.Fun)
	methodExpression := selector != nil && isTypeExpression(selector.X, imports)
	if parameterIndex == receiverParameterIndex {
		if selector == nil {
			return nil
		}
		if methodExpression {
			if len(call.Args) == 0 {
				return nil
			}
			return call.Args[:1]
		}
		return []ast.Expr{selector.X}
	}
	argumentIndex := parameterIndex
	if methodExpression {
		argumentIndex++
	}
	if argumentIndex >= len(call.Args) {
		return nil
	}
	end := argumentIndex + 1
	if variadic && parameterIndex == variadicIndex {
		end = len(call.Args)
	}
	return call.Args[argumentIndex:end]
}

func calledSelector(expression ast.Expr) *ast.SelectorExpr {
	switch value := expression.(type) {
	case *ast.SelectorExpr:
		return value
	case *ast.ParenExpr:
		return calledSelector(value.X)
	case *ast.IndexExpr:
		return calledSelector(value.X)
	case *ast.IndexListExpr:
		return calledSelector(value.X)
	default:
		return nil
	}
}

func resultParameterOrigins(expression ast.Expr, imports map[string]string, values map[string]string, origins map[string]map[int]bool, types compositionTypeIndex, resultIndex int) map[int]bool {
	result := make(map[int]bool)
	mergeOrigins(result, originsForExpression(expression, origins))
	switch value := expression.(type) {
	case *ast.ParenExpr:
		mergeOrigins(result, resultParameterOrigins(value.X, imports, values, origins, types, resultIndex))
	case *ast.TypeAssertExpr:
		mergeOrigins(result, resultParameterOrigins(value.X, imports, values, origins, types, 0))
	case *ast.CallExpr:
		callee := calledFunctionKey(value.Fun, imports, values, types)
		parameters := types.resultParams[callee][resultIndex]
		for parameterIndex := range parameters {
			for _, argument := range callArgumentsForParameter(value, callee, parameterIndex, imports, types) {
				mergeOrigins(result, resultParameterOrigins(argument, imports, values, origins, types, 0))
			}
		}
		if len(parameters) == 0 && len(value.Args) == 1 {
			mergeOrigins(result, resultParameterOrigins(value.Args[0], imports, values, origins, types, 0))
		}
	}
	return result
}

func resultParameterPaths(expression ast.Expr, imports map[string]string, values map[string]string, origins map[string]map[int]bool, types compositionTypeIndex, resultIndex int) map[int]map[string]bool {
	result := make(map[int]map[string]bool)
	addOriginsWithPrefix(result, originsForExpression(expression, origins), "")
	switch value := expression.(type) {
	case *ast.ParenExpr:
		mergeResultParameterPaths(result, resultParameterPaths(value.X, imports, values, origins, types, resultIndex), "")
	case *ast.TypeAssertExpr:
		mergeResultParameterPaths(result, resultParameterPaths(value.X, imports, values, origins, types, 0), "")
	case *ast.SelectorExpr:
		mergeResultParameterPaths(result, resultParameterPaths(value.X, imports, values, origins, types, 0), "")
	case *ast.IndexExpr:
		mergeResultParameterPaths(result, resultParameterPaths(value.X, imports, values, origins, types, 0), "")
	case *ast.SliceExpr:
		mergeResultParameterPaths(result, resultParameterPaths(value.X, imports, values, origins, types, 0), "")
	case *ast.UnaryExpr:
		mergeResultParameterPaths(result, resultParameterPaths(value.X, imports, values, origins, types, 0), "")
	case *ast.StarExpr:
		mergeResultParameterPaths(result, resultParameterPaths(value.X, imports, values, origins, types, 0), "")
	case *ast.CompositeLit:
		typeName := canonicalType(value.Type, imports)
		fieldOrder, structured := compositeFieldOrder(value.Type, typeName, imports, types)
		for index, element := range value.Elts {
			prefix := ""
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				if field, ok := pair.Key.(*ast.Ident); structured && ok {
					prefix = field.Name + "."
				}
				element = pair.Value
			} else if structured && index < len(fieldOrder) && !strings.HasPrefix(fieldOrder[index], embeddedPrefix) {
				prefix = fieldOrder[index] + "."
			}
			mergeResultParameterPaths(result, resultParameterPaths(element, imports, values, origins, types, 0), prefix)
		}
	case *ast.CallExpr:
		callee := calledFunctionKey(value.Fun, imports, values, types)
		for parameterIndex, prefixes := range resultParameterPathSummary(callee, resultIndex, types) {
			for _, argument := range callArgumentsForParameter(value, callee, parameterIndex, imports, types) {
				argumentPaths := resultParameterPaths(argument, imports, values, origins, types, 0)
				for prefix := range prefixes {
					mergeResultParameterPaths(result, argumentPaths, prefix)
				}
			}
		}
	}
	return result
}

func resultParameterPathSummary(callee string, resultIndex int, types compositionTypeIndex) map[int]map[string]bool {
	if paths := types.resultParamPaths[callee][resultIndex]; len(paths) != 0 {
		return paths
	}
	parameters := types.resultParams[callee][resultIndex]
	if len(parameters) == 0 {
		return nil
	}
	paths := make(map[int]map[string]bool, len(parameters))
	for parameterIndex := range parameters {
		paths[parameterIndex] = map[string]bool{"": true}
	}
	return paths
}

func addOriginsWithPrefix(destination map[int]map[string]bool, origins map[int]bool, prefix string) {
	for parameterIndex := range origins {
		if destination[parameterIndex] == nil {
			destination[parameterIndex] = make(map[string]bool)
		}
		destination[parameterIndex][prefix] = true
	}
}

func mergeResultParameterPaths(destination, source map[int]map[string]bool, prefix string) {
	for parameterIndex, paths := range source {
		if destination[parameterIndex] == nil {
			destination[parameterIndex] = make(map[string]bool)
		}
		for path := range paths {
			destination[parameterIndex][prefix+path] = true
		}
	}
}

func assignedResultParameterOrigins(expressions []ast.Expr, index int, imports map[string]string, values map[string]string, origins map[string]map[int]bool, types compositionTypeIndex) map[int]bool {
	if len(expressions) == 1 {
		return resultParameterOrigins(expressions[0], imports, values, origins, types, index)
	}
	if index >= len(expressions) {
		return nil
	}
	return resultParameterOrigins(expressions[index], imports, values, origins, types, 0)
}

func isCleanupCallableResultExpression(expression ast.Expr, resultIndex int, imports map[string]string, values map[string]string, origins map[string]map[int]bool, types compositionTypeIndex) bool {
	if call, ok := expression.(*ast.CallExpr); ok {
		callee := calledFunctionKey(call.Fun, imports, values, types)
		return types.cleanupResults[callee][resultIndex][callableCleanupPath]
	}
	if resultIndex != 0 {
		return false
	}
	selector := methodValueSelector(expression)
	return selector != nil && isFeatureCleanupMethodName(selector.Sel.Name) && len(resultParameterOrigins(selector.X, imports, values, origins, types, 0)) != 0
}

func isToolRegistrationCallableResultExpression(expression ast.Expr, resultIndex int, imports map[string]string, values map[string]string, origins map[string]map[int]bool, types compositionTypeIndex) bool {
	if call, ok := expression.(*ast.CallExpr); ok {
		callee := calledFunctionKey(call.Fun, imports, values, types)
		return types.toolCallResults[callee][resultIndex]
	}
	if resultIndex != 0 {
		return false
	}
	if isToolRegistrationCallableExpression(expression, imports, values, types) {
		return true
	}
	selector := methodValueSelector(expression)
	return selector != nil && isToolRegistrationName(selector.Sel.Name) && len(resultParameterOrigins(selector.X, imports, values, origins, types, 0)) != 0
}

func methodValueSelector(expression ast.Expr) *ast.SelectorExpr {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			break
		}
		expression = parenthesized.X
	}
	selector, _ := expression.(*ast.SelectorExpr)
	return selector
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

func resultCount(results *ast.FieldList) int {
	if results == nil {
		return 0
	}
	count := 0
	for _, field := range results.List {
		if len(field.Names) == 0 {
			count++
		} else {
			count += len(field.Names)
		}
	}
	return count
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
		values, resources := packageBindingState(file, imports, types)
		for name, typeName := range namedValueTypes(function.Recv, imports) {
			values[name] = typeName
		}
		for name, typeName := range namedValueTypes(function.Type.Params, imports) {
			values[name] = typeName
		}
		for name, paths := range namedCleanupValues(function.Type.Params, imports, types) {
			resources[name] = paths
		}
		for name, paths := range namedCleanupValues(function.Recv, imports, types) {
			resources[name] = paths
		}
		references := namedReferenceValues(function.Recv, imports, types)
		for key := range namedReferenceValues(function.Type.Params, imports, types) {
			references[key] = true
		}
		aliases := make(map[string]map[string]bool)
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.AssignStmt:
				for index, left := range value.Lhs {
					trackReferenceAssignment(references, aliases, left, nil, value.Rhs, index, imports, types)
					name, indexed := assignmentBinding(left)
					if name == nil {
						continue
					}
					key := bindingKey(name)
					paths := assignedCleanupPaths(value.Rhs, index, imports, values, resources, types)
					if mapKey := mapAssignmentKey(left, imports, values, types); mapKey != nil {
						keyPaths := cleanupPathsForExpression(mapKey, imports, values, resources, types)
						paths = mergeCleanupPaths(paths, prefixCleanupPaths(mapKeyPathPrefix, keyPaths))
					}
					if paths != nil {
						paths = prefixCleanupPaths(assignmentFieldPrefix(left), paths)
						mergeAliasedCleanupPaths(resources, aliases, key, paths)
					}
					trackCompositeFunctionTypes(values, functionKey, left, value.Rhs, index, imports, types)
					if localType := localFunctionType(functionKey, left, value.Rhs, index); localType != "" {
						setMayValueType(values, assignmentValueKey(left), localType, types)
					} else if !indexed {
						typeName := assignedExpressionType(value.Rhs, index, imports, values, types)
						setMayValueType(values, key, typeName, types)
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
						trackReferenceAssignment(references, aliases, name, spec.Type, spec.Values, index, imports, types)
						key := bindingKey(name)
						if len(spec.Values) != 0 {
							paths = assignedCleanupPaths(spec.Values, index, imports, values, resources, types)
						}
						if paths != nil {
							mergeAliasedCleanupPaths(resources, aliases, key, paths)
						}
						typeName := canonicalType(spec.Type, imports)
						if len(spec.Values) != 0 {
							typeName = assignedExpressionType(spec.Values, index, imports, values, types)
						}
						trackCompositeFunctionTypes(values, functionKey, name, spec.Values, index, imports, types)
						if localType := localFunctionType(functionKey, name, spec.Values, index); localType != "" {
							typeName = localType
						}
						setMayValueType(values, key, typeName, types)
					}
				}
			case *ast.RangeStmt:
				collectionPaths := cleanupPathsForExpression(value.X, imports, values, resources, types)
				collectionType := resolveNamedType(expressionType(value.X, imports, values, types), types)
				keyType, valueType, ok := rangeTypes(collectionType, types)
				if !ok {
					keyPaths, valuePaths, iterator := iteratorRangeCleanupPaths(value.X, collectionPaths, imports)
					if iterator {
						if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" && keyPaths != nil {
							resources[bindingKey(name)] = keyPaths
						}
						if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" && valuePaths != nil {
							resources[bindingKey(name)] = valuePaths
						}
					}
					break
				}
				if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" {
					key := bindingKey(name)
					values[key] = keyType
					if paths := cleanupPathsForType(keyType, types, nil); paths != nil {
						resources[key] = paths
					} else if strings.HasPrefix(collectionType, mapTypePrefix) {
						if paths := cleanupMapKeyPaths(collectionPaths); paths != nil {
							resources[key] = paths
						}
					} else if strings.HasPrefix(collectionType, channelTypePrefix) && collectionPaths != nil {
						resources[key] = collectionPaths
					}
				}
				if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" {
					key := bindingKey(name)
					values[key] = valueType
					if paths := cleanupPathsForType(valueType, types, nil); paths != nil {
						resources[key] = paths
					} else if paths := cleanupMapValuePaths(collectionPaths); paths != nil {
						resources[key] = paths
					}
				}
			case *ast.SendStmt:
				name, _ := assignmentBinding(value.Chan)
				if name != nil {
					paths := cleanupPathsForExpression(value.Value, imports, values, resources, types)
					paths = prefixCleanupPaths(assignmentFieldPrefix(value.Chan), paths)
					key := bindingKey(name)
					mergeAliasedCleanupPaths(resources, aliases, key, paths)
				}
			case *ast.CallExpr:
				if isCollectionWrite(value, imports) {
					if destination, _ := assignmentBinding(value.Args[0]); destination != nil {
						paths := cleanupPathsForExpression(value.Args[1], imports, values, resources, types)
						key := bindingKey(destination)
						mergeAliasedCleanupPaths(resources, aliases, key, paths)
					}
				}
				callee := calledFunctionKey(value.Fun, imports, values, types)
				for destinationIndex, sources := range types.parameterWrites[callee] {
					for _, destination := range callArgumentsForParameter(value, callee, destinationIndex, imports, types) {
						destinationName, _ := assignmentBinding(destination)
						if destinationName == nil {
							continue
						}
						destinationKey := bindingKey(destinationName)
						for sourceIndex, prefixes := range sources {
							for _, source := range callArgumentsForParameter(value, callee, sourceIndex, imports, types) {
								sourcePaths := cleanupPathsForExpression(source, imports, values, resources, types)
								for prefix := range prefixes {
									paths := prefixCleanupPaths(assignmentFieldPrefix(destination)+prefix, sourcePaths)
									mergeAliasedCleanupPaths(resources, aliases, destinationKey, paths)
								}
							}
						}
					}
				}
				cleanupParams := types.cleanupParams[callee]
				variadicIndex, variadic := types.variadicParams[callee]
				if literal := functionLiteralExpression(value.Fun); literal != nil {
					cleanupParams = inferCleanupParameters(indexedAppFunction{literal: literal, imports: imports}, types)
					variadicIndex, variadic = variadicParameterIndex(literal.Type.Params)
				}
				reported := false
				for index := range cleanupParams {
					for _, argument := range callArgumentsForParameterAt(value, index, variadicIndex, variadic, imports) {
						if cleanupPathsForExpression(argument, imports, values, resources, types) != nil {
							report(value, selectorChain(value.Fun))
							reported = true
							break
						}
					}
					if reported {
						break
					}
				}
				if reported {
					break
				}
				selector, selectorCall := value.Fun.(*ast.SelectorExpr)
				methodExpression := selectorCall && isFeatureCleanupMethodExpression(selector, imports, types)
				if !methodExpression && cleanupPathsForExpression(value.Fun, imports, values, resources, types)[callableCleanupPath] {
					report(value, selectorChain(value.Fun))
					break
				}
				if !selectorCall {
					break
				}
				if methodExpression && len(value.Args) != 0 {
					if cleanupPathsForExpression(value.Args[0], imports, values, resources, types) != nil {
						report(value, selectorChain(selector))
					}
					break
				}
				paths := cleanupPathsForExpression(selector.X, imports, values, resources, types)
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
		values, _ := packageBindingState(file, imports, types)
		for name, typeName := range namedValueTypes(function.Recv, imports) {
			values[name] = typeName
		}
		for name, typeName := range namedValueTypes(function.Type.Params, imports) {
			values[name] = typeName
		}
		references := namedReferenceValues(function.Recv, imports, types)
		for key := range namedReferenceValues(function.Type.Params, imports, types) {
			references[key] = true
		}
		aliases := make(map[string]map[string]bool)
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.AssignStmt:
				for index, left := range value.Lhs {
					trackReferenceAssignment(references, aliases, left, nil, value.Rhs, index, imports, types)
					name, indexed := assignmentBinding(left)
					if name == nil {
						continue
					}
					key := bindingKey(name)
					trackCompositeFunctionTypes(values, functionKey, left, value.Rhs, index, imports, types)
					if localType := localFunctionType(functionKey, left, value.Rhs, index); localType != "" {
						setMayValueType(values, assignmentValueKey(left), localType, types)
						continue
					}
					typeName := assignedToolExpressionType(value.Rhs, index, imports, values, types)
					if indexed && resolveNamedType(typeName, types) == modulePath+"/internal/tools.Registry" {
						typeName = toolRegistryCollectionType
					}
					if mapKey := mapAssignmentKey(left, imports, values, types); mapKey != nil && isToolRegistryExpression(mapKey, imports, values, types) {
						typeName = mergeToolRegistryCollectionTypes(typeName, toolRegistryMapKeyCollectionType)
					}
					setAliasedMayValueType(values, aliases, key, typeName, types)
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
						trackReferenceAssignment(references, aliases, name, spec.Type, spec.Values, index, imports, types)
						if len(spec.Values) != 0 {
							typeName = assignedToolExpressionType(spec.Values, index, imports, values, types)
						}
						trackCompositeFunctionTypes(values, functionKey, name, spec.Values, index, imports, types)
						if localType := localFunctionType(functionKey, name, spec.Values, index); localType != "" {
							typeName = localType
						}
						setAliasedMayValueType(values, aliases, bindingKey(name), typeName, types)
					}
				}
			case *ast.RangeStmt:
				keyType, valueType, ok := rangeTypes(expressionType(value.X, imports, values, types), types)
				if !ok {
					keyType, valueType, iterator := iteratorRangeToolTypes(value.X, imports, values, types)
					if !iterator {
						break
					}
					if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" && keyType != "" {
						values[bindingKey(name)] = keyType
					}
					if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" && valueType != "" {
						values[bindingKey(name)] = valueType
					}
					break
				}
				if name, ok := value.Key.(*ast.Ident); ok && name.Name != "_" {
					values[bindingKey(name)] = keyType
				}
				if name, ok := value.Value.(*ast.Ident); ok && name.Name != "_" {
					values[bindingKey(name)] = valueType
				}
			case *ast.SendStmt:
				if isToolRegistryExpression(value.Value, imports, values, types) || isToolRegistrationValueExpression(value.Value, imports, values, types) {
					if name, _ := assignmentBinding(value.Chan); name != nil {
						key := bindingKey(name)
						typeName := toolRegistryCollectionType
						if strings.HasPrefix(resolveNamedType(expressionType(value.Chan, imports, values, types), types), channelTypePrefix) {
							typeName = channelTypePrefix + modulePath + "/internal/tools.Registry"
						}
						setAliasedMayValueType(values, aliases, key, typeName, types)
					}
				}
			case *ast.CallExpr:
				if isCollectionWrite(value, imports) {
					if destination, _ := assignmentBinding(value.Args[0]); destination != nil {
						writeType := toolRegistryCollectionTypeForExpression(value.Args[1], imports, values, types)
						if writeType != "" {
							destinationKey := bindingKey(destination)
							writeType = mergeToolRegistryCollectionTypes(values[destinationKey], writeType)
							setAliasedMayValueType(values, aliases, destinationKey, writeType, types)
						}
					}
				}
				callee := calledFunctionKey(value.Fun, imports, values, types)
				for destinationIndex, sources := range types.parameterWrites[callee] {
					for _, destination := range callArgumentsForParameter(value, callee, destinationIndex, imports, types) {
						destinationName, _ := assignmentBinding(destination)
						if destinationName == nil {
							continue
						}
						for sourceIndex, prefixes := range sources {
							for _, source := range callArgumentsForParameter(value, callee, sourceIndex, imports, types) {
								if isToolRegistryExpression(source, imports, values, types) || isToolRegistryCollectionExpression(source, imports, values, types) || isToolRegistryMapKeyCollectionExpression(source, imports, values, types) {
									writeType := toolRegistryCollectionTypeForExpression(source, imports, values, types)
									if isToolRegistryExpression(source, imports, values, types) {
										writeType = ""
										for prefix := range prefixes {
											if strings.Contains(prefix, mapKeyPathPrefix) {
												writeType = mergeToolRegistryCollectionTypes(writeType, toolRegistryMapKeyCollectionType)
											} else {
												writeType = mergeToolRegistryCollectionTypes(writeType, toolRegistryCollectionType)
											}
										}
									}
									destinationKey := bindingKey(destinationName)
									writeType = mergeToolRegistryCollectionTypes(values[destinationKey], writeType)
									setAliasedMayValueType(values, aliases, destinationKey, writeType, types)
								}
							}
						}
					}
				}
				toolParams := types.toolParams[callee]
				variadicIndex, variadic := types.variadicParams[callee]
				if literal := functionLiteralExpression(value.Fun); literal != nil {
					toolParams = inferToolRegistrationParameters(indexedAppFunction{literal: literal, imports: imports}, types)
					variadicIndex, variadic = variadicParameterIndex(literal.Type.Params)
				}
				reported := false
				for index := range toolParams {
					for _, argument := range callArgumentsForParameterAt(value, index, variadicIndex, variadic, imports) {
						if isToolRegistryExpression(argument, imports, values, types) || isToolRegistryCollectionExpression(argument, imports, values, types) || isToolRegistrationValueExpression(argument, imports, values, types) {
							report(value, selectorChain(value.Fun))
							reported = true
							break
						}
					}
					if reported {
						break
					}
				}
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
		case *ast.IndexExpr:
			base := canonicalTypeInPackage(value.X, imports, packagePath)
			argument := canonicalTypeInPackage(value.Index, imports, packagePath)
			return genericTypePrefix + base + genericTypeSeparator + argument
		case *ast.IndexListExpr:
			parts := []string{canonicalTypeInPackage(value.X, imports, packagePath)}
			for _, argument := range value.Indices {
				parts = append(parts, canonicalTypeInPackage(argument, imports, packagePath))
			}
			return genericTypePrefix + strings.Join(parts, genericTypeSeparator)
		case *ast.ArrayType:
			return sliceTypePrefix + canonicalTypeInPackage(value.Elt, imports, packagePath)
		case *ast.MapType:
			return mapTypePrefix + canonicalTypeInPackage(value.Key, imports, packagePath) + typeSeparator + canonicalTypeInPackage(value.Value, imports, packagePath)
		case *ast.ChanType:
			return channelTypePrefix + canonicalTypeInPackage(value.Value, imports, packagePath)
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

func packageBindingStateForSources(sources []indexedAppSource, types compositionTypeIndex) (map[string]string, map[string]map[string]bool) {
	values := make(map[string]string)
	resources := make(map[string]map[string]bool)
	for {
		beforeValues := cloneStringMap(values)
		beforeResources := cloneCleanupResourceMap(resources)
		for _, source := range sources {
			mergePackageBindingSource(source.file, source.imports, types, values, resources)
		}
		if equalStringMap(values, beforeValues) && equalCleanupResourceMap(resources, beforeResources) {
			break
		}
	}
	return values, resources
}

func packageBindingState(file *ast.File, imports map[string]string, types compositionTypeIndex) (map[string]string, map[string]map[string]bool) {
	values := cloneStringMap(types.packageValues)
	resources := cloneCleanupResourceMap(types.packageResources)
	mergePackageBindingSource(file, imports, types, values, resources)
	return values, resources
}

func mergePackageBindingSource(file *ast.File, imports map[string]string, types compositionTypeIndex, values map[string]string, resources map[string]map[string]bool) {
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, raw := range general.Specs {
			spec := raw.(*ast.ValueSpec)
			for index, name := range spec.Names {
				key := bindingKey(name)
				trackCompositeFunctionTypes(values, packageFunctionScopeKey, name, spec.Values, index, imports, types)
				typeName := canonicalType(spec.Type, imports)
				paths := cleanupPathsForType(typeName, types, nil)
				if len(spec.Values) != 0 {
					if assigned := assignedToolExpressionType(spec.Values, index, imports, values, types); assigned != "" {
						typeName = assigned
					}
					paths = assignedCleanupPaths(spec.Values, index, imports, values, resources, types)
				}
				if localType := localFunctionType(packageFunctionScopeKey, name, spec.Values, index); localType != "" {
					typeName = localType
				}
				for _, binding := range []string{key, name.Name} {
					setMayValueType(values, binding, typeName, types)
					if paths != nil {
						resources[binding] = mergeCleanupPaths(resources[binding], paths)
					}
				}
			}
		}
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneCleanupResourceMap(source map[string]map[string]bool) map[string]map[string]bool {
	cloned := make(map[string]map[string]bool, len(source))
	for key, paths := range source {
		cloned[key] = mergeCleanupPaths(nil, paths)
	}
	return cloned
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func equalCleanupResourceMap(left, right map[string]map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for key, paths := range left {
		other := right[key]
		if len(paths) != len(other) {
			return false
		}
		for path := range paths {
			if !other[path] {
				return false
			}
		}
	}
	return true
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

func assignmentValueKey(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		if value.Name == "_" {
			return ""
		}
		return bindingKey(value)
	case *ast.SelectorExpr:
		prefix := assignmentValueKey(value.X)
		if prefix == "" {
			return ""
		}
		return prefix + "." + value.Sel.Name
	case *ast.IndexExpr:
		prefix := assignmentValueKey(value.X)
		if prefix == "" {
			return ""
		}
		return prefix + "[]"
	case *ast.SliceExpr:
		return assignmentValueKey(value.X)
	case *ast.ParenExpr:
		return assignmentValueKey(value.X)
	case *ast.StarExpr:
		return assignmentValueKey(value.X)
	default:
		return ""
	}
}

func assignedExpression(expressions []ast.Expr, index int) ast.Expr {
	switch {
	case len(expressions) == 1 && index == 0:
		return expressions[0]
	case len(expressions) > 1 && index < len(expressions):
		return expressions[index]
	default:
		return nil
	}
}

func trackReferenceAssignment(references map[string]bool, aliases map[string]map[string]bool, left ast.Expr, declaredType ast.Expr, expressions []ast.Expr, index int, imports map[string]string, types compositionTypeIndex) {
	name, indexed := assignmentBinding(left)
	if name == nil || indexed {
		return
	}
	key := bindingKey(name)
	expression := assignedExpression(expressions, index)
	declaredTypeName := resolveNamedType(canonicalType(declaredType, imports), types)
	if !isReferenceExpression(expression, references, imports, types) && !isReferenceTypeExpression(declaredType) && !isReferenceTypeName(declaredTypeName) {
		return
	}
	references[key] = true
	sourceKey := referenceSourceKey(expression)
	if sourceKey != "" && (references[sourceKey] || isAddressExpression(expression)) {
		addReferenceAlias(aliases, key, sourceKey)
	}
}

func referenceSourceKey(expression ast.Expr) string {
	for {
		switch value := expression.(type) {
		case *ast.ParenExpr:
			expression = value.X
		case *ast.UnaryExpr:
			if value.Op != token.AND {
				return ""
			}
			expression = value.X
		default:
			return assignmentValueKey(expression)
		}
	}
}

func isAddressExpression(expression ast.Expr) bool {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			break
		}
		expression = parenthesized.X
	}
	unary, ok := expression.(*ast.UnaryExpr)
	return ok && unary.Op == token.AND
}

func isReferenceExpression(expression ast.Expr, references map[string]bool, imports map[string]string, types compositionTypeIndex) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return references[bindingKey(value)]
	case *ast.ParenExpr:
		return isReferenceExpression(value.X, references, imports, types)
	case *ast.SliceExpr:
		return isReferenceExpression(value.X, references, imports, types)
	case *ast.UnaryExpr:
		return value.Op == token.AND || isReferenceExpression(value.X, references, imports, types)
	case *ast.CompositeLit:
		typeName := resolveNamedType(canonicalType(value.Type, imports), types)
		return isReferenceTypeExpression(value.Type) || isReferenceTypeName(typeName)
	case *ast.CallExpr:
		identifier, ok := value.Fun.(*ast.Ident)
		return ok && (identifier.Name == "make" || identifier.Name == "new")
	default:
		return false
	}
}

func isReferenceTypeExpression(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.StarExpr, *ast.MapType, *ast.ChanType:
		return true
	case *ast.ArrayType:
		return value.Len == nil
	case *ast.ParenExpr:
		return isReferenceTypeExpression(value.X)
	default:
		return false
	}
}

func addReferenceAlias(aliases map[string]map[string]bool, left, right string) {
	group := map[string]bool{left: true, right: true}
	for member := range aliases[left] {
		group[member] = true
	}
	for member := range aliases[right] {
		group[member] = true
	}
	for member := range group {
		aliases[member] = group
	}
}

func referenceAliasKeys(aliases map[string]map[string]bool, key string) map[string]bool {
	if len(aliases[key]) != 0 {
		return aliases[key]
	}
	return map[string]bool{key: true}
}

func mergeAliasedCleanupPaths(resources map[string]map[string]bool, aliases map[string]map[string]bool, key string, paths map[string]bool) {
	for alias := range referenceAliasKeys(aliases, key) {
		root, prefix := splitAssignmentValueKey(alias)
		resources[root] = mergeCleanupPaths(resources[root], prefixCleanupPaths(prefix, paths))
	}
}

func setAliasedMayValueType(values map[string]string, aliases map[string]map[string]bool, key, typeName string, types compositionTypeIndex) {
	for alias := range referenceAliasKeys(aliases, key) {
		root, _ := splitAssignmentValueKey(alias)
		setMayValueType(values, root, typeName, types)
	}
}

func splitAssignmentValueKey(key string) (string, string) {
	index := len(key)
	if dot := strings.IndexByte(key, '.'); dot >= 0 && dot < index {
		index = dot
	}
	if bracket := strings.IndexByte(key, '['); bracket >= 0 && bracket < index {
		index = bracket
	}
	root := key[:index]
	suffix := key[index:]
	suffix = strings.ReplaceAll(suffix, "[]", "")
	suffix = strings.TrimPrefix(suffix, ".")
	if suffix != "" {
		suffix += "."
	}
	return root, suffix
}

func assignmentBinding(expression ast.Expr) (*ast.Ident, bool) {
	switch value := expression.(type) {
	case *ast.Ident:
		return value, false
	case *ast.IndexExpr:
		identifier, _ := assignmentBinding(value.X)
		return identifier, identifier != nil
	case *ast.SliceExpr:
		return assignmentBinding(value.X)
	case *ast.SelectorExpr:
		identifier, _ := assignmentBinding(value.X)
		return identifier, identifier != nil
	case *ast.ParenExpr:
		return assignmentBinding(value.X)
	case *ast.StarExpr:
		identifier, _ := assignmentBinding(value.X)
		return identifier, identifier != nil
	default:
		return nil, false
	}
}

func isMutatingAssignmentTarget(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.IndexExpr, *ast.SelectorExpr, *ast.StarExpr:
		return true
	case *ast.ParenExpr:
		return isMutatingAssignmentTarget(value.X)
	case *ast.SliceExpr:
		return isMutatingAssignmentTarget(value.X)
	default:
		return false
	}
}

func assignmentFieldPrefix(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.SelectorExpr:
		return assignmentFieldPrefix(value.X) + value.Sel.Name + "."
	case *ast.IndexExpr:
		return assignmentFieldPrefix(value.X)
	case *ast.SliceExpr:
		return assignmentFieldPrefix(value.X)
	case *ast.ParenExpr:
		return assignmentFieldPrefix(value.X)
	case *ast.StarExpr:
		return assignmentFieldPrefix(value.X)
	default:
		return ""
	}
}

func mapAssignmentKey(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) ast.Expr {
	for {
		switch value := expression.(type) {
		case *ast.ParenExpr:
			expression = value.X
		case *ast.IndexExpr:
			if strings.HasPrefix(resolveNamedType(expressionType(value.X, imports, values, types), types), mapTypePrefix) {
				return value.Index
			}
			return nil
		default:
			return nil
		}
	}
}

func isBuiltinCopy(call *ast.CallExpr) bool {
	identifier, ok := call.Fun.(*ast.Ident)
	return ok && identifier.Name == "copy" && len(call.Args) == 2
}

func isMapsCopy(call *ast.CallExpr, imports map[string]string) bool {
	if len(call.Args) != 2 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Copy" {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && imports[qualifier.Name] == "maps"
}

func isMapsInsert(call *ast.CallExpr, imports map[string]string) bool {
	if len(call.Args) != 2 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Insert" {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && imports[qualifier.Name] == "maps"
}

func isCollectionWrite(call *ast.CallExpr, imports map[string]string) bool {
	return isBuiltinCopy(call) || isMapsCopy(call, imports) || isMapsInsert(call, imports)
}

func isMapsAllIterator(expression ast.Expr, imports map[string]string) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "All" {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && imports[qualifier.Name] == "maps"
}

func isBuiltinAppend(call *ast.CallExpr) bool {
	identifier, ok := call.Fun.(*ast.Ident)
	return ok && identifier.Name == "append" && len(call.Args) != 0
}

func valueIteratorArity(expression ast.Expr, imports map[string]string) int {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return 0
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return 0
	}
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return 0
	}
	if imports[qualifier.Name] != "maps" && imports[qualifier.Name] != "slices" {
		return 0
	}
	switch selector.Sel.Name {
	case "Keys", "Values":
		return 1
	case "All":
		return 2
	case "Backward":
		if imports[qualifier.Name] == "slices" {
			return 2
		}
		return 0
	default:
		return 0
	}
}

func iteratorRangeCleanupPaths(expression ast.Expr, paths map[string]bool, imports map[string]string) (map[string]bool, map[string]bool, bool) {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil, nil, false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, nil, false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return nil, nil, false
	}
	switch imports[qualifier.Name] + "." + selector.Sel.Name {
	case "maps.Keys":
		return cleanupMapKeyPaths(paths), nil, true
	case "maps.Values":
		return cleanupMapValuePaths(paths), nil, true
	case "maps.All":
		return cleanupMapKeyPaths(paths), cleanupMapValuePaths(paths), true
	case "slices.All", "slices.Backward":
		return nil, paths, true
	default:
		return nil, nil, false
	}
}

func iteratorRangeToolTypes(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) (string, string, bool) {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", "", false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	registryType := modulePath + "/internal/tools.Registry"
	keyCollection := isToolRegistryMapKeyCollectionExpression(call.Args[0], imports, values, types)
	valueCollection := isToolRegistryCollectionExpression(call.Args[0], imports, values, types)
	switch imports[qualifier.Name] + "." + selector.Sel.Name {
	case "maps.Keys":
		if keyCollection {
			return registryType, "", true
		}
		return "", "", true
	case "maps.Values":
		if valueCollection {
			return registryType, "", true
		}
		return "", "", true
	case "maps.All":
		var keyType, valueType string
		if keyCollection {
			keyType = registryType
		}
		if valueCollection {
			valueType = registryType
		}
		return keyType, valueType, true
	case "slices.All", "slices.Backward":
		if valueCollection {
			return "", registryType, true
		}
		return "", "", true
	default:
		return "", "", false
	}
}

func isSlicesConcatCall(call *ast.CallExpr, imports map[string]string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Concat" {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && imports[qualifier.Name] == "slices"
}

func prefixCleanupPaths(prefix string, paths map[string]bool) map[string]bool {
	if prefix == "" || len(paths) == 0 {
		return paths
	}
	prefixed := make(map[string]bool, len(paths))
	for path := range paths {
		prefixed[prefix+path] = true
	}
	return prefixed
}

func cleanupMapKeyPaths(paths map[string]bool) map[string]bool {
	keyPaths := make(map[string]bool)
	for path := range paths {
		if strings.HasPrefix(path, mapKeyPathPrefix) {
			keyPaths[strings.TrimPrefix(path, mapKeyPathPrefix)] = true
		}
	}
	if len(keyPaths) == 0 {
		return nil
	}
	return keyPaths
}

func cleanupMapValuePaths(paths map[string]bool) map[string]bool {
	valuePaths := make(map[string]bool)
	for path := range paths {
		if !strings.HasPrefix(path, mapKeyPathPrefix) {
			valuePaths[path] = true
		}
	}
	if len(valuePaths) == 0 {
		return nil
	}
	return valuePaths
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
		typeName == toolRegistryCollectionType ||
		typeName == toolRegistryMapKeyCollectionType ||
		typeName == toolRegistryMapKeyValueCollectionType ||
		typeName == channelTypePrefix+modulePath+"/internal/tools.Registry" ||
		strings.HasPrefix(typeName, localFunctionTypePrefix)
}

func mergeToolRegistryCollectionTypes(left, right string) string {
	keyCollection := left == toolRegistryMapKeyCollectionType || left == toolRegistryMapKeyValueCollectionType ||
		right == toolRegistryMapKeyCollectionType || right == toolRegistryMapKeyValueCollectionType
	valueCollection := left == toolRegistryCollectionType || left == toolRegistryMapKeyValueCollectionType ||
		right == toolRegistryCollectionType || right == toolRegistryMapKeyValueCollectionType
	switch {
	case keyCollection && valueCollection:
		return toolRegistryMapKeyValueCollectionType
	case keyCollection:
		return toolRegistryMapKeyCollectionType
	case valueCollection:
		return toolRegistryCollectionType
	case right != "":
		return right
	default:
		return left
	}
}

func toolRegistryCollectionTypeForExpression(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) string {
	var typeName string
	if isToolRegistryMapKeyCollectionExpression(expression, imports, values, types) {
		typeName = mergeToolRegistryCollectionTypes(typeName, toolRegistryMapKeyCollectionType)
	}
	if isToolRegistryCollectionExpression(expression, imports, values, types) {
		typeName = mergeToolRegistryCollectionTypes(typeName, toolRegistryCollectionType)
	}
	return typeName
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
	fields := instantiatedFields(typeName, types)
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
		if strings.HasPrefix(typeName, genericTypePrefix) {
			parts := strings.Split(strings.TrimPrefix(typeName, genericTypePrefix), genericTypeSeparator)
			if len(parts) < 2 {
				return typeName
			}
			base := parts[0]
			underlying := types.namedTypes[base]
			parameters := types.typeParameters[base]
			if underlying == "" || len(parameters) != len(parts)-1 {
				return typeName
			}
			for index, parameter := range parameters {
				underlying = strings.ReplaceAll(underlying, parameter, parts[index+1])
			}
			typeName = underlying
			continue
		}
		underlying := types.namedTypes[typeName]
		if underlying == "" {
			return typeName
		}
		typeName = underlying
	}
	return typeName
}

func instantiatedFields(typeName string, types compositionTypeIndex) map[string]string {
	if fields := types.fields[typeName]; len(fields) != 0 {
		return fields
	}
	if !strings.HasPrefix(typeName, genericTypePrefix) {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(typeName, genericTypePrefix), genericTypeSeparator)
	if len(parts) < 2 {
		return nil
	}
	base := parts[0]
	parameters := types.typeParameters[base]
	fields := types.fields[base]
	if len(fields) == 0 || len(parameters) != len(parts)-1 {
		return nil
	}
	instantiated := make(map[string]string, len(fields))
	for field, fieldType := range fields {
		for index, parameter := range parameters {
			fieldType = strings.ReplaceAll(fieldType, parameter, parts[index+1])
		}
		instantiated[field] = fieldType
	}
	return instantiated
}

func instantiatedFieldOrder(typeName string, types compositionTypeIndex) []string {
	if order := types.fieldOrder[typeName]; len(order) != 0 {
		return order
	}
	if !strings.HasPrefix(typeName, genericTypePrefix) {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(typeName, genericTypePrefix), genericTypeSeparator)
	if len(parts) < 2 {
		return nil
	}
	return types.fieldOrder[parts[0]]
}

func compositeFieldOrder(expression ast.Expr, typeName string, imports map[string]string, types compositionTypeIndex) ([]string, bool) {
	if order := instantiatedFieldOrder(typeName, types); len(order) != 0 {
		return order, true
	}
	structure, ok := expression.(*ast.StructType)
	if !ok {
		return nil, false
	}
	var order []string
	for _, field := range structure.Fields.List {
		if len(field.Names) == 0 {
			order = append(order, embeddedPrefix+canonicalType(field.Type, imports))
			continue
		}
		for _, name := range field.Names {
			order = append(order, name.Name)
		}
	}
	return order, true
}

func rangeTypes(typeName string, types compositionTypeIndex) (string, string, bool) {
	typeName = resolveNamedType(typeName, types)
	if typeName == toolRegistryCollectionType {
		return "", modulePath + "/internal/tools.Registry", true
	}
	if typeName == toolRegistryMapKeyCollectionType {
		return modulePath + "/internal/tools.Registry", "", true
	}
	if typeName == toolRegistryMapKeyValueCollectionType {
		return modulePath + "/internal/tools.Registry", modulePath + "/internal/tools.Registry", true
	}
	if strings.HasPrefix(typeName, sliceTypePrefix) {
		return "", strings.TrimPrefix(typeName, sliceTypePrefix), true
	}
	if strings.HasPrefix(typeName, mapTypePrefix) {
		parts := strings.SplitN(strings.TrimPrefix(typeName, mapTypePrefix), typeSeparator, 2)
		if len(parts) == 2 {
			return parts[0], parts[1], true
		}
	}
	if strings.HasPrefix(typeName, channelTypePrefix) {
		elementType := strings.TrimPrefix(typeName, channelTypePrefix)
		return elementType, elementType, true
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
			if paths := cleanupPathsForType(valueType, types, nil); paths != nil {
				return paths
			}
		}
		return cleanupPathsForExpression(value.X, imports, values, resources, types)
	case *ast.SliceExpr:
		return cleanupPathsForExpression(value.X, imports, values, resources, types)
	case *ast.TypeAssertExpr:
		return cleanupPathsForExpression(value.X, imports, values, resources, types)
	case *ast.CallExpr:
		if isBuiltinAppend(value) || isSlicesConcatCall(value, imports) {
			var paths map[string]bool
			for _, argument := range value.Args {
				paths = mergeCleanupPaths(paths, cleanupPathsForExpression(argument, imports, values, resources, types))
			}
			if paths != nil {
				return paths
			}
		}
		callee := calledFunctionKey(value.Fun, imports, values, types)
		paths := cleanupPathsFromCallArguments(value, callee, 0, imports, values, resources, types)
		paths = mergeCleanupPaths(paths, types.cleanupResults[callee][0])
		if paths != nil {
			return paths
		}
		if results := expressionResultTypes(value, imports, values, types); len(results) != 0 {
			return cleanupPathsForType(results[0], types, nil)
		}
		if len(value.Args) == 1 {
			return cleanupPathsForExpression(value.Args[0], imports, values, resources, types)
		}
	case *ast.UnaryExpr:
		if value.Op == token.AND || value.Op == token.ARROW {
			return cleanupPathsForExpression(value.X, imports, values, resources, types)
		}
	case *ast.StarExpr:
		return cleanupPathsForExpression(value.X, imports, values, resources, types)
	case *ast.CompositeLit:
		typeName := canonicalType(value.Type, imports)
		if paths := cleanupPathsForType(typeName, types, nil); paths != nil {
			return paths
		}
		paths := make(map[string]bool)
		fieldOrder, structured := compositeFieldOrder(value.Type, typeName, imports, types)
		mapLiteral := strings.HasPrefix(resolveNamedType(typeName, types), mapTypePrefix)
		for index, element := range value.Elts {
			prefix := ""
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				if mapLiteral {
					for path := range cleanupPathsForExpression(pair.Key, imports, values, resources, types) {
						paths[mapKeyPathPrefix+path] = true
					}
				}
				if field, ok := pair.Key.(*ast.Ident); structured && ok {
					prefix = field.Name + "."
				}
				element = pair.Value
			} else if structured && index < len(fieldOrder) && !strings.HasPrefix(fieldOrder[index], embeddedPrefix) {
				prefix = fieldOrder[index] + "."
			}
			for path := range cleanupPathsForExpression(element, imports, values, resources, types) {
				paths[prefix+path] = true
			}
		}
		if len(paths) != 0 {
			return paths
		}
	}
	return nil
}

func isFeatureCleanupMethodExpression(selector *ast.SelectorExpr, imports map[string]string, types compositionTypeIndex) bool {
	_ = types
	return isTypeExpression(selector.X, imports) && isFeatureCleanupMethodName(selector.Sel.Name)
}

func isTypeExpression(expression ast.Expr, imports map[string]string) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Obj != nil && value.Obj.Kind == ast.Typ
	case *ast.ParenExpr:
		return isTypeExpression(value.X, imports)
	case *ast.StarExpr:
		return isTypeExpression(value.X, imports)
	case *ast.IndexExpr:
		return isTypeExpression(value.X, imports)
	case *ast.IndexListExpr:
		return isTypeExpression(value.X, imports)
	case *ast.SelectorExpr:
		qualifier, ok := value.X.(*ast.Ident)
		return ok && imports[qualifier.Name] != ""
	default:
		return false
	}
}

func assignedCleanupPaths(expressions []ast.Expr, index int, imports map[string]string, values map[string]string, resources map[string]map[string]bool, types compositionTypeIndex) map[string]bool {
	if len(expressions) == 1 {
		if call, ok := expressions[0].(*ast.CallExpr); ok {
			callee := calledFunctionKey(call.Fun, imports, values, types)
			paths := cleanupPathsFromCallArguments(call, callee, index, imports, values, resources, types)
			paths = mergeCleanupPaths(paths, types.cleanupResults[callee][index])
			if paths != nil {
				return paths
			}
		}
		if results := expressionResultTypes(expressions[0], imports, values, types); index < len(results) {
			if paths := cleanupPathsForType(results[index], types, nil); paths != nil {
				return paths
			}
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

func mergeCleanupPaths(destination, source map[string]bool) map[string]bool {
	if len(source) == 0 {
		return destination
	}
	if destination == nil {
		destination = make(map[string]bool)
	}
	for path := range source {
		destination[path] = true
	}
	return destination
}

func cleanupPathsFromCallArguments(call *ast.CallExpr, callee string, resultIndex int, imports map[string]string, values map[string]string, resources map[string]map[string]bool, types compositionTypeIndex) map[string]bool {
	paths := make(map[string]bool)
	for parameterIndex, prefixes := range resultParameterPathSummary(callee, resultIndex, types) {
		for _, argument := range callArgumentsForParameter(call, callee, parameterIndex, imports, types) {
			for prefix := range prefixes {
				for path := range cleanupPathsForExpression(argument, imports, values, resources, types) {
					paths[prefix+path] = true
				}
			}
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return paths
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
		if index == 0 {
			return expressionType(expressions[0], imports, values, types)
		}
		return ""
	}
	if index >= len(expressions) {
		return ""
	}
	return expressionType(expressions[index], imports, values, types)
}

func assignedToolExpressionType(expressions []ast.Expr, index int, imports map[string]string, values map[string]string, types compositionTypeIndex) string {
	if len(expressions) == 1 {
		if call, ok := expressions[0].(*ast.CallExpr); ok {
			callee := calledFunctionKey(call.Fun, imports, values, types)
			if types.toolCallResults[callee][index] {
				return toolRegistrationCallableType
			}
			if isToolRegistryResultExpression(expressions[0], index, imports, values, types) {
				return modulePath + "/internal/tools.Registry"
			}
		}
	}
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
	if expression != nil {
		keyCollection := isToolRegistryMapKeyCollectionExpression(expression, imports, values, types)
		valueCollection := isToolRegistryCollectionExpression(expression, imports, values, types)
		switch {
		case keyCollection && valueCollection:
			return toolRegistryMapKeyValueCollectionType
		case keyCollection:
			return toolRegistryMapKeyCollectionType
		case valueCollection:
			return toolRegistryCollectionType
		}
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
	if typeName := values[assignmentValueKey(expression)]; strings.HasPrefix(typeName, localFunctionTypePrefix) {
		return strings.TrimPrefix(typeName, localFunctionTypePrefix)
	}
	switch function := expression.(type) {
	case *ast.ParenExpr:
		return calledFunctionKey(function.X, imports, values, types)
	case *ast.IndexExpr:
		return calledFunctionKey(function.X, imports, values, types)
	case *ast.IndexListExpr:
		return calledFunctionKey(function.X, imports, values, types)
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
		if isTypeExpression(function.X, imports) {
			return canonicalType(function.X, imports) + "." + function.Sel.Name
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
		if value.Op == token.ARROW {
			typeName := expressionType(value.X, imports, values, types)
			if _, elementType, ok := rangeTypes(typeName, types); ok {
				return elementType
			}
			return typeName
		}
	case *ast.CompositeLit:
		return canonicalType(value.Type, imports)
	case *ast.SelectorExpr:
		receiverType := expressionType(value.X, imports, values, types)
		methodKey := receiverType + "." + value.Sel.Name
		_, interfaceMethod := types.interfaceMethods[methodKey]
		if !isTypeExpression(value.X, imports) && (types.appFunctionKeys[methodKey] || interfaceMethod) {
			return localFunctionTypePrefix + methodKey
		}
		if receiverType == toolRegistryCollectionType {
			return modulePath + "/internal/tools.Registry"
		}
		return compositionFieldType(receiverType, value.Sel.Name, types, nil)
	case *ast.IndexExpr:
		_, valueType, _ := rangeTypes(expressionType(value.X, imports, values, types), types)
		return valueType
	case *ast.SliceExpr:
		return expressionType(value.X, imports, values, types)
	case *ast.TypeAssertExpr:
		return canonicalType(value.Type, imports)
	case *ast.CallExpr:
		if identifier, ok := value.Fun.(*ast.Ident); ok && (identifier.Name == "make" || identifier.Name == "new") && len(value.Args) != 0 {
			return canonicalType(value.Args[0], imports)
		}
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
	fields := instantiatedFields(typeName, types)
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
	target := call.Fun
	for {
		parenthesized, ok := target.(*ast.ParenExpr)
		if !ok {
			break
		}
		target = parenthesized.X
	}
	if isToolRegistrationCallableExpression(target, imports, values, types) {
		return true
	}
	switch function := target.(type) {
	case *ast.Ident:
		return values[bindingKey(function)] == toolRegistrationCallableType || imports["."] == modulePath+"/internal/tools" && isToolRegistrationName(function.Name)
	case *ast.SelectorExpr:
		if isToolRegistrationMethodExpression(function, imports, types) {
			return true
		}
		if qualifier, ok := function.X.(*ast.Ident); ok && imports[qualifier.Name] == modulePath+"/internal/tools" {
			return isToolRegistrationName(function.Sel.Name)
		}
		return isToolRegistryExpression(function.X, imports, values, types) && isToolRegistrationName(function.Sel.Name)
	default:
		return false
	}
}

func isToolRegistryExpression(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	const registryType = modulePath + "/internal/tools.Registry"
	if assertion, ok := expression.(*ast.TypeAssertExpr); ok {
		return isToolRegistryExpression(assertion.X, imports, values, types)
	}
	if selector, ok := expression.(*ast.SelectorExpr); ok && isReturnedToolRegistrySelector(selector, imports, values, types) {
		return true
	}
	if call, ok := expression.(*ast.CallExpr); ok {
		callee := calledFunctionKey(call.Fun, imports, values, types)
		for parameterIndex, prefixes := range resultParameterPathSummary(callee, 0, types) {
			if !prefixes[""] {
				continue
			}
			for _, argument := range callArgumentsForParameter(call, callee, parameterIndex, imports, types) {
				if isToolRegistryExpression(argument, imports, values, types) {
					return true
				}
			}
		}
		if types.toolResults[callee][0] {
			return true
		}
		if len(call.Args) == 1 && isToolRegistryExpression(call.Args[0], imports, values, types) {
			return true
		}
	}
	if resolveNamedType(expressionType(expression, imports, values, types), types) == registryType {
		return true
	}
	results := expressionResultTypes(expression, imports, values, types)
	return len(results) != 0 && resolveNamedType(results[0], types) == registryType
}

func isReturnedToolRegistrySelector(expression *ast.SelectorExpr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	path := ""
	var current ast.Expr = expression
	for {
		switch value := current.(type) {
		case *ast.SelectorExpr:
			path = value.Sel.Name + "." + path
			current = value.X
		case *ast.ParenExpr:
			current = value.X
		case *ast.StarExpr:
			current = value.X
		default:
			call, ok := current.(*ast.CallExpr)
			if !ok {
				return false
			}
			callee := calledFunctionKey(call.Fun, imports, values, types)
			for parameterIndex, prefixes := range resultParameterPathSummary(callee, 0, types) {
				if !prefixes[path] {
					continue
				}
				for _, argument := range callArgumentsForParameter(call, callee, parameterIndex, imports, types) {
					if isToolRegistryExpression(argument, imports, values, types) {
						return true
					}
				}
			}
			return false
		}
	}
}

func isToolRegistryResultExpression(expression ast.Expr, resultIndex int, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return resultIndex == 0 && isToolRegistryExpression(expression, imports, values, types)
	}
	callee := calledFunctionKey(call.Fun, imports, values, types)
	if types.toolCallResults[callee][resultIndex] {
		return false
	}
	for parameterIndex, prefixes := range resultParameterPathSummary(callee, resultIndex, types) {
		if !prefixes[""] {
			continue
		}
		for _, argument := range callArgumentsForParameter(call, callee, parameterIndex, imports, types) {
			if isToolRegistryExpression(argument, imports, values, types) {
				return true
			}
		}
	}
	if types.toolResults[callee][resultIndex] {
		return true
	}
	results := expressionResultTypes(expression, imports, values, types)
	return resultIndex < len(results) && resolveNamedType(results[resultIndex], types) == modulePath+"/internal/tools.Registry"
}

func isToolRegistryCollectionExpression(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	typeName := expressionType(expression, imports, values, types)
	if typeName == toolRegistryCollectionType || typeName == toolRegistryMapKeyValueCollectionType {
		return true
	}
	if call, ok := expression.(*ast.CallExpr); ok && valueIteratorArity(call, imports) != 0 {
		return isToolRegistryCollectionExpression(call.Args[0], imports, values, types)
	}
	if call, ok := expression.(*ast.CallExpr); ok && isSlicesConcatCall(call, imports) {
		for _, argument := range call.Args {
			if isToolRegistryCollectionExpression(argument, imports, values, types) || isToolRegistryExpression(argument, imports, values, types) {
				return true
			}
		}
		return false
	}
	if call, ok := expression.(*ast.CallExpr); ok && isBuiltinAppend(call) {
		for _, argument := range call.Args {
			if isToolRegistryCollectionExpression(argument, imports, values, types) || isToolRegistryExpression(argument, imports, values, types) {
				return true
			}
		}
		return false
	}
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		return false
	}
	for _, element := range literal.Elts {
		if pair, ok := element.(*ast.KeyValueExpr); ok {
			element = pair.Value
		}
		if isToolRegistryExpression(element, imports, values, types) {
			return true
		}
	}
	return false
}

func isToolRegistryMapKeyCollectionExpression(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	typeName := expressionType(expression, imports, values, types)
	if typeName == toolRegistryMapKeyCollectionType || typeName == toolRegistryMapKeyValueCollectionType {
		return true
	}
	if isMapsAllIterator(expression, imports) {
		return isToolRegistryMapKeyCollectionExpression(expression.(*ast.CallExpr).Args[0], imports, values, types)
	}
	if parenthesized, ok := expression.(*ast.ParenExpr); ok {
		return isToolRegistryMapKeyCollectionExpression(parenthesized.X, imports, values, types)
	}
	literal, ok := expression.(*ast.CompositeLit)
	if !ok || !strings.HasPrefix(resolveNamedType(canonicalType(literal.Type, imports), types), mapTypePrefix) {
		return false
	}
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if ok && isToolRegistryExpression(pair.Key, imports, values, types) {
			return true
		}
	}
	return false
}

func isToolRegistrationValueExpression(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || !isToolRegistrationName(selector.Sel.Name) {
		return false
	}
	if isToolRegistrationMethodExpression(selector, imports, types) {
		return true
	}
	if qualifier, ok := selector.X.(*ast.Ident); ok && imports[qualifier.Name] == modulePath+"/internal/tools" {
		return true
	}
	return isToolRegistryExpression(selector.X, imports, values, types)
}

func isToolRegistrationMethodExpression(selector *ast.SelectorExpr, imports map[string]string, types compositionTypeIndex) bool {
	_ = types
	return isTypeExpression(selector.X, imports) && isToolRegistrationName(selector.Sel.Name)
}

func isToolRegistrationCallableExpression(expression ast.Expr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return values[bindingKey(value)] == toolRegistrationCallableType
	case *ast.ParenExpr:
		return isToolRegistrationCallableExpression(value.X, imports, values, types)
	case *ast.CallExpr:
		callee := calledFunctionKey(value.Fun, imports, values, types)
		return types.toolCallResults[callee][0]
	case *ast.SelectorExpr:
		if isReturnedToolRegistrationSelector(value, imports, values, types) {
			return true
		}
		return isToolRegistrationValueExpression(value, imports, values, types)
	default:
		return isToolRegistrationValueExpression(expression, imports, values, types)
	}
}

func isReturnedToolRegistrationSelector(expression *ast.SelectorExpr, imports map[string]string, values map[string]string, types compositionTypeIndex) bool {
	path := ""
	var current ast.Expr = expression
	for {
		switch value := current.(type) {
		case *ast.SelectorExpr:
			if path == "" {
				path = value.Sel.Name
			} else {
				path = value.Sel.Name + "." + path
			}
			current = value.X
		case *ast.ParenExpr:
			current = value.X
		case *ast.IndexExpr:
			current = value.X
		default:
			call, ok := current.(*ast.CallExpr)
			if !ok {
				return false
			}
			callee := calledFunctionKey(call.Fun, imports, values, types)
			return types.toolCallPaths[callee][0][path]
		}
	}
}

func selectorChain(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.ParenExpr:
		return selectorChain(value.X)
	case *ast.IndexExpr:
		return selectorChain(value.X)
	case *ast.IndexListExpr:
		return selectorChain(value.X)
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
	case *ast.FuncLit:
		return "func"
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
