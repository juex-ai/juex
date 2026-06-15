package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
)

type ContractArtifacts struct {
	ConversationPath string
	EventsPath       string
}

type ContractExpectations struct {
	RequiredTools         []string
	AllowLegacyShellTools bool
	RequireTTYExec        bool
	ExecResultToken       string
	Events                EventContractExpectations
}

type EventContractExpectations struct {
	MinOutputDeltas                   int
	RequireInstallProgress            bool
	RequireInteractivePrompt          bool
	DoneToken                         string
	RequireCarriageReturn             bool
	RequireWriteStdinCompleted        bool
	RequireStructuredExecRunning      bool
	RequireStructuredExecCompleted    bool
	RequireStructuredWriteStdinResult bool
}

type ContractReport struct {
	Passed bool            `json:"passed"`
	Issues []ContractIssue `json:"issues,omitempty"`
}

type ContractIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (r ContractReport) Summary() string {
	if len(r.Issues) == 0 {
		return "contract passed"
	}
	parts := make([]string, 0, len(r.Issues))
	for _, issue := range r.Issues {
		parts = append(parts, issue.Message)
	}
	return strings.Join(parts, "; ")
}

func ValidateContractArtifacts(artifacts ContractArtifacts, expect ContractExpectations) ContractReport {
	var issues []ContractIssue
	conv := inspectConversationArtifact(artifacts.ConversationPath, expect)
	issues = append(issues, conv.issues...)
	eventSummary := inspectEventArtifact(artifacts.EventsPath, expect.Events.DoneToken)
	issues = append(issues, eventSummary.issues...)

	if len(expect.RequiredTools) > 0 {
		for _, tool := range expect.RequiredTools {
			if conv.toolNames[tool] == 0 {
				issues = append(issues, ContractIssue{Code: "conversation.tool.missing", Message: "missing required tool_use block: " + tool})
			}
		}
	}
	if !expect.AllowLegacyShellTools && len(conv.legacyShellUses) > 0 {
		issues = append(issues, ContractIssue{
			Code:    "conversation.shell.legacy",
			Message: "conversation contains legacy shell tool_use: " + strings.Join(conv.legacyShellUses, ", "),
		})
	}
	if expect.RequireTTYExec && !conv.sawTTYExec {
		issues = append(issues, ContractIssue{Code: "conversation.exec.tty", Message: "missing exec_command tool_use with tty:true"})
	}
	if expect.ExecResultToken != "" && !conv.sawExecResult {
		issues = append(issues, ContractIssue{
			Code:    "conversation.exec.result",
			Message: fmt.Sprintf("missing exec_command tool_result containing %s and successful exit status", expect.ExecResultToken),
		})
	}

	issues = append(issues, validateEventExpectations(eventSummary, expect.Events)...)
	return ContractReport{Passed: len(issues) == 0, Issues: issues}
}

type conversationContractSummary struct {
	issues          []ContractIssue
	toolNames       map[string]int
	toolUseNames    map[string]string
	legacyShellUses []string
	sawTTYExec      bool
	sawExecResult   bool
}

func inspectConversationArtifact(path string, expect ContractExpectations) conversationContractSummary {
	summary := conversationContractSummary{
		toolNames:    map[string]int{},
		toolUseNames: map[string]string{},
	}
	if path == "" {
		summary.issues = append(summary.issues, ContractIssue{Code: "conversation.path.missing", Message: "missing conversation path"})
		return summary
	}
	lines, err := readContractLines(path)
	if err != nil {
		summary.issues = append(summary.issues, ContractIssue{Code: "conversation.read", Message: "read conversation: " + err.Error()})
		return summary
	}
	for lineNumber, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			summary.issues = append(summary.issues, ContractIssue{
				Code:    "conversation.json",
				Message: fmt.Sprintf("conversation line %d is invalid JSON: %v", lineNumber+1, err),
			})
			continue
		}
		for _, block := range msg.Blocks {
			switch block.Type {
			case llm.BlockToolUse:
				toolName := block.ToolName
				summary.toolNames[toolName]++
				if block.ToolUseID != "" {
					summary.toolUseNames[block.ToolUseID] = toolName
				}
				if toolName == "exec_command" && block.Input["tty"] == true {
					summary.sawTTYExec = true
				}
				if toolName == "shell" || toolName == "shell_input" {
					summary.legacyShellUses = append(summary.legacyShellUses, fmt.Sprintf("%d:%s", lineNumber+1, toolName))
				}
			case llm.BlockToolResult:
				if expect.ExecResultToken == "" {
					continue
				}
				if summary.toolUseNames[block.ToolUseID] == "exec_command" &&
					strings.Contains(block.Content, expect.ExecResultToken) &&
					strings.Contains(block.Content, "Process exited with code 0") {
					summary.sawExecResult = true
				}
			}
		}
	}
	return summary
}

type eventContractSummary struct {
	issues                        []ContractIssue
	outputDeltas                  int
	sawInstallProgress            bool
	sawInteractivePrompt          bool
	sawDone                       bool
	sawCarriageReturn             bool
	sawWriteStdinCompleted        bool
	sawStructuredExecRunning      bool
	sawStructuredExecCompleted    bool
	sawStructuredWriteStdinResult bool
}

func inspectEventArtifact(path string, doneToken string) eventContractSummary {
	var summary eventContractSummary
	if path == "" {
		summary.issues = append(summary.issues, ContractIssue{Code: "events.path.missing", Message: "missing events path"})
		return summary
	}
	lines, err := readContractLines(path)
	if err != nil {
		summary.issues = append(summary.issues, ContractIssue{Code: "events.read", Message: "read events: " + err.Error()})
		return summary
	}
	for lineNumber, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			summary.issues = append(summary.issues, ContractIssue{
				Code:    "events.json",
				Message: fmt.Sprintf("events line %d is invalid JSON: %v", lineNumber+1, err),
			})
			continue
		}
		payload, _ := event["payload"].(map[string]any)
		if payload == nil {
			continue
		}
		switch event["type"] {
		case "tool.output_delta":
			text, _ := payload["text"].(string)
			summary.outputDeltas++
			summary.sawInstallProgress = summary.sawInstallProgress || strings.Contains(text, "INSTALL")
			summary.sawInteractivePrompt = summary.sawInteractivePrompt || strings.Contains(text, "PROMPT approve install")
			if doneToken != "" {
				summary.sawDone = summary.sawDone || strings.Contains(text, "TTY-DONE "+doneToken)
			} else {
				summary.sawDone = summary.sawDone || strings.Contains(text, "TTY-DONE")
			}
			summary.sawCarriageReturn = summary.sawCarriageReturn || strings.Contains(text, "\r")
		case "tool.completed":
			name, _ := payload["name"].(string)
			result, _ := payload["result"].(map[string]any)
			if name == "exec_command" && result != nil {
				sessionID := numberValue(result["session_id"])
				if result["running"] == true && sessionID > 0 {
					summary.sawStructuredExecRunning = true
				}
				if result["running"] == false && numberValue(result["exit_code"]) == 0 {
					summary.sawStructuredExecCompleted = true
				}
			}
			if name == "write_stdin" {
				summary.sawWriteStdinCompleted = true
				if result != nil &&
					result["running"] == false &&
					numberValue(result["exit_code"]) == 0 &&
					outputHasDoneToken(fmt.Sprint(result["output"]), doneToken) {
					summary.sawStructuredWriteStdinResult = true
				}
			}
		}
	}
	return summary
}

func outputHasDoneToken(output string, doneToken string) bool {
	if doneToken == "" {
		return strings.Contains(output, "TTY-DONE")
	}
	return strings.Contains(output, "TTY-DONE "+doneToken)
}

func validateEventExpectations(summary eventContractSummary, expect EventContractExpectations) []ContractIssue {
	var issues []ContractIssue
	if expect.MinOutputDeltas > 0 && summary.outputDeltas < expect.MinOutputDeltas {
		issues = append(issues, ContractIssue{
			Code:    "events.delta.count",
			Message: fmt.Sprintf("expected at least %d tool.output_delta events, saw %d", expect.MinOutputDeltas, summary.outputDeltas),
		})
	}
	if expect.RequireInstallProgress && !summary.sawInstallProgress {
		issues = append(issues, ContractIssue{Code: "events.delta.install", Message: "events missing INSTALL progress"})
	}
	if expect.RequireInteractivePrompt && !summary.sawInteractivePrompt {
		issues = append(issues, ContractIssue{Code: "events.delta.prompt", Message: "events missing interactive prompt"})
	}
	if expect.DoneToken != "" && !summary.sawDone {
		issues = append(issues, ContractIssue{Code: "events.delta.done", Message: "events missing TTY-DONE token"})
	}
	if expect.RequireCarriageReturn && !summary.sawCarriageReturn {
		issues = append(issues, ContractIssue{Code: "events.delta.carriage_return", Message: "events missing carriage-return progress update"})
	}
	if expect.RequireWriteStdinCompleted && !summary.sawWriteStdinCompleted {
		issues = append(issues, ContractIssue{Code: "events.write_stdin.completed", Message: "events missing write_stdin completion"})
	}
	if expect.RequireStructuredExecRunning && !summary.sawStructuredExecRunning {
		issues = append(issues, ContractIssue{Code: "events.exec.structured_running", Message: "events missing structured exec_command running result"})
	}
	if expect.RequireStructuredExecCompleted && !summary.sawStructuredExecCompleted {
		issues = append(issues, ContractIssue{Code: "events.exec.structured_completed", Message: "events missing structured exec_command completed result"})
	}
	if expect.RequireStructuredWriteStdinResult && !summary.sawStructuredWriteStdinResult {
		issues = append(issues, ContractIssue{Code: "events.write_stdin.structured", Message: "events missing structured write_stdin result"})
	}
	return issues
}

func readContractLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func numberValue(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	case json.Number:
		out, _ := n.Float64()
		return out
	default:
		return -1
	}
}
