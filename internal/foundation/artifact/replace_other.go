//go:build !windows

package artifact

import "os"

func replaceArtifact(root *os.Root, temp, target string, _ []byte) error {
	return root.Rename(temp, target)
}
