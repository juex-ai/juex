//go:build !darwin && !linux

package sandbox

func readHardLinkMetadata(string) (hardLinkMetadata, bool, error) {
	return hardLinkMetadata{}, false, nil
}
