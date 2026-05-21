// Package tools defines the Tool type and a Registry that the runtime uses
// to dispatch model-issued tool calls.
package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/juex-ai/juex/internal/llm"
)

type Handler func(ctx context.Context, input map[string]any) (string, error)

type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Handler     Handler
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds t. Returns an error if a tool with the same name already exists.
func (r *Registry) Register(t Tool) error {
	if t.Name == "" {
		return fmt.Errorf("tools: empty name")
	}
	if t.Handler == nil {
		return fmt.Errorf("tools: %s: nil handler", t.Name)
	}
	t.Schema = normalizeInputSchema(t.Schema)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Name]; ok {
		return fmt.Errorf("tools: %s already registered", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// MustRegister panics on error. Convenient for builtin registration at startup.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns every registered tool, sorted by name for determinism.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Specs converts the registry to the LLM-facing ToolSpec list.
func (r *Registry) Specs() []llm.ToolSpec {
	tools := r.List()
	out := make([]llm.ToolSpec, 0, len(tools))
	for _, t := range tools {
		out = append(out, llm.ToolSpec{
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.Schema,
		})
	}
	return out
}

// Call dispatches to the handler. The output is whatever string the handler
// returned; an error is converted to an error string by the caller.
func (r *Registry) Call(ctx context.Context, name string, input map[string]any) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("tools: unknown tool %q", name)
	}
	return t.Handler(ctx, input)
}
