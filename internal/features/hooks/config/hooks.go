// Package config defines and validates trusted command hook declarations.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

const (
	DefaultTimeoutSeconds = 10
	MaxTimeoutSeconds     = 300
	DefaultMaxOutputBytes = 64 * 1024
)

type EventName string

const (
	EventThreadStart      EventName = "ThreadStart"
	EventUserPromptSubmit EventName = "UserPromptSubmit"
	EventPreToolUse       EventName = "PreToolUse"
	EventPostToolUse      EventName = "PostToolUse"
	EventPreCompact       EventName = "PreCompact"
	EventPostCompact      EventName = "PostCompact"
	EventStop             EventName = "Stop"
)

type Config struct {
	Commands []CommandHook `json:"commands" yaml:"commands"`
}

type FileConfig struct {
	Trusted  bool          `yaml:"trusted"`
	Commands []CommandHook `yaml:"commands"`
}

type CommandHook struct {
	Name           string      `json:"name" yaml:"name"`
	Events         []EventName `json:"events" yaml:"events"`
	Tools          []string    `json:"tools,omitempty" yaml:"tools"`
	Command        []string    `json:"command" yaml:"command"`
	TimeoutSeconds int         `json:"timeout_seconds,omitempty" yaml:"timeout_seconds"`
	MaxOutputBytes int         `json:"max_output_bytes,omitempty" yaml:"max_output_bytes"`
	Required       bool        `json:"required,omitempty" yaml:"required"`
	Source         string      `json:"source,omitempty" yaml:"-"`
}

func (h CommandHook) Matches(event EventName, toolName string) bool {
	if len(h.Events) == 0 {
		return false
	}
	matchesEvent := false
	for _, candidate := range h.Events {
		if candidate == event {
			matchesEvent = true
			break
		}
	}
	if !matchesEvent {
		return false
	}
	if len(h.Tools) == 0 {
		return true
	}
	for _, tool := range h.Tools {
		if tool == toolName {
			return true
		}
	}
	return false
}

func ResolveFileConfig(fc FileConfig, source string, requireTrust bool) (Config, error) {
	if len(fc.Commands) == 0 {
		return Config{}, nil
	}
	if requireTrust && !fc.Trusted {
		return Config{}, fmt.Errorf("hooks: file command hooks require hooks.trusted: true")
	}
	cfg := Config{Commands: append([]CommandHook(nil), fc.Commands...)}
	for i := range cfg.Commands {
		cfg.Commands[i].Source = source
		if err := ValidateHook(cfg.Commands[i]); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func LoadFileConfig(path, source string, requireTrust bool) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var fc FileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("hooks: parse %s: %w", path, err)
	}
	return ResolveFileConfig(fc, source, requireTrust)
}

func ValidateHook(h CommandHook) error {
	if strings.TrimSpace(h.Name) == "" {
		return fmt.Errorf("hooks: command hook name is required")
	}
	if len(h.Events) == 0 {
		return fmt.Errorf("hooks: %s: at least one event is required", h.Name)
	}
	for _, event := range h.Events {
		if !validEvent(event) {
			return fmt.Errorf("hooks: %s: invalid event %q", h.Name, event)
		}
	}
	if len(h.Command) == 0 || strings.TrimSpace(h.Command[0]) == "" {
		return fmt.Errorf("hooks: %s: command is required", h.Name)
	}
	if h.TimeoutSeconds < 0 {
		return fmt.Errorf("hooks: %s: timeout_seconds must be >= 0", h.Name)
	}
	if h.TimeoutSeconds > MaxTimeoutSeconds {
		return fmt.Errorf("hooks: %s: timeout_seconds cannot exceed %d seconds", h.Name, MaxTimeoutSeconds)
	}
	if h.MaxOutputBytes < 0 {
		return fmt.Errorf("hooks: %s: max_output_bytes must be >= 0", h.Name)
	}
	return nil
}

func validEvent(event EventName) bool {
	switch event {
	case EventThreadStart, EventUserPromptSubmit, EventPreToolUse, EventPostToolUse, EventPreCompact, EventPostCompact, EventStop:
		return true
	default:
		return false
	}
}
