package artifact

import "strings"

const readURIPrefix = "artifact://"

// FormatReadURI returns the provider-facing, read-only reference for one
// logical path beneath the current Agent's Artifact root.
func FormatReadURI(relativePath string) (string, error) {
	normalized, err := normalizeRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	return readURIPrefix + normalized, nil
}

// ParseReadURI recognizes a provider-facing Artifact reference. The boolean is
// true whenever value uses the Artifact scheme, including malformed values.
func ParseReadURI(value string) (relativePath string, recognized bool, err error) {
	if !strings.HasPrefix(value, readURIPrefix) {
		return "", false, nil
	}
	normalized, err := normalizeRelativePath(strings.TrimPrefix(value, readURIPrefix))
	if err != nil {
		return "", true, err
	}
	return normalized, true, nil
}
