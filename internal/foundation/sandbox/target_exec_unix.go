//go:build darwin || linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// MaybeExecTarget recognizes the private sandbox target protocol before the
// public CLI is constructed. A successful syscall.Exec never returns.
func MaybeExecTarget(args []string) (bool, error) {
	if len(args) < 4 || args[1] != sandboxTargetHelperArgument {
		return false, nil
	}
	if args[2] != "--" || strings.TrimSpace(args[3]) == "" {
		return true, fmt.Errorf("sandbox target helper: invalid arguments")
	}
	deferred, err := decodeSandboxTargetEnvironment(os.Getenv(sandboxTargetEnvironmentKey))
	if err != nil {
		return true, err
	}
	targetEnv := make([]string, 0, len(os.Environ())+len(deferred))
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok && key == sandboxTargetEnvironmentKey {
			continue
		}
		targetEnv = append(targetEnv, item)
	}
	targetEnv = append(targetEnv, deferred...)

	target := args[3]
	if !strings.ContainsRune(target, '/') {
		target, err = exec.LookPath(target)
		if err != nil {
			return true, fmt.Errorf("sandbox target helper: resolve executable: %w", err)
		}
	}
	targetArgs := append([]string{args[3]}, args[4:]...)
	return true, syscall.Exec(target, targetArgs, targetEnv)
}
