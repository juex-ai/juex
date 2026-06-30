// Package tools defines the Tool type and a Registry that the runtime uses
// to dispatch model-issued tool calls.
package tools

import (
	"context"
	"encoding/json"
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

var ErrMalformedRawArguments = errors.New("provider returned malformed tool arguments; retry with smaller/chunked content")

type Handler func(ctx context.Context, input map[string]any) (string, error)

type Result struct {
	Text       string
	Structured any
}

type ResultHandler func(ctx context.Context, input map[string]any) (Result, error)

type Tool struct {
	Name           string
	Description    string
	Schema         map[string]any
	TimeoutSeconds int
	Handler        Handler
	ResultHandler  ResultHandler
}

type CallInfo struct {
	TimeoutSeconds   int          `json:"timeout_seconds"`
	TimedOut         bool         `json:"timed_out,omitempty"`
	StructuredResult any          `json:"structured_result,omitempty"`
	Observation      *Observation `json:"-"`
}

type structuredTimeoutResult interface {
	ToolCallTimedOut() bool
}

type Registry struct {
	mu                    sync.RWMutex
	tools                 map[string]Tool
	defaultTimeoutSeconds int
}

type RegistryOptions struct {
	DefaultTimeoutSeconds int
}

func NewRegistry() *Registry {
	return NewRegistryWithOptions(RegistryOptions{})
}

func NewRegistryWithOptions(opts RegistryOptions) *Registry {
	return &Registry{
		tools:                 make(map[string]Tool),
		defaultTimeoutSeconds: normalizedTimeoutSeconds(opts.DefaultTimeoutSeconds),
	}
}

// Register adds t. Returns an error if a tool with the same name already exists.
func (r *Registry) Register(t Tool) error {
	if t.Name == "" {
		return fmt.Errorf("tools: empty name")
	}
	if t.Handler == nil && t.ResultHandler == nil {
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

func (r *Registry) TimeoutSecondsFor(name string) int {
	if r == nil {
		return DefaultTimeoutSeconds
	}
	t, ok := r.Get(name)
	if !ok {
		return normalizedTimeoutSeconds(r.defaultTimeoutSeconds)
	}
	return r.timeoutSecondsFor(t)
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
			Schema:      normalizeInputSchema(t.Schema),
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
	t, ok := r.Get(name)
	if !ok {
		info := CallInfo{TimeoutSeconds: normalizedTimeoutSeconds(r.defaultTimeoutSeconds)}
		err := fmt.Errorf("tools: unknown tool %q", name)
		obs := NewObservation(ObservationOptions{
			ToolName: name,
			Input:    input,
			Err:      err,
		})
		info.Observation = &obs
		return "", info, err
	}
	timeoutSeconds := r.timeoutSecondsFor(t)
	info := CallInfo{TimeoutSeconds: timeoutSeconds}
	var err error
	input, err = NormalizeCallInputForDispatch(input)
	if err != nil {
		if errors.Is(err, ErrMalformedRawArguments) {
			err = malformedRawArgumentsError(name)
			obs := NewObservation(ObservationOptions{
				ToolName: name,
				Input:    input,
				Err:      err,
			})
			info.Observation = &obs
			return "", info, err
		}
		err = fmt.Errorf("tools: %s: %w", name, err)
		obs := NewObservation(ObservationOptions{
			ToolName: name,
			Input:    input,
			Err:      err,
		})
		info.Observation = &obs
		return "", info, err
	}
	callInput := cloneCallInput(input)
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	result, err := callToolHandler(callCtx, t, callInput)
	out := result.Text
	info.StructuredResult = result.Structured
	if structuredResultTimedOut(result.Structured) && ctx.Err() == nil {
		info.TimedOut = true
		err = toolTimeoutError(name, timeoutSeconds)
		obs := NewObservation(ObservationOptions{
			ToolName:         name,
			Input:            callInput,
			Content:          out,
			Err:              err,
			TimedOut:         info.TimedOut,
			StructuredResult: result.Structured,
		})
		info.Observation = &obs
		return out, info, err
	}
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		info.TimedOut = true
		err = toolTimeoutError(name, timeoutSeconds)
		obs := NewObservation(ObservationOptions{
			ToolName:         name,
			Input:            callInput,
			Content:          out,
			Err:              err,
			TimedOut:         info.TimedOut,
			StructuredResult: result.Structured,
		})
		info.Observation = &obs
		return out, info, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		obs := NewObservation(ObservationOptions{
			ToolName:         name,
			Input:            callInput,
			Content:          out,
			Err:              ctxErr,
			TimedOut:         info.TimedOut,
			StructuredResult: result.Structured,
		})
		info.Observation = &obs
		return out, info, ctxErr
	}
	obs := NewObservation(ObservationOptions{
		ToolName:         name,
		Input:            callInput,
		Content:          out,
		Err:              err,
		TimedOut:         info.TimedOut,
		StructuredResult: result.Structured,
	})
	info.Observation = &obs
	return out, info, err
}

func callToolHandler(ctx context.Context, t Tool, input map[string]any) (Result, error) {
	if t.ResultHandler != nil {
		return t.ResultHandler(ctx, input)
	}
	out, err := t.Handler(ctx, input)
	return Result{Text: out}, err
}

func structuredResultTimedOut(result any) bool {
	reporter, ok := result.(structuredTimeoutResult)
	return ok && reporter.ToolCallTimedOut()
}

func toolTimeoutError(name string, timeoutSeconds int) error {
	return fmt.Errorf("tools: %s timed out after %ds", name, timeoutSeconds)
}

func malformedRawArgumentsError(name string) error {
	if name == "write_chunk" {
		return fmt.Errorf("tools: %s: provider returned malformed tool arguments; retry with smaller write_chunk content, preferably no more than %d chars or %d bytes per chunk", name, chunkWriteRecommendedChunkChars, chunkWriteRecommendedChunkBytes)
	}
	return fmt.Errorf("tools: %s: %w", name, ErrMalformedRawArguments)
}

// NormalizeCallInput decodes OpenAI-compatible fallback arguments before a
// tool sees them. Provider adapters should normally return structured input,
// but this keeps leaked `_raw_arguments` payloads from failing builtin tools.
func NormalizeCallInput(input map[string]any) map[string]any {
	normalized, _ := normalizeCallInput(input, false)
	return normalized
}

func NormalizeCallInputForDispatch(input map[string]any) (map[string]any, error) {
	return normalizeCallInput(input, true)
}

func normalizeCallInput(input map[string]any, strict bool) (map[string]any, error) {
	if input == nil {
		return nil, nil
	}
	raw, ok := input["_raw_arguments"].(string)
	if !ok || raw == "" {
		return input, nil
	}
	decoded, ok := decodeRawArguments(raw)
	if !ok {
		if strict {
			return nil, ErrMalformedRawArguments
		}
		return input, nil
	}
	out := make(map[string]any, len(decoded)+len(input))
	for k, v := range decoded {
		out[k] = v
	}
	for k, v := range input {
		if k == "_raw_arguments" {
			continue
		}
		out[k] = v
	}
	return out, nil
}

func decodeRawArguments(raw string) (map[string]any, bool) {
	rawBytes := []byte(raw)
	var decoded map[string]any
	if err := json.Unmarshal(rawBytes, &decoded); err == nil && decoded != nil {
		return decoded, true
	}
	var encoded string
	if err := json.Unmarshal(rawBytes, &encoded); err != nil || encoded == "" {
		return nil, false
	}
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil || decoded == nil {
		return nil, false
	}
	return decoded, true
}

func (r *Registry) timeoutSecondsFor(t Tool) int {
	if t.TimeoutSeconds > 0 {
		return normalizedTimeoutSeconds(t.TimeoutSeconds)
	}
	return normalizedTimeoutSeconds(r.defaultTimeoutSeconds)
}

func normalizedTimeoutSeconds(timeoutSeconds int) int {
	if timeoutSeconds <= 0 {
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
