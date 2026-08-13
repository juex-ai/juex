//go:build !windows

package session

func transcriptIncrementalRevisionReliable() bool {
	return true
}
