//go:build windows

package session

func transcriptPrefixDigestRequired(transcriptFingerprint) bool {
	return true
}
