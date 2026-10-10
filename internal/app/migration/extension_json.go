package migration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
)

var extensionSemVer = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
var errExtensionJSON = errors.New("source extension JSON is invalid or requires separate conversion")

func mcpSourceManifest(data []byte, directoryName string) (extensionpolicy.Manifest, error) {
	var result extensionpolicy.Manifest
	fields, err := extensionObject(data, "manifest_version", "name", "version", "description", "display_name", "author", "homepage", "repository", "license", "requirements", "agent")
	if err != nil {
		return result, err
	}
	var version int
	if json.Unmarshal(fields["manifest_version"], &version) != nil || version != 1 {
		return result, errExtensionJSON
	}
	result.ManifestVersion = 2
	result.Name, err = extensionString(fields["name"])
	if err != nil || result.Name != directoryName {
		return result, errExtensionJSON
	}
	result.Version, err = extensionString(fields["version"])
	if err != nil || !extensionSemVer.MatchString(result.Version) {
		return result, errExtensionJSON
	}
	for _, key := range []string{"description", "display_name", "author", "homepage", "repository", "license"} {
		if raw, ok := fields[key]; ok {
			value, err := extensionString(raw)
			if err != nil {
				return result, err
			}
			if key == "description" {
				result.Description = value
			}
		}
	}
	if raw, ok := fields["requirements"]; ok {
		var requirements []json.RawMessage
		if json.Unmarshal(raw, &requirements) != nil || requirements == nil {
			return result, errExtensionJSON
		}
		for _, raw := range requirements {
			fields, err := extensionObject(raw, "name", "description", "url")
			if err != nil {
				return result, err
			}
			for _, key := range []string{"name", "description", "url"} {
				value, err := extensionString(fields[key])
				if err != nil || strings.TrimSpace(value) == "" {
					return result, errExtensionJSON
				}
			}
		}
	}
	if raw, ok := fields["agent"]; ok {
		agent, err := extensionObject(raw, "environment")
		if err != nil {
			return result, err
		}
		if raw, ok := agent["environment"]; ok {
			environment, err := extensionObject(raw, "variables")
			if err != nil {
				return result, err
			}
			if raw, ok := environment["variables"]; ok {
				variables, err := extensionObject(raw)
				if err != nil || len(variables) != 0 {
					return result, errors.New("agent-wide extension defaults require effective environment evidence")
				}
			}
		}
	}
	return result, nil
}

func extensionString(data []byte) (string, error) {
	var value string
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) || json.Unmarshal(data, &value) != nil || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", errExtensionJSON
	}
	return value, nil
}

// Duplicate keys must not silently choose a different installation/server or
// private environment value. Errors deliberately omit source data and secrets.
func extensionObject(data []byte, allowed ...string) (map[string]json.RawMessage, error) {
	if len(data) > 1<<20 || !utf8.Valid(data) {
		return nil, errExtensionJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if uniqueExtensionJSON(decoder, 0) != nil {
		return nil, errExtensionJSON
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errExtensionJSON
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(data, &result) != nil || result == nil {
		return nil, errExtensionJSON
	}
	if len(allowed) > 0 {
		for key := range result {
			if !slices.Contains(allowed, key) {
				return nil, errExtensionJSON
			}
		}
	}
	return result, nil
}
func uniqueExtensionJSON(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return errExtensionJSON
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return errExtensionJSON
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return errExtensionJSON
			}
			seen[name] = true
		}
		if err := uniqueExtensionJSON(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
