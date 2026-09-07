package tools

import (
	"errors"
)

func ExitCodeFromError(err error) (int, bool) {
	var exitErr interface{ ToolCallExitCode() (int, bool) }
	if errors.As(err, &exitErr) {
		return exitErr.ToolCallExitCode()
	}
	return 0, false
}
