package environment

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

type LoadDotenvOptions struct {
	GOOS string
}

type DotenvResult struct {
	Path   string
	Loaded bool
	Values map[string]string
}

func LoadDotenv(path string, opts LoadDotenvOptions) (DotenvResult, error) {
	result := DotenvResult{Path: path, Values: map[string]string{}}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return result, fmt.Errorf("environment: read %s: %w", path, err)
	}
	defer file.Close()

	goos := strings.TrimSpace(opts.GOOS)
	if goos == "" {
		goos = runtime.GOOS
	}
	caseInsensitive := goos == "windows"
	seen := map[string]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if lineNumber == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		key, value, skip, err := parseDotenvLine(line)
		if err != nil {
			return DotenvResult{}, dotenvLineError(path, lineNumber, err)
		}
		if skip {
			continue
		}
		if err := validateConfiguredEntry(key, value); err != nil {
			return DotenvResult{}, dotenvLineError(path, lineNumber, err)
		}
		canonical := canonicalKey(key, caseInsensitive)
		if previous, ok := seen[canonical]; ok {
			if caseInsensitive && previous != key {
				return DotenvResult{}, dotenvLineError(
					path,
					lineNumber,
					fmt.Errorf("case-conflicting names %q and %q", previous, key),
				)
			}
			return DotenvResult{}, dotenvLineError(path, lineNumber, fmt.Errorf("duplicate variable %q", key))
		}
		seen[canonical] = key
		result.Values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return DotenvResult{}, fmt.Errorf("environment: read %s: %w", path, err)
	}
	result.Loaded = true
	return result, nil
}

func parseDotenvLine(line string) (key, value string, skip bool, err error) {
	if strings.IndexByte(line, 0) >= 0 {
		return "", "", false, fmt.Errorf("line contains a NUL byte")
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", true, nil
	}
	if strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "export\t") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export"))
	}
	assignment := strings.IndexByte(trimmed, '=')
	if assignment < 0 {
		return "", "", false, fmt.Errorf("expected KEY=VALUE assignment")
	}
	key = strings.TrimSpace(trimmed[:assignment])
	raw := strings.TrimSpace(trimmed[assignment+1:])
	if raw == "" {
		return key, "", false, nil
	}
	switch raw[0] {
	case '\'':
		value, rest, err := parseSingleQuoted(raw)
		if err != nil {
			return "", "", false, err
		}
		if err := validateQuotedRemainder(rest); err != nil {
			return "", "", false, err
		}
		return key, value, false, nil
	case '"':
		value, rest, err := parseDoubleQuoted(raw)
		if err != nil {
			return "", "", false, err
		}
		if err := validateQuotedRemainder(rest); err != nil {
			return "", "", false, err
		}
		return key, value, false, nil
	default:
		return key, trimUnquotedComment(raw), false, nil
	}
}

func parseSingleQuoted(raw string) (string, string, error) {
	end := strings.IndexByte(raw[1:], '\'')
	if end < 0 {
		return "", "", fmt.Errorf("unterminated single-quoted value")
	}
	end++
	return raw[1:end], raw[end+1:], nil
}

func parseDoubleQuoted(raw string) (string, string, error) {
	var quoted strings.Builder
	escaped := false
	for i := 1; i < len(raw); i++ {
		b := raw[i]
		if escaped {
			switch b {
			case 'n':
				quoted.WriteByte('\n')
			case 'r':
				quoted.WriteByte('\r')
			case 't':
				quoted.WriteByte('\t')
			case '\\', '"':
				quoted.WriteByte(b)
			default:
				quoted.WriteByte('\\')
				quoted.WriteByte(b)
			}
			escaped = false
			continue
		}
		switch b {
		case '\\':
			escaped = true
		case '"':
			return quoted.String(), raw[i+1:], nil
		default:
			quoted.WriteByte(b)
		}
	}
	return "", "", fmt.Errorf("unterminated double-quoted value")
}

func validateQuotedRemainder(rest string) error {
	trimmed := strings.TrimSpace(rest)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return nil
	}
	return fmt.Errorf("unexpected content after quoted value")
}

func trimUnquotedComment(value string) string {
	for i := 0; i < len(value); i++ {
		if value[i] != '#' {
			continue
		}
		if i == 0 || value[i-1] == ' ' || value[i-1] == '\t' {
			return strings.TrimSpace(value[:i])
		}
	}
	return strings.TrimSpace(value)
}

func dotenvLineError(path string, line int, err error) error {
	return fmt.Errorf("environment: parse %s line %d: %w", path, line, err)
}
