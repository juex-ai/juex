//go:build !windows

package session

func transcriptPrefixDigestRequired(fingerprint transcriptFingerprint) bool {
	return !fingerprint.strong()
}
