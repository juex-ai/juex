package tools

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestToolDefinitionBindsHandlersAndSpecsStayProviderFacing(t *testing.T) {
	definition := ToolDefinition{
		Name:                      "classified",
		Guide:                     ToolGuide{Loader: "docs_load", Name: "reference"},
		MalformedArgumentsMessage: "retry with smaller input",
		Group:                     ToolGroupSearch,
		Description:               "A classified tool.",
		Schema:                    map[string]any{"type": "object"},
		TimeoutPolicy:             ToolTimeoutDefault,
		TimeoutSeconds:            12,
	}
	handler := func(context.Context, map[string]any) (string, error) { return "ok", nil }
	resultHandler := func(context.Context, map[string]any) (Result, error) {
		return Result{Text: "ok"}, nil
	}

	bound := definition.Bind(handler)
	if bound.Handler == nil || bound.ResultHandler != nil {
		t.Fatalf("Bind handlers = Handler:%t ResultHandler:%t", bound.Handler != nil, bound.ResultHandler != nil)
	}
	if got := bound.Definition(); !reflect.DeepEqual(got, definition) {
		t.Fatalf("bound definition = %#v, want %#v", got, definition)
	}
	boundResult := definition.BindResult(resultHandler)
	if boundResult.Handler != nil || boundResult.ResultHandler == nil {
		t.Fatalf("BindResult handlers = Handler:%t ResultHandler:%t", boundResult.Handler != nil, boundResult.ResultHandler != nil)
	}
	if got := boundResult.Definition(); !reflect.DeepEqual(got, definition) {
		t.Fatalf("bound result definition = %#v, want %#v", got, definition)
	}

	registry := NewRegistry()
	registry.MustRegister(bound)
	wantSpecs := []llm.ToolSpec{{Name: definition.Name, Description: definition.Description, Schema: definition.Schema}}
	if got := registry.Specs(); !reflect.DeepEqual(got, wantSpecs) {
		t.Fatalf("provider specs = %#v, want %#v", got, wantSpecs)
	}
}

func TestEffectiveToolTimeout(t *testing.T) {
	bounded := EffectiveToolTimeout(ToolDefinition{}, 90)
	if bounded.Mode != ToolTimeoutModeBounded || bounded.Seconds != 90 {
		t.Fatalf("bounded timeout = %+v", bounded)
	}
	disabled := EffectiveToolTimeout(ToolDefinition{TimeoutPolicy: ToolTimeoutDisabled}, 90)
	if disabled.Mode != ToolTimeoutModeDisabled || disabled.Seconds != 0 {
		t.Fatalf("disabled timeout = %+v", disabled)
	}
	capped := EffectiveToolTimeout(ToolDefinition{TimeoutSeconds: MaxTimeoutSeconds + 1}, 90)
	if capped.Mode != ToolTimeoutModeBounded || capped.Seconds != MaxTimeoutSeconds {
		t.Fatalf("capped timeout = %+v", capped)
	}
}

func TestToolDefinitionNormalizedCopiesAndNormalizesSchema(t *testing.T) {
	definition := ToolDefinition{
		Name: "nullable",
		Schema: map[string]any{
			"type":                 "object",
			"additionalProperties": nil,
			"properties": map[string]any{
				"query": nil,
				"items": map[string]any{
					"type":  "array",
					"items": nil,
				},
			},
		},
	}

	normalized := definition.Normalized()
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{},
			"items": map[string]any{
				"type":  "array",
				"items": map[string]any{},
			},
		},
	}
	if !reflect.DeepEqual(normalized.Schema, want) {
		t.Fatalf("normalized schema = %#v, want %#v", normalized.Schema, want)
	}
	properties := definition.Schema["properties"].(map[string]any)
	if properties["query"] != nil || definition.Schema["additionalProperties"] != nil {
		t.Fatalf("normalization mutated source definition: %#v", definition.Schema)
	}
}

func TestRegistry_RegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	tool := Tool{Name: "x", Handler: func(ctx context.Context, in map[string]any) (string, error) { return "", nil }}
	if err := r.Register(tool); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register(tool); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestRegistry_CallWithInfoSkipsHandlerWhenContextCancelled(t *testing.T) {
	r := NewRegistry()
	called := false
	r.MustRegister(Tool{
		Name:   "cancelled",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			called = true
			return "should not run", nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, info, err := r.CallWithInfo(ctx, "cancelled", map[string]any{})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("handler ran after context cancellation")
	}
	if out != "" {
		t.Fatalf("out = %q, want empty output", out)
	}
	if info.ErrorKind == "" {
		t.Fatalf("missing error classification: %+v", info)
	}
}

func TestRegistry_NormalizesNullSchemaEntries(t *testing.T) {
	r := NewRegistry()
	err := r.Register(Tool{
		Name: "x",
		Schema: map[string]any{
			"type":                 "object",
			"additionalProperties": nil,
			"default":              nil,
			"properties": map[string]any{
				"query":    nil,
				"mode":     map[string]any{"enum": []any{"all", nil}},
				"bad_enum": map[string]any{"enum": nil},
			},
			"patternProperties": map[string]any{"^x-": nil},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := r.Get("x")
	if !ok {
		t.Fatal("expected registered tool")
	}
	if _, ok := tool.Schema["additionalProperties"]; ok {
		t.Fatalf("additionalProperties null should be removed: %+v", tool.Schema)
	}
	if _, ok := tool.Schema["default"]; ok {
		t.Fatalf("default null should be removed: %+v", tool.Schema)
	}
	props, ok := tool.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %+v", tool.Schema["properties"])
	}
	if query, ok := props["query"].(map[string]any); !ok || len(query) != 0 {
		t.Fatalf("null property schema should become empty object: %+v", props["query"])
	}
	badEnum, _ := props["bad_enum"].(map[string]any)
	if _, ok := badEnum["enum"]; ok {
		t.Fatalf("enum:null should be removed: %+v", badEnum)
	}
	mode, _ := props["mode"].(map[string]any)
	enum, _ := mode["enum"].([]any)
	if len(enum) != 2 || enum[1] != nil {
		t.Fatalf("enum null values should be preserved: %+v", enum)
	}
	patternProps, ok := tool.Schema["patternProperties"].(map[string]any)
	if !ok {
		t.Fatalf("patternProperties = %+v", tool.Schema["patternProperties"])
	}
	if pattern, ok := patternProps["^x-"].(map[string]any); !ok || len(pattern) != 0 {
		t.Fatalf("null pattern property schema should become empty object: %+v", patternProps["^x-"])
	}
}

func TestRegistry_SpecsPreserveToolSchema(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{
		Name: "slow",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "string"}},
			"required":   []string{"value"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) { return "ok", nil },
	}); err != nil {
		t.Fatal(err)
	}

	specs := r.Specs()
	if len(specs) != 1 {
		t.Fatalf("spec count = %d", len(specs))
	}
	props, ok := specs[0].Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %+v", specs[0].Schema["properties"])
	}
	if _, ok := props["timeout"]; ok {
		t.Fatalf("runtime timeout should not be injected into model schema: %+v", props)
	}
}

func TestRegistry_CallWithInfoAppliesConfiguredTimeout(t *testing.T) {
	r := NewRegistryWithOptions(RegistryOptions{DefaultTimeoutSeconds: 1})
	seen := make(chan map[string]any, 1)
	if err := r.Register(Tool{
		Name:   "slow",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			seen <- in
			<-ctx.Done()
			return "", ctx.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	out, info, err := r.CallWithInfo(context.Background(), "slow", map[string]any{"timeout": 9, "value": "x"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if out != "" {
		t.Fatalf("out = %q, want empty", out)
	}
	if !info.TimedOut || info.TimeoutSeconds != 1 {
		t.Fatalf("info = %+v, want timed out after 1s", info)
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("err = %v, want timed out after 1s", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
	input := <-seen
	if input["timeout"] != 9 {
		t.Fatalf("model input timeout should not be interpreted as runtime policy: %+v", input)
	}
	if input["value"] != "x" {
		t.Fatalf("handler input = %+v", input)
	}
}

func TestRegistry_CallWithInfoReturnsParentCancellation(t *testing.T) {
	r := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Register(Tool{
		Name:   "soft-cancel",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			cancel()
			return "partial output", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, _, err := r.CallWithInfo(ctx, "soft-cancel", map[string]any{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if out != "partial output" {
		t.Fatalf("out = %q, want partial output", out)
	}
}

func TestRegistry_CallWithInfoUsesToolTimeoutOverride(t *testing.T) {
	r := NewRegistryWithOptions(RegistryOptions{DefaultTimeoutSeconds: 5})
	if err := r.Register(Tool{
		Name:           "quick",
		Schema:         map[string]any{"type": "object"},
		TimeoutSeconds: 2,
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "ok", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "quick", map[string]any{"timeout": 9})
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q, want ok", out)
	}
	if info.TimeoutSeconds != 2 {
		t.Fatalf("timeout = %d, want tool override 2", info.TimeoutSeconds)
	}
}

func TestRegistry_CallWithInfoParsesRawArgumentsBeforeDispatch(t *testing.T) {
	r := NewRegistry()
	seen := make(chan map[string]any, 1)
	if err := r.Register(Tool{
		Name: "echo",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"timeout": map[string]any{"type": "integer"},
			},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			seen <- in
			return "ok", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "echo", map[string]any{
		"_raw_arguments": `{"value":"x","timeout":2}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q, want ok", out)
	}
	if info.TimeoutSeconds != DefaultTimeoutSeconds {
		t.Fatalf("timeout = %d, want default", info.TimeoutSeconds)
	}
	input := <-seen
	timeout, ok := IntArgument(input["timeout"])
	if input["value"] != "x" || !ok || timeout != 2 {
		t.Fatalf("handler input = %+v, want decoded raw arguments", input)
	}
	if _, ok := input["_raw_arguments"]; ok {
		t.Fatalf("raw arguments leaked to handler: %+v", input)
	}
}

func TestRegistry_CallWithInfoRejectsMalformedRawArgumentsBeforeDispatch(t *testing.T) {
	r := NewRegistry()
	called := false
	if err := r.Register(Tool{
		Name: "echo",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "string"}},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			called = true
			return "ok", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	_, _, err := r.CallWithInfo(context.Background(), "echo", map[string]any{
		"_raw_arguments": `{"value":"unterminated`,
	})
	if err == nil {
		t.Fatal("expected malformed raw arguments error")
	}
	if called {
		t.Fatal("handler was called for malformed raw arguments")
	}
	msg := err.Error()
	if !strings.Contains(msg, "provider returned malformed tool arguments") ||
		!strings.Contains(msg, "retry with valid JSON and smaller content") {
		t.Fatalf("error = %q, want provider malformed arguments guidance", msg)
	}
}

func TestRegistryCallWithInfoKeepsStructuredResult(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{
		Name:   "structured",
		Schema: map[string]any{"type": "object"},
		ResultHandler: func(ctx context.Context, input map[string]any) (Result, error) {
			return Result{
				Text:       "ok",
				Structured: map[string]any{"answer": 42},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "structured", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q, want ok", out)
	}
	structured, ok := info.StructuredResult.(map[string]any)
	if !ok || structured["answer"] != 42 {
		t.Fatalf("structured result = %#v", info.StructuredResult)
	}
	if info.Observation == nil {
		t.Fatal("observation = nil")
	}
	if info.Observation.ToolName != "structured" || info.Observation.Content != "ok" {
		t.Fatalf("observation = %+v, want structured tool output", info.Observation)
	}
	obsStructured, ok := info.Observation.StructuredResult.(map[string]any)
	if !ok || obsStructured["answer"] != 42 {
		t.Fatalf("observation structured result = %#v, want answer 42", info.Observation.StructuredResult)
	}
}

func TestRegistryCallWithInfoHonorsStructuredTimeout(t *testing.T) {
	r := NewRegistryWithOptions(RegistryOptions{DefaultTimeoutSeconds: 1})
	if err := r.Register(Tool{
		Name:   "structured_timeout",
		Schema: map[string]any{"type": "object"},
		ResultHandler: func(ctx context.Context, input map[string]any) (Result, error) {
			return Result{
				Text:       "partial output",
				Structured: coreToolstestTimedOutStructuredTestResult{},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "structured_timeout", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if out != "partial output" {
		t.Fatalf("out = %q, want partial output", out)
	}
	if !info.TimedOut || info.TimeoutSeconds != 1 {
		t.Fatalf("info = %+v, want structured timeout after 1s", info)
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("err = %v, want timed out after 1s", err)
	}
	if info.Observation == nil || !info.Observation.TimedOut || info.Observation.Error == "" {
		t.Fatalf("observation = %+v, want timed-out error observation", info.Observation)
	}
}

func TestRegistryCallWithInfoClassifiesDirectDeadlineExceeded(t *testing.T) {
	r := NewRegistryWithOptions(RegistryOptions{DefaultTimeoutSeconds: 1})
	if err := r.Register(Tool{
		Name:   "deadline",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, input map[string]any) (string, error) {
			return "partial output", context.DeadlineExceeded
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "deadline", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if out != "partial output" {
		t.Fatalf("out = %q, want partial output", out)
	}
	if !info.TimedOut || info.ErrorKind != "timeout" {
		t.Fatalf("info = %+v, want timeout classification", info)
	}
	if !strings.Contains(info.RawCause, "context deadline exceeded") {
		t.Fatalf("raw cause = %q, want original deadline cause", info.RawCause)
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err = %q, should use public timeout wording", err.Error())
	}
	if !strings.Contains(err.Error(), "tools: deadline timed out after 1s") {
		t.Fatalf("err = %q, want public tool timeout", err.Error())
	}
	if info.Observation == nil || !info.Observation.TimedOut || info.Observation.ErrorKind != "timeout" {
		t.Fatalf("observation = %+v, want timeout observation", info.Observation)
	}
	if !strings.Contains(info.Observation.RawCause, "context deadline exceeded") {
		t.Fatalf("observation raw cause = %q, want original deadline cause", info.Observation.RawCause)
	}
}

func TestRegistryCallWithInfoObservationCapturesStructuredExitCode(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{
		Name:   "check",
		Schema: map[string]any{"type": "object"},
		ResultHandler: func(ctx context.Context, input map[string]any) (Result, error) {
			return Result{
				Text:       "opaque failure output",
				Structured: coreToolstestExitCodeStructuredTestResult{code: 9},
			}, errors.New("check failed")
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, info, err := r.CallWithInfo(context.Background(), "check", map[string]any{"path": "artifact.txt"})
	if err == nil {
		t.Fatal("expected check error")
	}
	if out != "opaque failure output" {
		t.Fatalf("out = %q, want opaque failure output", out)
	}
	if info.Observation == nil {
		t.Fatal("observation = nil")
	}
	if info.Observation.ExitCode == nil || *info.Observation.ExitCode != 9 {
		t.Fatalf("observation exit code = %+v, want 9", info.Observation.ExitCode)
	}
	if info.Observation.Error != "check failed" {
		t.Fatalf("observation error = %q, want check failed", info.Observation.Error)
	}
}

func TestObservationWithRuntimeContextPreservesSpecificExitCode(t *testing.T) {
	explicitCode := 42
	tests := []struct {
		name string
		obs  Observation
		want int
	}{
		{
			name: "explicit option",
			obs:  NewObservation(ObservationOptions{ExitCode: &explicitCode}),
			want: 42,
		},
		{
			name: "structured result",
			obs: NewObservation(ObservationOptions{
				StructuredResult: coreToolstestExitCodeStructuredTestResult{code: 9},
			}),
			want: 9,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.obs.WithRuntimeContext(
				"exec_command",
				"call_1",
				map[string]any{"cmd": "false"},
				"runtime output",
				testExitError(7),
			)
			if got.ExitCode == nil || *got.ExitCode != tt.want {
				t.Fatalf("exit code = %+v, want %d", got.ExitCode, tt.want)
			}
			if got.Error == "" {
				t.Fatal("error should still be captured from runtime context")
			}
			if got.ToolName != "exec_command" || got.ToolUseID != "call_1" {
				t.Fatalf("runtime identity = %q/%q, want exec_command/call_1", got.ToolName, got.ToolUseID)
			}
		})
	}
}

type coreToolstestExitCodeStructuredTestResult struct {
	code int
}

type coreToolstestTimedOutStructuredTestResult struct{}

func (r coreToolstestExitCodeStructuredTestResult) ToolCallExitCode() (int, bool) {
	return r.code, true
}

func (coreToolstestTimedOutStructuredTestResult) ToolCallTimedOut() bool {
	return true
}

type testExitError int

func (e testExitError) Error() string                 { return "process exit" }
func (e testExitError) ToolCallExitCode() (int, bool) { return int(e), true }
