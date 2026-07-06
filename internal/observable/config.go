package observable

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	DefaultKind     = "log_batch"
	DefaultSeverity = "info"

	StreamStdout = "stdout"
	StreamStderr = "stderr"

	ParserText  = "text"
	ParserJSONL = "jsonl"

	MinBatchIntervalSeconds = 5
	MaxBatchIntervalSeconds = 86400
	MaxBatchChars           = 1000
)

type FileConfig struct {
	Observables []Spec `json:"observables"`
}

type ConfigIssue struct {
	ID    string
	Spec  Spec
	Error error
}

type Spec struct {
	ID       string            `json:"id"`
	Name     string            `json:"name,omitempty"`
	Command  string            `json:"command"`
	Args     []string          `json:"args,omitempty"`
	CWD      string            `json:"cwd,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Streams  []string          `json:"streams,omitempty"`
	Defaults Defaults          `json:"defaults,omitempty"`
	Parser   *ParserSpec       `json:"parser,omitempty"`
	Filters  []FilterSpec      `json:"filters,omitempty"`
	Batch    BatchSpec         `json:"batch"`
	OnExit   OnExitSpec        `json:"on_exit,omitempty"`
}

type Defaults struct {
	Kind     string `json:"kind,omitempty"`
	Severity string `json:"severity,omitempty"`
}

type ParserSpec struct {
	Type          string `json:"type"`
	ContentField  string `json:"content_field,omitempty"`
	KindField     string `json:"kind_field,omitempty"`
	SeverityField string `json:"severity_field,omitempty"`
	TimeField     string `json:"time_field,omitempty"`
}

type FilterSpec struct {
	Contains string `json:"contains,omitempty"`
	Regex    string `json:"regex,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Severity string `json:"severity,omitempty"`
}

type BatchSpec struct {
	IntervalSeconds int `json:"interval_seconds"`
	MaxChars        int `json:"max_chars"`
}

type OnExitSpec struct {
	Notify string `json:"notify,omitempty"`
}

func LoadConfig(path string) (FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileConfig{}, nil
		}
		return FileConfig{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return FileConfig{}, nil
	}
	var cfg FileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return FileConfig{}, fmt.Errorf("observable config: parse %s: %w", path, err)
	}
	return ValidateConfig(cfg)
}

func LoadConfigLenient(path string) (FileConfig, []ConfigIssue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileConfig{}, nil, nil
		}
		return FileConfig{}, nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return FileConfig{}, nil, nil
	}
	var raw FileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return FileConfig{}, []ConfigIssue{{
			ID:    "config",
			Error: fmt.Errorf("observable config: parse %s: %w", path, err),
		}}, nil
	}
	seen := map[string]struct{}{}
	out := FileConfig{Observables: make([]Spec, 0, len(raw.Observables))}
	var issues []ConfigIssue
	for i, spec := range raw.Observables {
		normalized, err := ValidateSpec(spec)
		if err != nil {
			issues = append(issues, ConfigIssue{
				ID:    issueID(spec, i),
				Spec:  spec,
				Error: fmt.Errorf("observable config: observables[%d] %q: %w", i, spec.ID, err),
			})
			continue
		}
		if _, ok := seen[normalized.ID]; ok {
			issues = append(issues, ConfigIssue{
				ID:    fmt.Sprintf("%s#%d", normalized.ID, i),
				Spec:  normalized,
				Error: fmt.Errorf("observable config: duplicate id %q", normalized.ID),
			})
			continue
		}
		seen[normalized.ID] = struct{}{}
		out.Observables = append(out.Observables, normalized)
	}
	return out, issues, nil
}

func SaveConfig(path string, cfg FileConfig) error {
	cfg, err := ValidateConfig(cfg)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".observables-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func ValidateConfig(cfg FileConfig) (FileConfig, error) {
	seen := map[string]struct{}{}
	out := FileConfig{Observables: make([]Spec, 0, len(cfg.Observables))}
	for i, spec := range cfg.Observables {
		normalized, err := ValidateSpec(spec)
		if err != nil {
			return FileConfig{}, fmt.Errorf("observable config: observables[%d] %q: %w", i, spec.ID, err)
		}
		if _, ok := seen[normalized.ID]; ok {
			return FileConfig{}, fmt.Errorf("observable config: duplicate id %q", normalized.ID)
		}
		seen[normalized.ID] = struct{}{}
		out.Observables = append(out.Observables, normalized)
	}
	return out, nil
}

func ValidateSpec(spec Spec) (Spec, error) {
	spec.ID = strings.TrimSpace(spec.ID)
	spec.Command = strings.TrimSpace(spec.Command)
	if !validID(spec.ID) {
		return Spec{}, fmt.Errorf("id must use lower-case letters, digits, '_' or '-'")
	}
	if spec.Command == "" {
		return Spec{}, fmt.Errorf("command is required")
	}
	if len(spec.Streams) == 0 {
		spec.Streams = []string{StreamStdout, StreamStderr}
	}
	for _, stream := range spec.Streams {
		switch stream {
		case StreamStdout, StreamStderr:
		default:
			return Spec{}, fmt.Errorf("stream must be stdout or stderr, got %q", stream)
		}
	}
	if err := validateSeverity("defaults.severity", spec.Defaults.Severity); err != nil {
		return Spec{}, err
	}
	if spec.Parser != nil {
		parserType := spec.Parser.Type
		if parserType == "" {
			parserType = ParserText
			spec.Parser.Type = parserType
		}
		switch parserType {
		case ParserText, ParserJSONL:
		default:
			return Spec{}, fmt.Errorf("parser.type must be text or jsonl, got %q", parserType)
		}
	}
	if spec.Batch.IntervalSeconds < MinBatchIntervalSeconds || spec.Batch.IntervalSeconds > MaxBatchIntervalSeconds {
		return Spec{}, fmt.Errorf("batch.interval_seconds must be between %d and %d", MinBatchIntervalSeconds, MaxBatchIntervalSeconds)
	}
	if spec.Batch.MaxChars < 1 || spec.Batch.MaxChars > MaxBatchChars {
		return Spec{}, fmt.Errorf("batch.max_chars must be between 1 and %d", MaxBatchChars)
	}
	for i, filter := range spec.Filters {
		hasContains := strings.TrimSpace(filter.Contains) != ""
		hasRegex := strings.TrimSpace(filter.Regex) != ""
		if hasContains == hasRegex {
			return Spec{}, fmt.Errorf("filter %d must set exactly one of contains or regex", i)
		}
		if err := validateSeverity(fmt.Sprintf("filters[%d].severity", i), filter.Severity); err != nil {
			return Spec{}, err
		}
		if hasRegex {
			if _, err := regexp.Compile(filter.Regex); err != nil {
				return Spec{}, fmt.Errorf("filter %d regex: %w", i, err)
			}
		}
	}
	switch spec.OnExit.Notify {
	case "", "never", "always", "nonzero":
	default:
		return Spec{}, fmt.Errorf("on_exit.notify must be never, always, or nonzero, got %q", spec.OnExit.Notify)
	}
	return spec, nil
}

func ExpandVariables(value, workDir string) string {
	replacer := strings.NewReplacer(
		"${WORKDIR}", workDir,
		"$WORKDIR", workDir,
		"${JUEX_WORKDIR}", workDir,
		"$JUEX_WORKDIR", workDir,
	)
	return replacer.Replace(value)
}

func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func issueID(spec Spec, index int) string {
	if validID(strings.TrimSpace(spec.ID)) {
		return strings.TrimSpace(spec.ID)
	}
	return fmt.Sprintf("config-%d", index)
}

func validateSeverity(field, severity string) error {
	switch severity {
	case "", "info", "warning", "error", "critical":
		return nil
	default:
		return fmt.Errorf("%s must be info, warning, error, or critical, got %q", field, severity)
	}
}

func resolvedKind(kind string) string {
	if kind == "" {
		return DefaultKind
	}
	return kind
}

func resolvedSeverity(severity string) string {
	if severity == "" {
		return DefaultSeverity
	}
	return severity
}
