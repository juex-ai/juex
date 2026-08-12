//go:build !darwin && !linux && !windows

package session

import "os"

func transcriptFileChangeID(_ *os.File, _ os.FileInfo) string {
	return ""
}
