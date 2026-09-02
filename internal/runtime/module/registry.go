// Package module defines the typed in-process capabilities and lifecycle sets
// assembled into a JueX runtime. It is separate from resource-based Extensions.
package module

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

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
	MediaDir      string
}

type ThreadContext struct {
	ID            string
	Dir           string
	ScratchpadDir string
}

type ToolContext struct {
	Runtime RuntimeContext
	Thread  *ThreadContext
}

type ContextPurpose string

const (
	ContextPurposeThreadStart       ContextPurpose = "thread_start"
	ContextPurposeTurnPreparation   ContextPurpose = "turn_preparation"
	ContextPurposeProviderIteration ContextPurpose = "provider_iteration"
)

type ContextRequest struct {
	Purpose ContextPurpose
	Runtime RuntimeContext
	Thread  *ThreadContext
}

type ContextProjection string

const (
	ContextProjectionSystemPrompt   ContextProjection = "system_prompt"
	ContextProjectionRuntimeMessage ContextProjection = "runtime_message"
)

type ContextBudgetMode string

const (
	ContextBudgetBounded   ContextBudgetMode = "bounded"
	ContextBudgetUnbounded ContextBudgetMode = "unbounded"
)

type ContextBudget struct {
	Mode     ContextBudgetMode
	MaxChars int
}

func BoundedContextBudget(maxChars int) ContextBudget {
	return ContextBudget{Mode: ContextBudgetBounded, MaxChars: maxChars}
}

func UnboundedContextBudget() ContextBudget {
	return ContextBudget{Mode: ContextBudgetUnbounded}
}

// ContextSection is one named provider-context fragment. ModuleID is assigned
// by the framework and cannot be forged by a provider.
type ContextSection struct {
	ModuleID   ID
	Scope      Scope
	Purpose    ContextPurpose
	Key        string
	Label      string
	Source     string
	Path       string
	Text       string
	Projection ContextProjection
	MessageID  string
	Budget     ContextBudget
}

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
	mu                    sync.Mutex
	sealed                bool
	modules               []registeredModule
	ids                   map[ID]struct{}
	tools                 []registeredModule
	contexts              []registeredModule
	turnInputPolicies     []registeredModule
	toolPolicies          []registeredModule
	finishPolicies        []registeredModule
	threadStartPolicies   []registeredModule
	compactionPolicies    []registeredModule
	pendingInputObservers []registeredModule
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
	if _, ok := mod.(TurnInputPolicy); ok {
		r.turnInputPolicies = append(r.turnInputPolicies, registered)
	}
	if _, ok := mod.(ToolPolicy); ok {
		r.toolPolicies = append(r.toolPolicies, registered)
	}
	if _, ok := mod.(FinishPolicy); ok {
		r.finishPolicies = append(r.finishPolicies, registered)
	}
	if _, ok := mod.(ThreadStartPolicy); ok {
		r.threadStartPolicies = append(r.threadStartPolicies, registered)
	}
	if _, ok := mod.(CompactionPolicy); ok {
		r.compactionPolicies = append(r.compactionPolicies, registered)
	}
	if _, ok := mod.(PendingInputObserver); ok {
		r.pendingInputObservers = append(r.pendingInputObservers, registered)
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
	set, err := r.freeze()
	if err != nil {
		return nil, err
	}
	if err := set.materializeToolCatalog(ctx, toolContext); err != nil {
		return nil, err
	}
	return set, nil
}

// freeze seals structural identity, registration order, and capability
// indexes without evaluating contributions that may depend on started
// resources. Pure callers use Seal; lifecycle composition uses freeze, starts
// resources, then materializes the catalog before publication.
func (r *Registry) freeze() (*Set, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime modules: nil registry")
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
	turnInputPolicies := append([]registeredModule(nil), r.turnInputPolicies...)
	toolPolicies := append([]registeredModule(nil), r.toolPolicies...)
	finishPolicies := append([]registeredModule(nil), r.finishPolicies...)
	threadStartPolicies := append([]registeredModule(nil), r.threadStartPolicies...)
	compactionPolicies := append([]registeredModule(nil), r.compactionPolicies...)
	pendingInputObservers := append([]registeredModule(nil), r.pendingInputObservers...)
	r.mu.Unlock()

	return &Set{
		modules:               modules,
		toolProviders:         toolProviders,
		contextProviders:      contextProviders,
		turnInputPolicies:     turnInputPolicies,
		toolPolicies:          toolPolicies,
		finishPolicies:        finishPolicies,
		threadStartPolicies:   threadStartPolicies,
		compactionPolicies:    compactionPolicies,
		pendingInputObservers: pendingInputObservers,
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

func (c ToolCatalog) Names() []string {
	names := make([]string, 0, len(c.entries))
	for _, entry := range c.entries {
		names = append(names, entry.Tool.Name)
	}
	return names
}

// BuildToolRegistry validates every catalog before returning a new serving
// registry. A failed build never exposes a partially installed registry.
func BuildToolRegistry(options tools.RegistryOptions, sets ...*Set) (*tools.Registry, error) {
	owners := make(map[string]ID)
	var entries []ToolEntry
	for _, set := range sets {
		if set == nil {
			continue
		}
		for _, entry := range set.ToolCatalog().Entries() {
			if first, exists := owners[entry.Tool.Name]; exists {
				return nil, fmt.Errorf("runtime modules: tool %q contributed by module %q and module %q", entry.Tool.Name, first, entry.ModuleID)
			}
			owners[entry.Tool.Name] = entry.ModuleID
			entries = append(entries, entry)
		}
	}
	registry := tools.NewRegistryWithOptions(options)
	for _, entry := range entries {
		if err := registry.Register(entry.Tool.Clone()); err != nil {
			return nil, fmt.Errorf("runtime modules: build tool %q from module %q: %w", entry.Tool.Name, entry.ModuleID, err)
		}
	}
	return registry, nil
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
	modules               []registeredModule
	toolProviders         []registeredModule
	contextProviders      []registeredModule
	toolCatalog           ToolCatalog
	turnInputPolicies     []registeredModule
	toolPolicies          []registeredModule
	finishPolicies        []registeredModule
	threadStartPolicies   []registeredModule
	compactionPolicies    []registeredModule
	pendingInputObservers []registeredModule

	scope       Scope
	mu          sync.RWMutex
	lifecycleMu sync.Mutex
	state       lifecycleState
}

type Descriptor struct {
	ID    ID
	Scope Scope
}

func (s *Set) Descriptors() []Descriptor {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	descriptors := make([]Descriptor, 0, len(s.modules))
	for _, registered := range s.modules {
		descriptors = append(descriptors, Descriptor{ID: registered.id, Scope: s.scope})
	}
	return descriptors
}

func (s *Set) materializeToolCatalog(ctx context.Context, toolContext ToolContext) error {
	if s == nil {
		return nil
	}
	catalog, err := buildToolCatalog(nonNilContext(ctx), toolContext, s.toolProviders)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.toolCatalog = catalog
	s.mu.Unlock()
	return nil
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

func AllowsLiveToolOutput(sets ...*Set) bool {
	for _, set := range sets {
		if set == nil {
			continue
		}
		set.mu.RLock()
		allowed := true
		for _, registered := range set.toolPolicies {
			policy, ok := registered.module.(LiveToolOutputPolicy)
			if !ok || !policy.AllowsLiveToolOutput() {
				allowed = false
				break
			}
		}
		set.mu.RUnlock()
		if !allowed {
			return false
		}
	}
	return true
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
			if section.Scope != "" && section.Scope != s.scope {
				return nil, fmt.Errorf("runtime module %q context key %q claims scope %q for %q set", registered.id, key, section.Scope, s.scope)
			}
			if section.Purpose != "" && section.Purpose != request.Purpose {
				return nil, fmt.Errorf("runtime module %q context key %q claims purpose %q for %q request", registered.id, key, section.Purpose, request.Purpose)
			}
			section.Source = strings.TrimSpace(section.Source)
			if section.Source == "" {
				return nil, fmt.Errorf("runtime module %q context key %q has empty source", registered.id, key)
			}
			if err := validateContextProjection(section); err != nil {
				return nil, fmt.Errorf("runtime module %q context key %q: %w", registered.id, key, err)
			}
			if err := validateContextBudget(section); err != nil {
				return nil, fmt.Errorf("runtime module %q context key %q: %w", registered.id, key, err)
			}
			if first, exists := owners[key]; exists {
				return nil, fmt.Errorf("runtime modules: context key %q contributed by module %q and module %q", key, first, registered.id)
			}
			section.ModuleID = registered.id
			section.Scope = s.scope
			section.Purpose = request.Purpose
			section.Key = key
			owners[key] = registered.id
			sections = append(sections, section)
		}
	}
	return sections, nil
}

func validateContextProjection(section ContextSection) error {
	switch section.Projection {
	case ContextProjectionSystemPrompt:
		return nil
	case ContextProjectionRuntimeMessage:
		if strings.TrimSpace(section.MessageID) == "" {
			return fmt.Errorf("runtime_message projection requires message id")
		}
		return nil
	default:
		return fmt.Errorf("invalid projection %q", section.Projection)
	}
}

func validateContextBudget(section ContextSection) error {
	switch section.Budget.Mode {
	case ContextBudgetUnbounded:
		if section.Budget.MaxChars != 0 {
			return fmt.Errorf("invalid budget: unbounded section has max_chars %d", section.Budget.MaxChars)
		}
		return nil
	case ContextBudgetBounded:
		if section.Budget.MaxChars <= 0 {
			return fmt.Errorf("invalid budget: bounded section requires positive max_chars")
		}
		if count := utf8.RuneCountInString(section.Text); count > section.Budget.MaxChars {
			return fmt.Errorf("context length %d exceeds max_chars %d", count, section.Budget.MaxChars)
		}
		return nil
	default:
		return fmt.Errorf("invalid budget mode %q", section.Budget.Mode)
	}
}

func SectionsForProjection(sections []ContextSection, projection ContextProjection) []ContextSection {
	filtered := make([]ContextSection, 0, len(sections))
	for _, section := range sections {
		if section.Projection == projection {
			filtered = append(filtered, section)
		}
	}
	return filtered
}

// CollectContext validates context ownership across complete Runtime and
// Thread sets while preserving their explicit composition order.
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
