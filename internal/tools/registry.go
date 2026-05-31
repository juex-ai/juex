// Package tools defines the Tool type and a Registry that the runtime uses
// to dispatch model-issued tool calls.
package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

const (
	DefaultTimeoutSeconds = 60
	MaxTimeoutSeconds     = 300
)

type Handler func(ctx context.Context, input map[string]any) (string, error)

type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Handler     Handler
}

type CallInfo struct {
	TimeoutSeconds int  `json:"timeout_seconds"`
	TimedOut       bool `json:"timed_out,omitempty"`
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
			Schema:      schemaWithReservedTimeout(t.Schema),
		})
	}
	return out
}

// Call dispatches to the handler. The output is whatever string the handler
// returned; an error is converted to an error string by the caller.
func (r *Registry) Call(ctx context.Context, name string, input map[string]any) (string, error) {
	out, _, err := r.CallWithInfo(ctx, name, input)
	return out, err
}

func (r *Registry) CallWithInfo(ctx context.Context, name string, input map[string]any) (string, CallInfo, error) {
	timeoutSeconds := CallTimeoutSeconds(input)
	info := CallInfo{TimeoutSeconds: timeoutSeconds}
	t, ok := r.Get(name)
	if !ok {
		return "", info, fmt.Errorf("tools: unknown tool %q", name)
	}
	callInput := cloneCallInput(input)
	if schemaDeclaresProperty(t.Schema, "timeout") {
		callInput["timeout"] = timeoutSeconds
	} else {
		delete(callInput, "timeout")
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	out, err := t.Handler(callCtx, callInput)
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		info.TimedOut = true
		return out, info, fmt.Errorf("tools: %s timed out after %ds", name, timeoutSeconds)
	}
	return out, info, err
}

func CallTimeoutSeconds(input map[string]any) int {
	timeoutSeconds, ok := toInt(input["timeout"])
	if !ok || timeoutSeconds <= 0 {
		return DefaultTimeoutSeconds
	}
	if timeoutSeconds > MaxTimeoutSeconds {
		return MaxTimeoutSeconds
	}
	return timeoutSeconds
}

func cloneCallInput(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}
