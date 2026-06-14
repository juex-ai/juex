package hooks

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCommandHookMatchesEventAndTool(t *testing.T) {
	h := CommandHook{Name: "guard", Events: []EventName{EventPreToolUse}, Tools: []string{"exec_command"}}
	if !h.Matches(EventPreToolUse, "exec_command") {
		t.Fatal("hook should match configured event and tool")
	}
	if h.Matches(EventPostToolUse, "exec_command") {
		t.Fatal("hook should not match a different event")
	}
	if h.Matches(EventPreToolUse, "read") {
		t.Fatal("hook should not match a different tool")
	}
	withoutToolFilter := CommandHook{Name: "any", Events: []EventName{EventUserPromptSubmit}}
	if !withoutToolFilter.Matches(EventUserPromptSubmit, "") {
		t.Fatal("hook without tool filter should match event")
	}
}

func TestRunnerRunStableOrderAndAdditionalContext(t *testing.T) {
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "first", Events: []EventName{EventUserPromptSubmit}, Command: helperCommand("context:first")},
		{Name: "second", Events: []EventName{EventUserPromptSubmit}, Command: helperCommand("context:second")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventUserPromptSubmit})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d", len(results))
	}
	if results[0].Hook.Name != "first" || results[0].Output.AdditionalContext != "first" {
		t.Fatalf("first result = %+v", results[0])
	}
	if results[0].EventName != EventUserPromptSubmit {
		t.Fatalf("first event = %q", results[0].EventName)
	}
	if results[1].Hook.Name != "second" || results[1].Output.AdditionalContext != "second" {
		t.Fatalf("second result = %+v", results[1])
	}
}

func TestRunnerRunResultCarriesTriggeredTool(t *testing.T) {
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "multi", Events: []EventName{EventPreToolUse, EventPostToolUse}, Tools: []string{"exec_command"}, Command: helperCommand("allow")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventPostToolUse, ToolName: "exec_command"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results len = %d", len(results))
	}
	if results[0].EventName != EventPostToolUse || results[0].ToolName != "exec_command" {
		t.Fatalf("result trigger = %q/%q", results[0].EventName, results[0].ToolName)
	}
}

func TestRunnerRunTimeout(t *testing.T) {
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "slow", Events: []EventName{EventPreToolUse}, Command: helperCommand("sleep"), TimeoutSeconds: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.Run(context.Background(), Request{EventName: EventPreToolUse, ToolName: "exec_command"})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestRunnerRunOutputLimit(t *testing.T) {
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "large", Events: []EventName{EventPostToolUse}, Command: helperCommand("large"), MaxOutputBytes: 8},
	}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.Run(context.Background(), Request{EventName: EventPostToolUse, ToolName: "read"})
	if err == nil || !strings.Contains(err.Error(), "stdout exceeded") {
		t.Fatalf("err = %v, want output limit", err)
	}
}

func TestParseOutputValidatesDecisions(t *testing.T) {
	out, err := ParseOutput([]byte(`{"decision":"deny","additional_context":"ctx","block_stop":true,"continue_prompt":"more"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != DecisionDeny || out.AdditionalContext != "ctx" || !out.BlockStop || out.ContinuePrompt != "more" {
		t.Fatalf("out = %+v", out)
	}
	if _, err := ParseOutput([]byte(`{"decision":"maybe"}`)); err == nil {
		t.Fatal("expected invalid decision error")
	}
}

func helperCommand(mode string) []string {
	return []string{os.Args[0], "-test.run=TestHookHelperProcess", "--", mode}
}

func TestHookHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch {
	case strings.HasPrefix(mode, "context:"):
		value := strings.TrimPrefix(mode, "context:")
		_, _ = os.Stdout.WriteString(`{"additional_context":"` + value + `"}`)
	case mode == "sleep":
		time.Sleep(5 * time.Second)
	case mode == "large":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 32))
	default:
		_, _ = os.Stdout.WriteString(`{"decision":"allow"}`)
	}
	os.Exit(0)
}
