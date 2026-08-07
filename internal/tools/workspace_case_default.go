//go:build !darwin

package tools

import "runtime"

func workspaceCaseInsensitive(string) bool {
	return runtime.GOOS == "windows"
}
