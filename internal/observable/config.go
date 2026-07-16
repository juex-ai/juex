package observable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/eventmedia"
)

const (
	DefaultKind     = "log_batch"
	DefaultSeverity = "info"

	StreamStdout = "stdout"
	StreamStderr = "stderr"

	ParserText  = "text"
	ParserJSONL = "jsonl"

	MinBatchIntervalSeconds     = 5
	MaxBatchIntervalSeconds     = 86400
	MaxBatchChars               = 1000
	DefaultBatchIntervalSeconds = MinBatchIntervalSeconds
	DefaultBatchMaxChars        = MaxBatchChars

	SourceTypeCommand  = "command"
	SourceTypeSchedule = "schedule"

	ScheduleCatchUpNone   = "none"
	ScheduleCatchUpLatest = "latest"

	DefaultScheduleKind       = "heartbeat"
	MaxScheduleContentChars   = 1000
	MinIntervalScheduleSecond = 60
)

type FileConfig struct {
	Observables []Spec `json:"observables"`
}

type ConfigIssue struct {
	ID    string
	Spec  Spec
	Error error
}

type sourceConfig interface {
	sourceType() string
}

type Spec struct {
	ID     string
	Name   string
	source sourceConfig
}

type CommandSourceSpec struct {
	Command     string                 `json:"command"`
	Args        []string               `json:"args,omitempty"`
	CWD         string                 `json:"cwd,omitempty"`
	Env         map[string]string      `json:"env,omitempty"`
	Streams     []string               `json:"streams,omitempty"`
	Parser      *ParserSpec            `json:"parser,omitempty"`
	Filters     []FilterSpec           `json:"filters,omitempty"`
	Batch       BatchSpec              `json:"batch,omitempty"`
	OnExit      OnExitSpec             `json:"on_exit,omitempty"`
	Observation CommandObservationSpec `json:"observation,omitempty"`
}

func (CommandSourceSpec) sourceType() string { return SourceTypeCommand }

type ScheduleSourceSpec struct {
	Timezone    string                  `json:"timezone,omitempty"`
	Once        *OnceSchedule           `json:"once,omitempty"`
	Daily       *DailySchedule          `json:"daily,omitempty"`
	Interval    *IntervalSchedule       `json:"interval,omitempty"`
	CatchUp     CatchUpSpec             `json:"catch_up,omitempty"`
	Observation ScheduleObservationSpec `json:"observation"`
}

func (ScheduleSourceSpec) sourceType() string { return SourceTypeSchedule }

type CommandObservationSpec struct {
	Kind     string `json:"kind,omitempty"`
	Severity string `json:"severity,omitempty"`
}

type ScheduleObservationSpec struct {
	Kind        string           `json:"kind,omitempty"`
	Severity    string           `json:"severity,omitempty"`
	Content     string           `json:"content"`
	Attachments []AttachmentSpec `json:"attachments,omitempty"`
}

type OnceSchedule struct {
	At string `json:"at"`
}

type DailySchedule struct {
	Times    []string `json:"times"`
	Weekdays []string `json:"weekdays,omitempty"`
}

type IntervalSchedule struct {
	EverySeconds int `json:"every_seconds"`
}

type CatchUpSpec struct {
	Mode               string `json:"mode,omitempty"`
	MaxLatenessMinutes int    `json:"max_lateness_minutes,omitempty"`
}

type AttachmentSpec = eventmedia.AttachmentRef

type Defaults struct {
	Kind     string `json:"kind,omitempty"`
	Severity string `json:"severity,omitempty"`
}

type ParserSpec struct {
	Type             string `json:"type"`
	ContentField     string `json:"content_field,omitempty"`
	KindField        string `json:"kind_field,omitempty"`
	SeverityField    string `json:"severity_field,omitempty"`
	TimeField        string `json:"time_field,omitempty"`
	AttachmentsField string `json:"attachments_field,omitempty"`
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

type commandRuntimeSpec struct {
	ID string
	CommandSourceSpec
	Defaults Defaults
}

type scheduleRuntimeSpec struct {
	ID string
	ScheduleSourceSpec
}

func NewCommandSpec(id, name string, config CommandSourceSpec) (Spec, error) {
	return ValidateSpec(Spec{ID: id, Name: name, source: cloneCommandSourceSpec(config)})
}

func NewScheduleSpec(id, name string, config ScheduleSourceSpec) (Spec, error) {
	return ValidateSpec(Spec{ID: id, Name: name, source: cloneScheduleSourceSpec(config)})
}

func (spec Spec) SourceType() string {
	if spec.source == nil {
		return ""
	}
	return spec.source.sourceType()
}

func (spec Spec) CommandConfig() (CommandSourceSpec, bool) {
	config, ok := spec.source.(CommandSourceSpec)
	if !ok {
		return CommandSourceSpec{}, false
	}
	return cloneCommandSourceSpec(config), true
}

func (spec Spec) ScheduleConfig() (ScheduleSourceSpec, bool) {
	config, ok := spec.source.(ScheduleSourceSpec)
	if !ok {
		return ScheduleSourceSpec{}, false
	}
	return cloneScheduleSourceSpec(config), true
}

func (spec Spec) commandRuntime() (commandRuntimeSpec, bool) {
	config, ok := spec.CommandConfig()
	if !ok {
		return commandRuntimeSpec{}, false
	}
	return commandRuntimeSpec{
		ID:                spec.ID,
		CommandSourceSpec: config,
		Defaults: Defaults{
			Kind:     config.Observation.Kind,
			Severity: config.Observation.Severity,
		},
	}, true
}

func (spec Spec) scheduleRuntime() (scheduleRuntimeSpec, bool) {
	config, ok := spec.ScheduleConfig()
	if !ok {
		return scheduleRuntimeSpec{}, false
	}
	return scheduleRuntimeSpec{ID: spec.ID, ScheduleSourceSpec: config}, true
}

type wireSpec struct {
	ID             string              `json:"id"`
	Name           string              `json:"name,omitempty"`
	Type           string              `json:"type"`
	CommandConfig  *CommandSourceSpec  `json:"command_config,omitempty"`
	ScheduleConfig *ScheduleSourceSpec `json:"schedule_config,omitempty"`
}

type wireCommandSourceSpec struct {
	Command     string                  `json:"command"`
	Args        []string                `json:"args,omitempty"`
	CWD         string                  `json:"cwd,omitempty"`
	Env         map[string]string       `json:"env,omitempty"`
	Streams     []string                `json:"streams,omitempty"`
	Parser      *ParserSpec             `json:"parser,omitempty"`
	Filters     []FilterSpec            `json:"filters,omitempty"`
	Batch       *BatchSpec              `json:"batch,omitempty"`
	OnExit      *OnExitSpec             `json:"on_exit,omitempty"`
	Observation *CommandObservationSpec `json:"observation,omitempty"`
}

type marshalWireSpec struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name,omitempty"`
	Type           string                 `json:"type"`
	CommandConfig  *wireCommandSourceSpec `json:"command_config,omitempty"`
	ScheduleConfig *ScheduleSourceSpec    `json:"schedule_config,omitempty"`
}

func (spec Spec) MarshalJSON() ([]byte, error) {
	wire := marshalWireSpec{ID: spec.ID, Name: spec.Name, Type: spec.SourceType()}
	switch spec.SourceType() {
	case SourceTypeCommand:
		config, _ := spec.CommandConfig()
		wireConfig := wireCommandSourceSpec{
			Command: config.Command,
			Args:    config.Args,
			CWD:     config.CWD,
			Env:     config.Env,
			Streams: config.Streams,
			Parser:  config.Parser,
			Filters: config.Filters,
		}
		if config.Batch != (BatchSpec{}) {
			wireConfig.Batch = &config.Batch
		}
		if config.OnExit != (OnExitSpec{}) {
			wireConfig.OnExit = &config.OnExit
		}
		if config.Observation != (CommandObservationSpec{}) {
			wireConfig.Observation = &config.Observation
		}
		wire.CommandConfig = &wireConfig
	case SourceTypeSchedule:
		config, _ := spec.ScheduleConfig()
		wire.ScheduleConfig = &config
	default:
		return nil, fmt.Errorf("observable spec %q has no source", spec.ID)
	}
	return json.Marshal(wire)
}

func (spec *Spec) UnmarshalJSON(data []byte) error {
	decoded, err := decodeWireSpec(data)
	if err != nil {
		return err
	}
	*spec = decoded
	return nil
}

func decodeWireSpec(data []byte) (Spec, error) {
	var wire wireSpec
	if err := decodeStrict(data, &wire); err != nil {
		return Spec{}, err
	}
	switch strings.TrimSpace(wire.Type) {
	case SourceTypeCommand:
		if wire.CommandConfig == nil || wire.ScheduleConfig != nil {
			return Spec{}, fmt.Errorf("type command requires command_config and forbids schedule_config")
		}
		return NewCommandSpec(wire.ID, wire.Name, *wire.CommandConfig)
	case SourceTypeSchedule:
		if wire.ScheduleConfig == nil || wire.CommandConfig != nil {
			return Spec{}, fmt.Errorf("type schedule requires schedule_config and forbids command_config")
		}
		return NewScheduleSpec(wire.ID, wire.Name, *wire.ScheduleConfig)
	default:
		return Spec{}, fmt.Errorf("type must be command or schedule, got %q", wire.Type)
	}
}

func decodeStrict(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

func LoadConfig(path string) (FileConfig, error) {
	cfg, issues, err := LoadConfigLenient(path)
	if err != nil {
		return FileConfig{}, err
	}
	if len(issues) > 0 {
		return FileConfig{}, issues[0].Error
	}
	return cfg, nil
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
	var raw struct {
		Observables []json.RawMessage `json:"observables"`
	}
	if err := decodeStrict(data, &raw); err != nil {
		return FileConfig{}, []ConfigIssue{{ID: "config", Error: fmt.Errorf("observable config: parse %s: %w", path, err)}}, nil
	}
	seen := map[string]struct{}{}
	out := FileConfig{Observables: make([]Spec, 0, len(raw.Observables))}
	var issues []ConfigIssue
	for i, entry := range raw.Observables {
		spec, err := decodeWireSpec(entry)
		if err != nil {
			id, name, hint := rawEntryIdentity(entry, i)
			issueSpec := Spec{ID: id, Name: name}
			issues = append(issues, ConfigIssue{
				ID:    id,
				Spec:  issueSpec,
				Error: fmt.Errorf("observable config: observables[%d] %q: %w; rewrite as type plus %s", i, id, err, hint),
			})
			continue
		}
		if _, ok := seen[spec.ID]; ok {
			id := fmt.Sprintf("%s#%d", spec.ID, i)
			issues = append(issues, ConfigIssue{ID: id, Spec: spec, Error: fmt.Errorf("observable config: duplicate id %q", spec.ID)})
			continue
		}
		seen[spec.ID] = struct{}{}
		out.Observables = append(out.Observables, spec)
	}
	return out, issues, nil
}

func rawEntryIdentity(data []byte, index int) (string, string, string) {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	var id, name, typ string
	_ = json.Unmarshal(raw["id"], &id)
	_ = json.Unmarshal(raw["name"], &name)
	_ = json.Unmarshal(raw["type"], &typ)
	if !validID(strings.TrimSpace(id)) {
		id = fmt.Sprintf("config-%d", index)
	} else {
		id = strings.TrimSpace(id)
	}
	hint := "command_config"
	if typ == SourceTypeSchedule || raw["schedule_config"] != nil {
		hint = "schedule_config"
	} else if source := raw["source"]; source != nil {
		var sourceType struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(source, &sourceType) == nil && sourceType.Type == SourceTypeSchedule {
			hint = "schedule_config"
		}
	}
	return id, name, hint
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
	defer func() { _ = os.Remove(tmpPath) }()
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
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.ID == "" && spec.Name != "" {
		spec.ID = slugObservableID(spec.Name)
	}
	if !validID(spec.ID) {
		return Spec{}, fmt.Errorf("missing or invalid id: id must use lower-case letters, digits, '_' or '-'")
	}
	switch source := spec.source.(type) {
	case CommandSourceSpec:
		return validateCommandSpec(spec.ID, spec.Name, source)
	case ScheduleSourceSpec:
		return validateScheduleSpec(spec.ID, spec.Name, source)
	default:
		return Spec{}, fmt.Errorf("source is required")
	}
}

func validateCommandSpec(id, name string, source CommandSourceSpec) (Spec, error) {
	source = cloneCommandSourceSpec(source)
	source.Command = strings.TrimSpace(source.Command)
	if source.Command == "" {
		return Spec{}, fmt.Errorf("command_config.command is required")
	}
	if source.Batch.IntervalSeconds == 0 {
		source.Batch.IntervalSeconds = DefaultBatchIntervalSeconds
	}
	if source.Batch.MaxChars == 0 {
		source.Batch.MaxChars = DefaultBatchMaxChars
	}
	if len(source.Streams) == 0 {
		source.Streams = []string{StreamStdout, StreamStderr}
	}
	for _, stream := range source.Streams {
		if stream != StreamStdout && stream != StreamStderr {
			return Spec{}, fmt.Errorf("stream must be stdout or stderr, got %q", stream)
		}
	}
	source.Observation.Kind = strings.TrimSpace(source.Observation.Kind)
	source.Observation.Severity = strings.TrimSpace(source.Observation.Severity)
	if err := validateSeverity("observation.severity", source.Observation.Severity); err != nil {
		return Spec{}, err
	}
	if source.Parser != nil {
		parserType := source.Parser.Type
		if parserType == "" {
			parserType = ParserText
			source.Parser.Type = parserType
		}
		if parserType != ParserText && parserType != ParserJSONL {
			return Spec{}, fmt.Errorf("parser.type must be text or jsonl, got %q", parserType)
		}
		source.Parser.AttachmentsField = strings.TrimSpace(source.Parser.AttachmentsField)
		if source.Parser.AttachmentsField != "" && parserType != ParserJSONL {
			return Spec{}, fmt.Errorf("parser.attachments_field requires parser.type jsonl")
		}
	}
	if source.Batch.IntervalSeconds < MinBatchIntervalSeconds || source.Batch.IntervalSeconds > MaxBatchIntervalSeconds {
		return Spec{}, fmt.Errorf("batch.interval_seconds must be between %d and %d", MinBatchIntervalSeconds, MaxBatchIntervalSeconds)
	}
	if source.Batch.MaxChars < 1 || source.Batch.MaxChars > MaxBatchChars {
		return Spec{}, fmt.Errorf("batch.max_chars must be between 1 and %d", MaxBatchChars)
	}
	for i, filter := range source.Filters {
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
	switch source.OnExit.Notify {
	case "", "never", "always", "nonzero":
	default:
		return Spec{}, fmt.Errorf("on_exit.notify must be never, always, or nonzero, got %q", source.OnExit.Notify)
	}
	return Spec{ID: id, Name: name, source: source}, nil
}

func validateScheduleSpec(id, name string, source ScheduleSourceSpec) (Spec, error) {
	source = cloneScheduleSourceSpec(source)
	source.Observation.Kind = strings.TrimSpace(source.Observation.Kind)
	source.Observation.Severity = strings.TrimSpace(source.Observation.Severity)
	source.Observation.Content = strings.TrimSpace(source.Observation.Content)
	var err error
	source.Observation.Attachments, err = normalizeAttachments(source.Observation.Attachments)
	if err != nil {
		return Spec{}, err
	}
	if source.Observation.Kind == "" {
		source.Observation.Kind = DefaultScheduleKind
	}
	if err := validateSeverity("observation.severity", source.Observation.Severity); err != nil {
		return Spec{}, err
	}
	if source.Observation.Severity == "" {
		source.Observation.Severity = DefaultSeverity
	}
	if source.Observation.Content == "" {
		return Spec{}, fmt.Errorf("observation.content is required for schedule sources")
	}
	if len([]rune(source.Observation.Content)) > MaxScheduleContentChars {
		return Spec{}, fmt.Errorf("observation.content must be at most %d characters", MaxScheduleContentChars)
	}
	enabled := 0
	if source.Once != nil {
		enabled++
		if _, err := parseOnceAt(source.Once.At); err != nil {
			return Spec{}, err
		}
	}
	if source.Daily != nil {
		enabled++
		source.Timezone = strings.TrimSpace(source.Timezone)
		if source.Timezone == "" {
			return Spec{}, fmt.Errorf("schedule_config.timezone is required for daily schedules")
		}
		if _, err := time.LoadLocation(source.Timezone); err != nil {
			return Spec{}, fmt.Errorf("schedule_config.timezone must be a valid IANA timezone: %w", err)
		}
		if len(source.Daily.Times) == 0 {
			return Spec{}, fmt.Errorf("schedule_config.daily.times is required")
		}
		for _, value := range source.Daily.Times {
			if _, err := parseDailyClock(value); err != nil {
				return Spec{}, err
			}
		}
		for _, value := range source.Daily.Weekdays {
			if _, ok := weekdayNumber(value); !ok {
				return Spec{}, fmt.Errorf("schedule_config.daily.weekdays contains invalid weekday %q", value)
			}
		}
	}
	if source.Interval != nil {
		enabled++
		if source.Interval.EverySeconds < MinIntervalScheduleSecond {
			return Spec{}, fmt.Errorf("schedule_config.interval.every_seconds must be at least %d", MinIntervalScheduleSecond)
		}
	}
	if enabled != 1 {
		return Spec{}, fmt.Errorf("schedule source must set exactly one of once, daily, or interval")
	}
	if source.CatchUp.Mode == "" {
		source.CatchUp.Mode = ScheduleCatchUpNone
	}
	switch source.CatchUp.Mode {
	case ScheduleCatchUpNone:
	case ScheduleCatchUpLatest:
		if source.CatchUp.MaxLatenessMinutes < 1 || source.CatchUp.MaxLatenessMinutes > 1440 {
			return Spec{}, fmt.Errorf("schedule_config.catch_up.max_lateness_minutes must be between 1 and 1440")
		}
	default:
		return Spec{}, fmt.Errorf("schedule_config.catch_up.mode must be none or latest, got %q", source.CatchUp.Mode)
	}
	return Spec{ID: id, Name: name, source: source}, nil
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

func cloneCommandSourceSpec(in CommandSourceSpec) CommandSourceSpec {
	out := in
	out.Args = append([]string(nil), in.Args...)
	out.Env = cloneStringMap(in.Env)
	out.Streams = append([]string(nil), in.Streams...)
	out.Parser = cloneParserSpec(in.Parser)
	out.Filters = append([]FilterSpec(nil), in.Filters...)
	return out
}

func cloneScheduleSourceSpec(in ScheduleSourceSpec) ScheduleSourceSpec {
	out := in
	if in.Once != nil {
		once := *in.Once
		out.Once = &once
	}
	if in.Daily != nil {
		daily := *in.Daily
		daily.Times = append([]string(nil), in.Daily.Times...)
		daily.Weekdays = append([]string(nil), in.Daily.Weekdays...)
		out.Daily = &daily
	}
	if in.Interval != nil {
		interval := *in.Interval
		out.Interval = &interval
	}
	out.Observation.Attachments = append([]AttachmentSpec(nil), in.Observation.Attachments...)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneParserSpec(in *ParserSpec) *ParserSpec {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func normalizeAttachments(in []AttachmentSpec) ([]AttachmentSpec, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]AttachmentSpec, 0, len(in))
	for i, ref := range in {
		ref.Path = strings.TrimSpace(ref.Path)
		ref.MediaType = strings.TrimSpace(ref.MediaType)
		if ref.Path == "" {
			return nil, fmt.Errorf("observation.attachments[%d].path is required", i)
		}
		out = append(out, ref)
	}
	return out, nil
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

func slugObservableID(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastSep := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastSep = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSep = false
		case r == '_' || r == '-':
			if b.Len() > 0 && !lastSep {
				b.WriteByte(byte(r))
				lastSep = true
			}
		default:
			if b.Len() > 0 && !lastSep {
				b.WriteByte('-')
				lastSep = true
			}
		}
	}
	return strings.Trim(b.String(), "-_")
}

func validateSeverity(field, severity string) error {
	if severity == "" {
		return nil
	}
	if !validSeverityValue(severity) {
		return fmt.Errorf("%s must be info, warning, error, or critical, got %q", field, severity)
	}
	return nil
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
