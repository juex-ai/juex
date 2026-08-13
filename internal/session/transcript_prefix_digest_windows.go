//go:build windows

package session

func transcriptCheckpointContentDigestRequired(transcriptFingerprint) bool {
	return true
}
