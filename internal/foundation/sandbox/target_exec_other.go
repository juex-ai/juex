//go:build !darwin && !linux

package sandbox

// MaybeExecTarget is inert on platforms without a sandbox backend.
func MaybeExecTarget(args []string) (bool, error) {
	return false, nil
}
