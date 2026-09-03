package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/modules/promptcontext"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/tools"
)

type CapabilityCase struct {
	Name       string
	Prompt     string
	Files      map[string]string
	Hooks      func(workDir string) hooks.Config
	ExtraTools []tools.Tool
	Script     []CapabilityStep
	Contract   ContractExpectations
	Assert     func(*testing.T, CapabilityResult)
}

type CapabilityStep func(CapabilityState) llm.Response

type CapabilityState struct {
	WorkDir   string
	CallIndex int
	System    string
	History   []llm.Message
	Tools     []llm.ToolSpec
}

type ProviderSnapshot struct {
	System       string
	History      []llm.Message
	ToolSpecName []string
}

type CapabilityResult struct {
	Name           string             `json:"name"`
	Success        bool               `json:"success"`
	ProviderCalls  int                `json:"provider_calls"`
	ToolCalls      int                `json:"tool_calls"`
	ErrorToolCalls int                `json:"error_tool_calls"`
	ContextBytes   int                `json:"context_bytes"`
	ToolBytes      int                `json:"tool_bytes"`
	Elapsed        time.Duration      `json:"-"`
	ElapsedMS      int64              `json:"elapsed_ms"`
	Events         map[string]int     `json:"events"`
	ToolNames      map[string]int     `json:"tool_names"`
	Contract       ContractReport     `json:"contract"`
	FinalText      string             `json:"final_text"`
	Error          string             `json:"error,omitempty"`
	WorkDir        string             `json:"-"`
	ThreadDir      string             `json:"-"`
	TranscriptText string             `json:"-"`
	Snapshots      []ProviderSnapshot `json:"-"`
}

func (r CapabilityResult) Report() string {
	type report CapabilityResult
	out, err := json.MarshalIndent(report(r), "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"name":%q,"success":false,"error":%q}`, r.Name, err.Error())
	}
	return string(out)
}

type capabilityProvider struct {
	workDir string
	steps   []CapabilityStep
	called  int
	snaps   []ProviderSnapshot
}

func (p *capabilityProvider) Name() string { return "capability-script" }

func (p *capabilityProvider) Complete(ctx context.Context, sys string, hist []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	idx := p.called
	p.called++
	history := append([]llm.Message(nil), hist...)
	toolNames := make([]string, 0, len(specs))
	for _, spec := range specs {
		toolNames = append(toolNames, spec.Name)
	}
	p.snaps = append(p.snaps, ProviderSnapshot{
		System:       sys,
		History:      history,
		ToolSpecName: toolNames,
	})
	if idx >= len(p.steps) {
		return llm.Response{}, fmt.Errorf("capability script exhausted at call %d", idx)
	}
	return p.steps[idx](CapabilityState{
		WorkDir:   p.workDir,
		CallIndex: idx,
		System:    sys,
		History:   history,
		Tools:     specs,
	}), nil
}

func RunCapabilityCase(t *testing.T, tc CapabilityCase) CapabilityResult {
	t.Helper()
	if tc.Name == "" {
		t.Fatal("capability case requires name")
	}
	if len(tc.Script) == 0 {
		t.Fatal("capability case requires script")
	}
	workDir := t.TempDir()
	for rel, body := range tc.Files {
		writeCapabilityFile(t, filepath.Join(workDir, rel), body)
	}

	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, tools.BuiltinOptions{WorkDir: workDir, Shell: tools.DefaultShellProfile()})
	for _, tool := range tc.ExtraTools {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register extra tool %q: %v", tool.Name, err)
		}
	}

	store := thread.NewStore(filepath.Join(workDir, ".juex", "agent-state"))
	worker, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close() })

	bus := events.NewBus()
	worker.SubscribeBus(bus)

	var hookRunner hooks.PolicyRunner
	if tc.Hooks != nil {
		runner, err := hooks.NewRunner(tc.Hooks(workDir))
		if err != nil {
			t.Fatalf("create hooks runner: %v", err)
		}
		hookRunner = runner
	}

	provider := &capabilityProvider{workDir: workDir, steps: tc.Script}
	engine := &runtime.Engine{
		Provider:       provider,
		Tools:          reg,
		Bus:            bus,
		Thread:         worker,
		Prompt:         capabilityPromptBuilder(workDir, worker),
		WorkDir:        workDir,
		MediaDir:       filepath.Join(workDir, ".juex", "media"),
		RuntimeContext: runtimemodule.RuntimeContext{WorkDir: workDir},
	}
	if hookRunner != nil {
		hookModule := hooks.NewModule(hookRunner, hooks.ModuleOptions{BaseRequest: hooks.Request{
			ThreadID:              worker.ID,
			CWD:                   workDir,
			WorkspaceRoots:        []string{workDir},
			GenerationJournalPath: worker.CurrentGenerationJournalPath(),
		}})
		engine.RuntimeModules, err = runtimemodule.BuildRuntimeSet(context.Background(), []runtimemodule.RuntimeFactorySpec{{
			ID: hooks.ModuleID, Enabled: true,
			New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
				return hookModule, nil
			},
		}}, engine.RuntimeContext, runtimemodule.ToolContext{Runtime: engine.RuntimeContext})
		if err != nil {
			t.Fatalf("build hook module: %v", err)
		}
	}

	start := time.Now()
	finalText, turnErr := engine.Turn(context.Background(), tc.Prompt)
	elapsed := time.Since(start)
	result := collectCapabilityResult(t, tc.Name, workDir, worker, finalText, elapsed, provider, tc.Contract)
	if turnErr != nil {
		result.Success = false
		result.Error = turnErr.Error()
	}
	return result
}

func capabilityPromptBuilder(workDir string, worker *thread.Thread) *prompt.Builder {
	guidance := &promptcontext.GuidanceModule{AgentsMDDirs: []string{workDir}}
	runtimeContext := &promptcontext.ThreadContextModule{
		WorkDir: workDir,
		Shell:   capabilityPromptShellProfile(),
		Now:     func() time.Time { return time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC) },
	}
	request := runtimemodule.ContextRequest{
		Purpose: runtimemodule.ContextPurposeProviderIteration,
		Thread:  &runtimemodule.ThreadContext{ID: worker.ID, Dir: worker.Dir, ScratchpadDir: worker.ScratchpadDir()},
	}
	return &prompt.Builder{ModulePromptContext: func() ([]runtimemodule.ContextSection, error) {
		sections, err := guidance.Context(context.Background(), request)
		if err != nil {
			return nil, err
		}
		runtimeSections, err := runtimeContext.Context(context.Background(), request)
		return append(sections, runtimeSections...), err
	}}
}

func collectCapabilityResult(t *testing.T, name, workDir string, worker *thread.Thread, finalText string, elapsed time.Duration, provider *capabilityProvider, contract ContractExpectations) CapabilityResult {
	t.Helper()
	replay := worker.ReplaySnapshot()
	convLines := marshalCapabilityLines(t, replay.Messages)
	eventLines := marshalCapabilityLines(t, replay.Events)
	oracleDir := filepath.Join(workDir, ".eval")
	if err := os.MkdirAll(oracleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	convPath := filepath.Join(oracleDir, "conversation.jsonl")
	eventPath := filepath.Join(oracleDir, "events.jsonl")
	if err := os.WriteFile(convPath, []byte(strings.Join(convLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventPath, []byte(strings.Join(eventLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcriptText := strings.Join(convLines, "\n")
	result := CapabilityResult{
		Name:           name,
		Success:        strings.Contains(finalText, "TASK COMPLETE"),
		ProviderCalls:  provider.called,
		ContextBytes:   len(transcriptText),
		Elapsed:        elapsed,
		ElapsedMS:      elapsed.Milliseconds(),
		Events:         map[string]int{},
		ToolNames:      map[string]int{},
		FinalText:      finalText,
		WorkDir:        workDir,
		ThreadDir:      worker.Dir,
		TranscriptText: transcriptText,
		Snapshots:      append([]ProviderSnapshot(nil), provider.snaps...),
	}
	result.Contract = ValidateContractArtifacts(ContractArtifacts{
		ConversationPath: convPath,
		EventsPath:       eventPath,
	}, contract)
	if !result.Contract.Passed {
		result.Success = false
		result.Error = "contract oracle failed: " + result.Contract.Summary()
	}
	for _, line := range convLines {
		var msg llm.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("conversation line is not Message JSON: %v\n%s", err, line)
		}
		for _, block := range msg.Blocks {
			switch block.Type {
			case llm.BlockToolUse:
				result.ToolCalls++
				result.ToolNames[block.ToolName]++
			case llm.BlockToolResult:
				result.ToolBytes += len(block.Content)
				if block.IsError {
					result.ErrorToolCalls++
				}
			}
		}
	}
	for _, line := range eventLines {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event line is not Event JSON: %v\n%s", err, line)
		}
		result.Events[ev.Type]++
	}
	return result
}

func marshalCapabilityLines[T any](t *testing.T, values []T) []string {
	t.Helper()
	lines := make([]string, 0, len(values))
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	return lines
}

func capabilityPromptShellProfile() promptcontext.ShellProfile {
	p := tools.DefaultShellProfile()
	return promptcontext.ShellProfile{
		Profile:       p.Profile,
		Family:        p.Family,
		Binary:        p.Binary,
		Args:          append([]string(nil), p.Args...),
		PathStyle:     p.PathStyle,
		HostPathStyle: p.HostPathStyle,
	}
}

func writeCapabilityFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertCapabilityFile(t *testing.T, result CapabilityResult, rel, want string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(result.WorkDir, rel))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), want) {
		t.Fatalf("%s = %q, want to contain %q", rel, body, want)
	}
}

func messagesText(messages []llm.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			b.WriteString(block.Text)
			b.WriteString(block.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func readCapabilityLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}
