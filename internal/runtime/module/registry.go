// Package module defines the typed in-process capabilities and lifecycle sets
// assembled into a JueX runtime. It is separate from resource-based Extensions.
package module

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/tools"
)

var ErrSealed = errors.New("runtime modules: registry is sealed")

type ID string

// Module is one trusted in-process capability unit. A Module is registered
// once and indexed under every narrow capability interface it implements.
type Module interface {
	ID() ID
}

type RuntimeContext struct {
	ID            string
	WorkDir       string
	AgentStateDir string
	ArtifactDir   string
}

type SessionContext struct {
	ID            string
	Dir           string
	ScratchpadDir string
}

type ToolContext struct {
	Runtime RuntimeContext
	Session *SessionContext
}

type ContextPurpose string

const (
	ContextPurposeSessionStart      ContextPurpose = "session_start"
	ContextPurposeTurnPreparation   ContextPurpose = "turn_preparation"
	ContextPurposeProviderIteration ContextPurpose = "provider_iteration"
)

type ContextRequest struct {
	Purpose ContextPurpose
	Runtime RuntimeContext
	Session *SessionContext
}

// ContextSection is one named provider-context fragment. ModuleID is assigned
// by the framework and cannot be forged by a provider.
type ContextSection struct {
	ModuleID ID
	Key      string
	Label    string
	Source   string
	Path     string
	Text     string
}

// PromptSection remains an alias while prompt callers migrate to the broader
// Context Provider vocabulary.
type PromptSection = ContextSection

type ToolProvider interface {
	Tools(context.Context, ToolContext) ([]tools.Tool, error)
}

type ContextProvider interface {
	Context(context.Context, ContextRequest) ([]ContextSection, error)
}

type registeredModule struct {
	id     ID
	module Module
}

// Registry is mutable only during composition. Seal validates contributions
// and returns an immutable Set used while the runtime is serving.
type Registry struct {
	mu       sync.Mutex
	sealed   bool
	modules  []registeredModule
	ids      map[ID]struct{}
	tools    []registeredModule
	contexts []registeredModule
}

func NewRegistry() *Registry {
	return &Registry{ids: make(map[ID]struct{})}
}

func (r *Registry) Register(mod Module) error {
	if mod == nil {
		return fmt.Errorf("runtime modules: nil module")
	}
	id := normalizeID(mod.ID())
	if id == "" {
		return fmt.Errorf("runtime modules: empty id")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return ErrSealed
	}
	if r.ids == nil {
		r.ids = make(map[ID]struct{})
	}
	if _, exists := r.ids[id]; exists {
		return fmt.Errorf("runtime modules: module %q already registered", id)
	}
	registered := registeredModule{id: id, module: mod}
	r.ids[id] = struct{}{}
	r.modules = append(r.modules, registered)
	if _, ok := mod.(ToolProvider); ok {
		r.tools = append(r.tools, registered)
	}
	if _, ok := mod.(ContextProvider); ok {
		r.contexts = append(r.contexts, registered)
	}
	return nil
}

func (r *Registry) Seal(ctx context.Context, toolContext ToolContext) (*Set, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime modules: nil registry")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	if r.sealed {
		r.mu.Unlock()
		return nil, ErrSealed
	}
	r.sealed = true
	modules := append([]registeredModule(nil), r.modules...)
	toolProviders := append([]registeredModule(nil), r.tools...)
	contextProviders := append([]registeredModule(nil), r.contexts...)
	r.mu.Unlock()

	catalog, err := buildToolCatalog(ctx, toolContext, toolProviders)
	if err != nil {
		return nil, err
	}
	return &Set{
		modules:          modules,
		contextProviders: contextProviders,
		toolCatalog:      catalog,
	}, nil
}

type ToolEntry struct {
	ModuleID ID
	Tool     tools.Tool
}

type ToolCatalog struct {
	entries []ToolEntry
}

func (c ToolCatalog) Entries() []ToolEntry {
	entries := make([]ToolEntry, len(c.entries))
	for i, entry := range c.entries {
		entries[i] = entry
		entries[i].Tool = entry.Tool.Clone()
	}
	return entries
}

func (c ToolCatalog) Install(registry *tools.Registry) error {
	if registry == nil {
		return fmt.Errorf("runtime modules: nil tool registry")
	}
	for _, entry := range c.entries {
		if err := registry.Register(entry.Tool.Clone()); err != nil {
			return fmt.Errorf("runtime modules: install tool %q from module %q: %w", entry.Tool.Name, entry.ModuleID, err)
		}
	}
	return nil
}

func (c ToolCatalog) Names() []string {
	names := make([]string, 0, len(c.entries))
	for _, entry := range c.entries {
		names = append(names, entry.Tool.Name)
	}
	return names
}

// InstallToolCatalogs validates ownership across complete Runtime and Session
// sets before mutating the destination registry.
func InstallToolCatalogs(registry *tools.Registry, sets ...*Set) error {
	if registry == nil {
		return fmt.Errorf("runtime modules: nil tool registry")
	}
	owners := make(map[string]ID)
	var entries []ToolEntry
	for _, set := range sets {
		if set == nil {
			continue
		}
		for _, entry := range set.ToolCatalog().Entries() {
			if first, exists := owners[entry.Tool.Name]; exists {
				return fmt.Errorf("runtime modules: tool %q contributed by module %q and module %q", entry.Tool.Name, first, entry.ModuleID)
			}
			owners[entry.Tool.Name] = entry.ModuleID
			entries = append(entries, entry)
		}
	}
	for _, entry := range entries {
		if err := registry.Register(entry.Tool.Clone()); err != nil {
			return fmt.Errorf("runtime modules: install tool %q from module %q: %w", entry.Tool.Name, entry.ModuleID, err)
		}
	}
	return nil
}

func buildToolCatalog(ctx context.Context, toolContext ToolContext, providers []registeredModule) (ToolCatalog, error) {
	owners := make(map[string]ID)
	validator := tools.NewRegistry()
	var entries []ToolEntry
	for _, registered := range providers {
		provided, err := registered.module.(ToolProvider).Tools(ctx, toolContext)
		if err != nil {
			return ToolCatalog{}, fmt.Errorf("runtime module %q tools: %w", registered.id, err)
		}
		for _, tool := range provided {
			tool = tool.Clone()
			name := strings.TrimSpace(tool.Name)
			if first, exists := owners[name]; exists {
				return ToolCatalog{}, fmt.Errorf("runtime modules: tool %q contributed by module %q and module %q", name, first, registered.id)
			}
			tool.Name = name
			if err := validator.Register(tool); err != nil {
				return ToolCatalog{}, fmt.Errorf("runtime module %q tool %q: %w", registered.id, name, err)
			}
			normalized, _ := validator.Get(tool.Name)
			owners[name] = registered.id
			entries = append(entries, ToolEntry{ModuleID: registered.id, Tool: normalized})
		}
	}
	return ToolCatalog{entries: entries}, nil
}

// Set is the immutable capability index produced by Seal. Lifecycle state is
// private and does not permit capability registration after publication.
type Set struct {
	modules          []registeredModule
	contextProviders []registeredModule
	toolCatalog      ToolCatalog

	scope Scope
	mu    sync.RWMutex
	state lifecycleState
}

func (s *Set) Modules() []Module {
	if s == nil {
		return nil
	}
	modules := make([]Module, 0, len(s.modules))
	for _, registered := range s.modules {
		modules = append(modules, registered.module)
	}
	return modules
}

func (s *Set) ToolCatalog() ToolCatalog {
	if s == nil {
		return ToolCatalog{}
	}
	return ToolCatalog{entries: s.toolCatalog.Entries()}
}

func (s *Set) Context(ctx context.Context, request ContextRequest) ([]ContextSection, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.closed {
		return nil, fmt.Errorf("runtime modules: %s set is closed", s.scope)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	owners := make(map[string]ID)
	var sections []ContextSection
	for _, registered := range s.contextProviders {
		provided, err := registered.module.(ContextProvider).Context(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("runtime module %q context: %w", registered.id, err)
		}
		for _, section := range provided {
			if section.Text == "" {
				continue
			}
			key := strings.TrimSpace(section.Key)
			if key == "" {
				return nil, fmt.Errorf("runtime module %q context: empty key", registered.id)
			}
			if section.ModuleID != "" && normalizeID(section.ModuleID) != registered.id {
				return nil, fmt.Errorf("runtime module %q context key %q claims module %q", registered.id, key, section.ModuleID)
			}
			if first, exists := owners[key]; exists {
				return nil, fmt.Errorf("runtime modules: context key %q contributed by module %q and module %q", key, first, registered.id)
			}
			section.ModuleID = registered.id
			section.Key = key
			owners[key] = registered.id
			sections = append(sections, section)
		}
	}
	return sections, nil
}

// CollectContext validates context ownership across complete Runtime and
// Session sets while preserving their explicit composition order.
func CollectContext(ctx context.Context, request ContextRequest, sets ...*Set) ([]ContextSection, error) {
	owners := make(map[string]ID)
	var sections []ContextSection
	for _, set := range sets {
		provided, err := set.Context(ctx, request)
		if err != nil {
			return nil, err
		}
		for _, section := range provided {
			if first, exists := owners[section.Key]; exists {
				return nil, fmt.Errorf("runtime modules: context key %q contributed by module %q and module %q", section.Key, first, section.ModuleID)
			}
			owners[section.Key] = section.ModuleID
			sections = append(sections, section)
		}
	}
	return sections, nil
}

func normalizeID(id ID) ID {
	return ID(strings.TrimSpace(string(id)))
}
