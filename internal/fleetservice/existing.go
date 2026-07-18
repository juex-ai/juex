package fleetservice

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func (m *Manager) ExistingServeOptions() (InstalledServeOptions, bool, error) {
	if m == nil {
		return InstalledServeOptions{}, false, fmt.Errorf("fleet service: manager is nil")
	}
	path := m.plan.registration.DefinitionPath
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return InstalledServeOptions{}, false, nil
	}
	if err != nil {
		return InstalledServeOptions{}, false, fmt.Errorf("fleet service: read existing definition %s: %w", path, err)
	}
	args, err := existingDefinitionArgs(m.plan.registration.Platform, data)
	if err != nil {
		return InstalledServeOptions{}, false, fmt.Errorf("fleet service: parse existing definition %s: %w", path, err)
	}
	options, found, err := parseInstalledServeOptions(args)
	if err != nil {
		return InstalledServeOptions{}, false, fmt.Errorf("fleet service: parse existing definition %s: %w", path, err)
	}
	if !found {
		return InstalledServeOptions{}, false, fmt.Errorf(
			"fleet service: existing definition %s does not run juex fleet serve",
			path,
		)
	}
	return options, true, nil
}

func existingDefinitionArgs(platform Platform, data []byte) ([]string, error) {
	switch platform {
	case PlatformLaunchd:
		return launchdProgramArguments(data)
	case PlatformSystemd:
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "ExecStart=") {
				continue
			}
			return normalizeDefinitionFields(strings.Fields(strings.TrimPrefix(line, "ExecStart=")), true), nil
		}
		return nil, fmt.Errorf("systemd unit has no ExecStart")
	case PlatformTermux:
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "exec ") {
				continue
			}
			return normalizeDefinitionFields(strings.Fields(strings.TrimPrefix(line, "exec ")), false), nil
		}
		return nil, fmt.Errorf("termux run script has no exec command")
	default:
		return nil, fmt.Errorf("unsupported platform %q", platform)
	}
}

func normalizeDefinitionFields(fields []string, systemd bool) []string {
	normalized := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, `"'`)
		if systemd {
			field = strings.ReplaceAll(field, "%%", "%")
			field = strings.ReplaceAll(field, "$$", "$")
		}
		normalized = append(normalized, field)
	}
	return normalized
}

func launchdProgramArguments(data []byte) ([]string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	wantArray := false
	inArguments := false
	var args []string
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "key":
				var key string
				if err := decoder.DecodeElement(&key, &typed); err != nil {
					return nil, err
				}
				wantArray = key == "ProgramArguments"
			case "array":
				if wantArray {
					inArguments = true
					wantArray = false
				}
			case "string":
				if !inArguments {
					continue
				}
				var value string
				if err := decoder.DecodeElement(&value, &typed); err != nil {
					return nil, err
				}
				args = append(args, value)
			}
		case xml.EndElement:
			if typed.Name.Local == "array" && inArguments {
				return args, nil
			}
		}
	}
	if inArguments {
		return nil, fmt.Errorf("unterminated ProgramArguments array")
	}
	return nil, fmt.Errorf("plist has no ProgramArguments array")
}

func parseInstalledServeOptions(args []string) (InstalledServeOptions, bool, error) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "fleet" || args[i+1] != "serve" {
			continue
		}
		options := InstalledServeOptions{}
		for index := i + 2; index < len(args); index++ {
			switch args[index] {
			case "--addr":
				if options.Addr != "" {
					return InstalledServeOptions{}, false, fmt.Errorf("duplicate --addr")
				}
				index++
				if index >= len(args) || strings.TrimSpace(args[index]) == "" {
					return InstalledServeOptions{}, false, fmt.Errorf("--addr has no value")
				}
				options.Addr = strings.TrimSpace(args[index])
			case "--unsafe-bind-any":
				options.UnsafeBindAny = true
			default:
				if value, ok := strings.CutPrefix(args[index], "--addr="); ok {
					if options.Addr != "" || strings.TrimSpace(value) == "" {
						return InstalledServeOptions{}, false, fmt.Errorf("invalid duplicate or empty --addr")
					}
					options.Addr = strings.TrimSpace(value)
				}
			}
		}
		return options, true, nil
	}
	return InstalledServeOptions{}, false, nil
}
