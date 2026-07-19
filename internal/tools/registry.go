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

	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/errorclass"
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

type ToolGroup string

const (
	ToolGroupFile         ToolGroup = "file"
	ToolGroupChunkedWrite ToolGroup = "chunked_write"
	ToolGroupShell        ToolGroup = "shell"
	ToolGroupSearch       ToolGroup = "search"
	ToolGroupSkill        ToolGroup = "skill"
	ToolGroupMemory       ToolGroup = "memory"
	ToolGroupSessionState ToolGroup = "session_state"
	ToolGroupObservable   ToolGroup = "observable"
	ToolGroupMCP          ToolGroup = "mcp"
)

// GuideSkill returns the builtin guide associated with a compact guided tool
// group. Unguided groups deliberately return no skill.
func (g ToolGroup) GuideSkill() (string, bool) {
	switch g {
	case ToolGroupChunkedWrite:
		return "juex-chunked-write", true
	case ToolGroupSessionState:
		return "juex-session-state", true
	case ToolGroupObservable:
		return "juex-observables", true
	default:
		return "", false
	}
}

type ToolTimeoutPolicy int

const (
	ToolTimeoutDefault ToolTimeoutPolicy = iota
	ToolTimeoutDisabled
)

type ToolDefinition struct {
	Name           string
	Group          ToolGroup
	Description    string
	Schema         map[string]any
	TimeoutPolicy  ToolTimeoutPolicy
	TimeoutSeconds int
}

type ToolTimeoutMode string

const (
	ToolTimeoutModeBounded  ToolTimeoutMode = "bounded"
	ToolTimeoutModeDisabled ToolTimeoutMode = "disabled"
)

type EffectiveTimeout struct {
	Mode    ToolTimeoutMode
	Seconds int
}

// Normalized returns a definition copy with the registry's canonical input
// schema normalization applied.
func (d ToolDefinition) Normalized() ToolDefinition {
	d.Schema = normalizeInputSchema(d.Schema)
	return d
}

type Tool struct {
	Name           string
	Group          ToolGroup
	Description    string
	Schema         map[string]any
	TimeoutPolicy  ToolTimeoutPolicy
	TimeoutSeconds int
	Handler        Handler
	ResultHandler  ResultHandler
}

func (d ToolDefinition) Bind(handler Handler) Tool {
	return Tool{
		Name:           d.Name,
		Group:          d.Group,
		Description:    d.Description,
		Schema:         d.Schema,
		TimeoutPolicy:  d.TimeoutPolicy,
		TimeoutSeconds: d.TimeoutSeconds,
		Handler:        handler,
	}
}

func (d ToolDefinition) BindResult(handler ResultHandler) Tool {
	return Tool{
		Name:           d.Name,
		Group:          d.Group,
		Description:    d.Description,
		Schema:         d.Schema,
		TimeoutPolicy:  d.TimeoutPolicy,
		TimeoutSeconds: d.TimeoutSeconds,
		ResultHandler:  handler,
	}
}

func (t Tool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:           t.Name,
		Group:          t.Group,
		Description:    t.Description,
		Schema:         t.Schema,
		TimeoutPolicy:  t.TimeoutPolicy,
		TimeoutSeconds: t.TimeoutSeconds,
	}
}

func EffectiveToolTimeout(def ToolDefinition, defaultSeconds int) EffectiveTimeout {
	if def.TimeoutPolicy == ToolTimeoutDisabled {
		return EffectiveTimeout{Mode: ToolTimeoutModeDisabled}
	}
	seconds := def.TimeoutSeconds
	if seconds <= 0 {
		seconds = defaultSeconds
	}
	return EffectiveTimeout{
		Mode:    ToolTimeoutModeBounded,
		Seconds: normalizedTimeoutSeconds(seconds),
	}
}

type CallInfo struct {
	TimeoutSeconds   int          `json:"timeout_seconds"`
	TimedOut         bool         `json:"timed_out,omitempty"`
	ErrorKind        string       `json:"error_kind,omitempty"`
	RawCause         string       `json:"raw_cause,omitempty"`
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
	t.Schema = t.Definition().Normalized().Schema
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
		definition := t.Definition().Normalized()
		out = append(out, llm.ToolSpec{
			Name:        definition.Name,
			Description: definition.Description,
			Schema:      definition.Schema,
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
		info.setObservation(ObservationOptions{
			ToolName: name,
			Input:    input,
			Err:      err,
		})
		return "", info, err
	}
	timeoutSeconds := r.timeoutSecondsFor(t)
	info := CallInfo{TimeoutSeconds: timeoutSeconds}
	var err error
	input, err = NormalizeCallInputForDispatch(input)
	if err != nil {
		if errors.Is(err, ErrMalformedRawArguments) {
			err = malformedRawArgumentsError(name)
			info.setObservation(ObservationOptions{
				ToolName: name,
				Input:    input,
				Err:      err,
			})
			return "", info, err
		}
		err = fmt.Errorf("tools: %s: %w", name, err)
		info.setObservation(ObservationOptions{
			ToolName: name,
			Input:    input,
			Err:      err,
		})
		return "", info, err
	}
	callInput := cloneCallInput(input)
	callCtx := ctx
	var cancel context.CancelFunc
	if timeoutSeconds > 0 {
		callCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
		defer cancel()
	}
	if err := cancellation.ContextError(callCtx); err != nil {
		info.ErrorKind = errorclass.KindForError(err)
		info.setObservation(ObservationOptions{
			ToolName:  name,
			Input:     callInput,
			Err:       err,
			ErrorKind: info.ErrorKind,
		})
		return "", info, err
	}
	result, err := callToolHandler(callCtx, t, callInput)
	rawErr := err
	result.Text = SanitizeOutputText(result.Text).Text
	out := result.Text
	info.StructuredResult = result.Structured
	if timeoutSeconds > 0 && structuredResultTimedOut(result.Structured) && ctx.Err() == nil {
		info.TimedOut = true
		info.ErrorKind = string(errorclass.KindTimeout)
		info.RawCause = rawCauseFor(rawErr, "structured result timed_out=true")
		err = toolTimeoutError(name, timeoutSeconds)
	} else if timeoutSeconds > 0 && rawErr != nil && errorclass.IsTimeout(rawErr) && ctx.Err() == nil {
		info.TimedOut = true
		info.ErrorKind = string(errorclass.KindTimeout)
		info.RawCause = rawErr.Error()
		err = toolTimeoutError(name, timeoutSeconds)
	} else if timeoutSeconds > 0 && errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		info.TimedOut = true
		info.ErrorKind = string(errorclass.KindTimeout)
		info.RawCause = rawCauseFor(rawErr, callCtx.Err().Error())
		err = toolTimeoutError(name, timeoutSeconds)
	} else if ctxErr := cancellation.ContextError(ctx); ctxErr != nil {
		err = ctxErr
	}
	if err != nil && info.ErrorKind == "" {
		info.ErrorKind = errorclass.KindForError(err)
	}
	info.setObservation(ObservationOptions{
		ToolName:         name,
		Input:            callInput,
		Content:          out,
		Err:              err,
		TimedOut:         info.TimedOut,
		ErrorKind:        info.ErrorKind,
		RawCause:         info.RawCause,
		StructuredResult: result.Structured,
	})
	return out, info, err
}

func (i *CallInfo) setObservation(opts ObservationOptions) {
	obs := NewObservation(opts)
	i.Observation = &obs
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

func rawCauseFor(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
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
	return EffectiveToolTimeout(t.Definition(), r.defaultTimeoutSeconds).Seconds
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
