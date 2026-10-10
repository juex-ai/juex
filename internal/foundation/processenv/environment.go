// Package processenv validates private process defaults independently of any
// service's persistence. Values belong on the dispatch channel, never Request.
package processenv

import (
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid process environment")
var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func ValidName(name string) bool {
	// The executor owns the user's home and workspace identity. Private defaults
	// may choose tools through PATH, but must not redirect managed state roots.
	return namePattern.MatchString(name) && !strings.HasPrefix(name, "JUEX_") && name != "HOME" && name != "USERPROFILE" && name != "WORKDIR"
}
func Validate(values map[string]string) error {
	if len(values) > 64 {
		return ErrInvalid
	}
	for name, value := range values {
		if !ValidName(name) || !utf8.ValidString(value) || len(value) > 4096 || strings.ContainsRune(value, 0) {
			return ErrInvalid
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil || len(encoded) > 64<<10 {
		return ErrInvalid
	}
	return nil
}
func Merge(layers ...map[string]string) map[string]string {
	result := map[string]string{}
	for _, layer := range layers {
		maps.Copy(result, layer)
	}
	return result
}

// Uses reports operations which create an authorized user process. File
// helpers, inspection and reads must not receive unrelated process credentials.
func Uses(kind string) bool {
	switch kind {
	case "exec_command", "run_hook", "mcp_connect", "observe_command":
		return true
	}
	return false
}
