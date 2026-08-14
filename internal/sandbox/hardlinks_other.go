//go:build !darwin && !linux

package sandbox

func hasMultipleHardLinks(string) (bool, error) {
	return false, nil
}
