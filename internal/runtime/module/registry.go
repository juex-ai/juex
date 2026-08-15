// Package module defines the in-process capability modules assembled into a
// JueX runtime. It is intentionally separate from resource-based Extensions.
package module

import (
	"fmt"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/tools"
)

// Module is one named in-process runtime capability. A Module implements one
// or more narrow capability interfaces such as PromptContextProvider or
// ToolRegistrar.
type Module interface {
	Name() string
}

// PromptSection is one named system-prompt fragment supplied by a Module.
type PromptSection struct {
	Key    string
	Label  string
	Source string
	Path   string
	Text   string
}

// PromptContextProvider contributes prompt sections in Module registration
// order. Implementations are called whenever the system prompt is rebuilt.
type PromptContextProvider interface {
	PromptContext() ([]PromptSection, error)
}

// ToolRegistrar registers a Module's model-callable tools during startup.
type ToolRegistrar interface {
	RegisterTools(*tools.Registry) error
}

// Registry retains enabled Modules in stable registration order.
type Registry struct {
	mu      sync.RWMutex
	modules []Module
	names   map[string]struct{}
}

func NewRegistry() *Registry {
	return &Registry{names: make(map[string]struct{})}
}

// Register adds an enabled Module. Disabled Modules are deliberately absent
// from the Registry and therefore cannot contribute any capability.
func (r *Registry) Register(mod Module, enabled bool) error {
	if !enabled {
		return nil
	}
	if mod == nil {
		return fmt.Errorf("runtime modules: nil module")
	}
	name := strings.TrimSpace(mod.Name())
	if name == "" {
		return fmt.Errorf("runtime modules: empty name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names == nil {
		r.names = make(map[string]struct{})
	}
	if _, exists := r.names[name]; exists {
		return fmt.Errorf("runtime modules: %q already registered", name)
	}
	r.names[name] = struct{}{}
	r.modules = append(r.modules, mod)
	return nil
}

// Modules returns a stable snapshot in registration order.
func (r *Registry) Modules() []Module {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Module(nil), r.modules...)
}

// PromptContext collects non-empty prompt sections in Module registration
// order. A contribution error identifies its owning Module.
func (r *Registry) PromptContext() ([]PromptSection, error) {
	var sections []PromptSection
	for _, mod := range r.Modules() {
		provider, ok := mod.(PromptContextProvider)
		if !ok {
			continue
		}
		provided, err := provider.PromptContext()
		if err != nil {
			return nil, fmt.Errorf("runtime module %q prompt context: %w", strings.TrimSpace(mod.Name()), err)
		}
		for _, section := range provided {
			if section.Text != "" {
				sections = append(sections, section)
			}
		}
	}
	return sections, nil
}

// RegisterTools invokes ToolRegistrar Modules in registration order. Any
// failure aborts startup and identifies the owning Module.
func (r *Registry) RegisterTools(reg *tools.Registry) error {
	if reg == nil {
		return fmt.Errorf("runtime modules: nil tool registry")
	}
	for _, mod := range r.Modules() {
		registrar, ok := mod.(ToolRegistrar)
		if !ok {
			continue
		}
		if err := registrar.RegisterTools(reg); err != nil {
			return fmt.Errorf("runtime module %q register tools: %w", strings.TrimSpace(mod.Name()), err)
		}
	}
	return nil
}
