package migration

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/processenv"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

// Source processes overlay inherited values after every configuration layer.
// Disk declarations identify which keys to preserve, but only the captured
// effective snapshot proves their values. Never copy this operator's OS env.
func convertProcessEnvironment(source legacy.Fleet, config ResolvedConfig, evidence ModelEvidence) (map[string]string, error) {
	if evidence.AgentID != config.AgentID {
		return nil, errors.New("matching source process snapshot is required")
	}
	keys := maps.Clone(config.Environment)
	if keys == nil {
		keys = map[string]string{}
	}
	if config.LoadDotenv {
		var workspace *legacy.Workspace
		for i := range source.Workspaces {
			if source.Workspaces[i].AgentID == config.AgentID {
				if workspace != nil {
					return nil, errors.New("repeated Workspace capture")
				}
				workspace = &source.Workspaces[i]
			}
		}
		if workspace == nil {
			return nil, errors.New("workspace dotenv evidence is required")
		}
		file, err := capturedConfigFile(".env", workspace.Files, workspace.AbsentFiles)
		if err != nil {
			return nil, errors.New("workspace dotenv bytes or proven absence are required")
		}
		if file != nil {
			if verifyConfigFile(*file) != nil {
				return nil, errors.New("invalid captured dotenv bytes")
			}
			values, err := sourceDotenv(file.Data)
			if err != nil {
				return nil, err
			}
			maps.Copy(keys, values)
		}
	}
	result := map[string]string{}
	for key := range keys {
		value, known := evidence.Environment[key]
		if !known || value == nil {
			return nil, fmt.Errorf("configured key %s requires captured effective process value", key)
		}
		result[key] = *value
	}
	if processenv.Validate(result) != nil {
		return nil, errors.New("captured process environment exceeds target policy")
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// Literal parser for the fixed source revision's dotenv grammar. It does not
// expand variables, execute shell syntax or reopen a source path.
func sourceDotenv(data []byte) (map[string]string, error) {
	result := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Text()
		if line == 1 {
			raw = strings.TrimPrefix(raw, "\uFEFF")
		}
		key, value, skip, err := sourceDotenvLine(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid captured dotenv line %d", line)
		}
		if skip {
			continue
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate dotenv key at line %d", line)
		}
		if !validSourceEnvironment(key, value) {
			return nil, fmt.Errorf("invalid dotenv declaration at line %d", line)
		}
		result[key] = value
	}
	if scanner.Err() != nil {
		return nil, errors.New("captured dotenv line exceeds source limit")
	}
	return result, nil
}

func validSourceEnvironment(key, value string) bool {
	if key == "" || strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return false
	}
	for i := range len(key) {
		c := key[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		digit := i > 0 && c >= '0' && c <= '9'
		if c != '_' && !letter && !digit {
			return false
		}
	}
	switch strings.ToUpper(key) {
	case "JUEX_HOME", "HOME", "USERPROFILE", "WORKDIR", "JUEX_WORKDIR", "JUEX_EXT_DIR", "JUEX_EXT_DATA_DIR":
		return false
	}
	return true
}

func sourceDotenvLine(line string) (key, value string, skip bool, err error) {
	invalid := errors.New("invalid dotenv syntax")
	if strings.ContainsRune(line, 0) {
		return "", "", false, invalid
	}
	raw := strings.TrimSpace(line)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return "", "", true, nil
	}
	if strings.HasPrefix(raw, "export ") || strings.HasPrefix(raw, "export\t") {
		raw = strings.TrimSpace(raw[len("export"):])
	}
	key, raw, found := strings.Cut(raw, "=")
	if !found {
		return "", "", false, invalid
	}
	key, raw = strings.TrimSpace(key), strings.TrimSpace(raw)
	if raw == "" {
		return key, "", false, nil
	}
	if raw[0] != '\'' && raw[0] != '"' {
		for i := range len(raw) {
			if raw[i] == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
				raw = raw[:i]
				break
			}
		}
		return key, strings.TrimSpace(raw), false, nil
	}
	quote := raw[0]
	var valueBuilder strings.Builder
	for i := 1; i < len(raw); i++ {
		c := raw[i]
		if c == quote {
			rest := strings.TrimSpace(raw[i+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return "", "", false, invalid
			}
			return key, valueBuilder.String(), false, nil
		}
		if quote == '"' && c == '\\' {
			i++
			if i == len(raw) {
				return "", "", false, invalid
			}
			switch raw[i] {
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case '\\', '"':
				c = raw[i]
			default:
				valueBuilder.WriteByte('\\')
				c = raw[i]
			}
		}
		valueBuilder.WriteByte(c)
	}
	return "", "", false, invalid
}
