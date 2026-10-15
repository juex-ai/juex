package shell

import (
	"fmt"
)

type ShellExitError struct {
	ToolName string
	Code     int
}

func (e *ShellExitError) Error() string {
	tool := e.ToolName
	if tool == "" {
		tool = "shell"
	}
	return fmt.Sprintf("%s: process exited with code %d", tool, e.Code)
}

func shellSessionExitError(toolName string, result ShellSessionResult) error {
	if result.Running || result.ExitCode == nil || *result.ExitCode == 0 {
		return nil
	}
	return &ShellExitError{ToolName: toolName, Code: *result.ExitCode}
}

func (e *ShellExitError) ToolCallExitCode() (int, bool) { return e.Code, true }
