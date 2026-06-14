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
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

type CapabilityCase struct {
	Name       string
	Prompt     string
	Files      map[string]string
	Hooks      func(workDir string) hooks.Config
	ExtraTools []tools.Tool
	Script     []CapabilityStep
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
	FinalText      string             `json:"final_text"`
	Error          string             `json:"error,omitempty"`
	WorkDir        string             `json:"-"`
	SessionDir     string             `json:"-"`
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

	sess, err := session.New(filepath.Join(workDir, ".juex", "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	bus := events.NewBus()
	sess.SubscribeBus(bus)

	var hookRunner runtime.HookRunner
	if tc.Hooks != nil {
		runner, err := hooks.NewRunner(tc.Hooks(workDir))
		if err != nil {
			t.Fatalf("create hooks runner: %v", err)
		}
		hookRunner = runner
	}

	provider := &capabilityProvider{workDir: workDir, steps: tc.Script}
	engine := &runtime.Engine{
		Provider: provider,
		Tools:    reg,
		Bus:      bus,
		Session:  sess,
		Prompt: &prompt.Builder{
			AgentsMDDirs: []string{workDir},
			WorkDir:      workDir,
			Shell:        capabilityPromptShellProfile(),
			Now:          func() time.Time { return time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC) },
		},
		Hooks: hookRunner,
		HookContext: hooks.Request{
			CWD:              workDir,
			WorkspaceRoots:   []string{workDir},
			ConversationPath: filepath.Join(sess.Dir, "conversation.jsonl"),
			EventsPath:       filepath.Join(sess.Dir, "events.jsonl"),
		},
	}

	start := time.Now()
	finalText, turnErr := engine.Turn(context.Background(), tc.Prompt)
	elapsed := time.Since(start)
	result := collectCapabilityResult(t, tc.Name, workDir, sess.Dir, finalText, elapsed, provider)
	if turnErr != nil {
		result.Success = false
		result.Error = turnErr.Error()
	}
	return result
}

func collectCapabilityResult(t *testing.T, name, workDir, sessionDir, finalText string, elapsed time.Duration, provider *capabilityProvider) CapabilityResult {
	t.Helper()
	convPath := filepath.Join(sessionDir, "conversation.jsonl")
	eventPath := filepath.Join(sessionDir, "events.jsonl")
	convLines := readCapabilityLines(t, convPath)
	eventLines := readCapabilityLines(t, eventPath)
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
		SessionDir:     sessionDir,
		TranscriptText: transcriptText,
		Snapshots:      append([]ProviderSnapshot(nil), provider.snaps...),
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

func capabilityPromptShellProfile() prompt.ShellProfile {
	p := tools.DefaultShellProfile()
	return prompt.ShellProfile{
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
