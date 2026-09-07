//go:build integration && input_tracking_eval

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/features/inputtracking"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/prompt"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
	"github.com/juex-ai/juex/internal/providers"
)

type inputEvalModule struct{ tools []toolcore.Tool }

func (*inputEvalModule) ID() runtimemodule.ID { return "input-eval" }
func (m *inputEvalModule) Tools(context.Context, runtimemodule.ToolContext) ([]toolcore.Tool, error) {
	return m.tools, nil
}

type inputEvalSample struct {
	Case          string         `json:"case"`
	Enabled       bool           `json:"enabled"`
	Repeat        int            `json:"repeat"`
	Actions       map[string]int `json:"actions"`
	Omissions     int            `json:"omissions"`
	Duplicates    int            `json:"duplicates"`
	FalseChecks   int            `json:"false_checks"`
	Checks        int            `json:"checks"`
	Calls         int            `json:"calls"`
	Usage         llm.Usage      `json:"usage"`
	Unchecked     int            `json:"unchecked_after_turn"`
	Retained      int            `json:"unchecked_after_failure"`
	Checkpoints   int            `json:"checkpoints"`
	Compactions   int            `json:"compactions"`
	Interrupted   bool           `json:"interrupted"`
	Error         string         `json:"error,omitempty"`
	Final         string         `json:"final"`
	ElapsedMillis int64          `json:"elapsed_ms"`
}

type inputEvalProvider struct {
	base     llm.Provider
	mu       sync.Mutex
	failNext bool
	before   func()
	sample   *inputEvalSample
}

func (p *inputEvalProvider) Name() string { return p.base.Name() }
func (p *inputEvalProvider) Complete(ctx context.Context, system string, history []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, system, history, specs, llm.CompleteOptions{})
}

func (p *inputEvalProvider) CompleteWithOptions(ctx context.Context, system string, history []llm.Message, specs []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	p.before()
	p.mu.Lock()
	fail := p.failNext
	p.failNext = false
	p.mu.Unlock()
	if fail {
		return llm.Response{}, errors.New("input-eval injected API interruption")
	}
	response, err := llm.CompleteWithOptions(ctx, p.base, system, history, specs, opts)
	p.mu.Lock()
	p.sample.Calls++
	p.sample.Usage.Add(response.Usage)
	p.mu.Unlock()
	return response, err
}

var inputEvalActions = regexp.MustCompile(`ACTION:([A-Z_]+)`)

// TestLiveInputTrackingAB deliberately uses a real configured model with small
// instrumented actions. Scripted providers are used only by deterministic tests.
func TestLiveInputTrackingAB(t *testing.T) {
	selected := loadLiveConfigs(t)[0]
	profile, err := selected.cfg.ProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	base, err := providers.NewProvider(profile)
	if err != nil {
		t.Fatal(err)
	}
	repeats := 3
	if value := os.Getenv("JUEX_INPUT_EVAL_REPEATS"); value != "" {
		repeats, err = strconv.Atoi(value)
		if err != nil || repeats < 1 {
			t.Fatal("invalid JUEX_INPUT_EVAL_REPEATS")
		}
	}
	var samples []inputEvalSample
	for repeat := 0; repeat < repeats; repeat++ {
		for _, name := range []string{"new_input", "original_task", "multiple_inputs", "historical_input", "api_recovery", "compaction"} {
			// Alternate order across repetitions to reduce warmup/order effects.
			for _, enabled := range []bool{repeat%2 == 1, repeat%2 == 0} {
				t.Logf("starting %s enabled=%v repeat=%d", name, enabled, repeat)
				sample := runInputEvalSample(t, base, name, enabled, repeat)
				samples = append(samples, sample)
				data, _ := json.Marshal(sample)
				t.Log(string(data))
			}
		}
	}
	report := struct {
		Model   string            `json:"model"`
		Samples []inputEvalSample `json:"samples"`
	}{selected.name, samples}
	reportDir := os.Getenv("JUEX_INPUT_EVAL_REPORT_DIR")
	if reportDir == "" {
		reportDir = filepath.Join(repoRoot(t), ".tmp", "reports", "input-tracking", time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(reportDir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(reportDir, "report.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("report: %s", reportDir)
	for index := 0; index < len(samples); index += 2 {
		off, on := samples[index], samples[index+1]
		if off.Enabled {
			off, on = on, off
		}
		if off.Error != "" || on.Error != "" || on.Omissions > off.Omissions || on.Duplicates > off.Duplicates || on.FalseChecks != 0 {
			t.Errorf("A/B gate failed for %s repeat %d: off=%+v on=%+v", on.Case, on.Repeat, off, on)
		}
	}
}

func runInputEvalSample(t *testing.T, base llm.Provider, name string, enabled bool, repeat int) inputEvalSample {
	t.Helper()
	started := time.Now()
	sample := inputEvalSample{Case: name, Enabled: enabled, Repeat: repeat, Actions: map[string]int{}}
	var mu sync.Mutex
	requirements := map[string][]string{}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	target, err := thread.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	var engine *runtime.Engine
	wrapper := &inputEvalProvider{base: base, sample: &sample}
	wrapper.before = func() {
		records, err := engine.PendingInputQueue.Records()
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for id, record := range records {
			if _, ok := requirements[id]; ok {
				continue
			}
			for _, match := range inputEvalActions.FindAllStringSubmatch(record.Message.FirstText(), -1) {
				requirements[id] = append(requirements[id], match[1])
			}
		}
	}
	checkpointCalls := 0
	incoming := []string(nil)
	expected := []string{"ORIGINAL_A", "ORIGINAL_B"}
	query := "Record ACTION:ORIGINAL_A and ACTION:ORIGINAL_B exactly once. Call checkpoint once before doing either action."
	switch name {
	case "new_input":
		incoming = []string{"Also record ACTION:EXTRA exactly once."}
		expected = append(expected, "EXTRA")
	case "original_task":
		incoming = []string{"What is the current progress? Report it by recording ACTION:STATUS exactly once."}
		expected = append(expected, "STATUS")
	case "multiple_inputs":
		incoming = []string{"Also record ACTION:EXTRA_X.", "Also record ACTION:EXTRA_Y.", "Also record ACTION:EXTRA_Z."}
		expected = append(expected, "EXTRA_X", "EXTRA_Y", "EXTRA_Z")
	case "historical_input":
		query = "Record ACTION:OLD exactly once."
		expected = []string{"OLD", "NEW"}
	case "api_recovery", "compaction":
		query = "First record ACTION:BEFORE exactly once, then call checkpoint, then record ACTION:AFTER exactly once."
		expected = []string{"BEFORE", "AFTER"}
	}
	fixture := &inputEvalModule{tools: []toolcore.Tool{
		{Name: "record_action", ExecutionPolicy: toolcore.ToolExecutionSerial,
			Description: "Record one completed action by its requested name. The optional ACTION: prefix identifies the same action. Each call performs the action; do not repeat completed actions.",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}},
			Handler: func(_ context.Context, input map[string]any) (string, error) {
				name, ok := input["name"].(string)
				if !ok || name == "" {
					return "", errors.New("name required")
				}
				name = strings.TrimPrefix(name, "ACTION:")
				mu.Lock()
				sample.Actions[name]++
				mu.Unlock()
				return "Action " + name + " committed.", nil
			}},
		{Name: "checkpoint", ExecutionPolicy: toolcore.ToolExecutionSerial, Description: "Obtain work data at the requested checkpoint.", Schema: map[string]any{"type": "object", "properties": map[string]any{}},
			Handler: func(ctx context.Context, _ map[string]any) (string, error) {
				checkpointCalls++
				if checkpointCalls == 1 {
					for _, message := range incoming {
						if _, err := engine.ReceivePendingInput(ctx, runtime.PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, message)}); err != nil {
							return "", err
						}
					}
					if name == "api_recovery" {
						wrapper.mu.Lock()
						wrapper.failNext = true
						wrapper.mu.Unlock()
					}
					if name == "compaction" {
						return "Checkpoint reached.", engine.RequestContextTransition(runtimemodule.ContextTransitionRequest{Kind: runtimemodule.ContextTransitionCompact})
					}
				}
				return "Checkpoint reached. Continue the requested work.", nil
			}},
	}}
	setup := func() func() {
		bus := events.NewBus()
		detach := target.SubscribeBus(bus)
		engine = &runtime.Engine{Provider: wrapper, SummaryProvider: wrapper, Thread: target, Bus: bus, Prompt: &prompt.Builder{}, TrackUserInputs: enabled, ContextWindow: 32000, Compaction: runtime.DefaultCompactionPolicy(), PendingInputQueue: runtime.NewPendingInputQueue(target.Dir, runtime.PendingInputQueueOptions{Thread: target, TrackUserInputs: enabled})}
		threadContext := runtimemodule.ThreadContext{ID: target.ID, Dir: target.Dir}
		set, err := runtimemodule.BuildAndStartThreadSet(ctx, []runtimemodule.ThreadFactorySpec{
			{ID: "input-eval", Enabled: true, New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) { return fixture, nil }},
			{ID: inputtracking.ModuleID, Enabled: enabled, New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				return inputtracking.New(engine), nil
			}},
		}, threadContext, runtimemodule.ToolContext{Thread: &threadContext})
		if err != nil {
			t.Fatal(err)
		}
		registry, err := runtimemodule.BuildToolRegistry(toolcore.RegistryOptions{}, set)
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.ReplaceThreadRuntimeBundle(target, runtime.ThreadRuntimeReplacement{Modules: set, Tools: registry}); err != nil {
			t.Fatal(err)
		}
		bus.Subscribe(runtime.InputCheckedType, func(event events.Event) {
			data, _ := json.Marshal(event.Payload)
			var payload runtime.InputCheckedPayload
			_ = json.Unmarshal(data, &payload)
			mu.Lock()
			defer mu.Unlock()
			for _, id := range payload.InputIDs {
				sample.Checks++
				for _, action := range requirements[id] {
					if sample.Actions[action] == 0 {
						sample.FalseChecks++
						break
					}
				}
			}
		})
		return func() { detach(); _ = set.CloseThread(context.Background()) }
	}
	closeEngine := setup()
	sample.Final, err = engine.Turn(ctx, query)
	if name == "api_recovery" && err != nil && strings.Contains(err.Error(), "input-eval injected API interruption") && checkpointCalls == 1 {
		sample.Interrupted = true
		open, _ := engine.UncheckedInputs(ctx)
		sample.Retained = len(open)
		closeEngine()
		if closeErr := target.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		target, err = thread.Load(target.Dir)
		if err != nil {
			t.Fatal(err)
		}
		closeEngine = setup()
		sample.Final, err = engine.Turn(ctx, "Resume after the interruption using persisted progress.")
	} else if name == "historical_input" && err == nil {
		sample.Final, err = engine.Turn(ctx, "Now record ACTION:NEW exactly once.")
	}
	if err != nil {
		sample.Error = err.Error()
	}
	if open, openErr := engine.UncheckedInputs(ctx); openErr == nil {
		sample.Unchecked = len(open)
	}
	sample.Checkpoints = checkpointCalls
	sample.Compactions = target.Projection().Counts.GenerationCount - 1
	if name != "historical_input" && checkpointCalls != 1 {
		sample.Error += " checkpoint was not exercised exactly once"
	}
	if name == "api_recovery" && !sample.Interrupted {
		sample.Error += " injected API failure was not recovered"
	}
	if name == "compaction" && sample.Compactions == 0 {
		sample.Error += " compaction was not exercised"
	}
	closeEngine()
	for _, action := range expected {
		if sample.Actions[action] == 0 {
			sample.Omissions++
		}
	}
	for _, count := range sample.Actions {
		if count > 1 {
			sample.Duplicates += count - 1
		}
	}
	sample.ElapsedMillis = time.Since(started).Milliseconds()
	return sample
}
