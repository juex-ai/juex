package observable

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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

type Spec struct {
	ID          string          `json:"id"`
	Name        string          `json:"name,omitempty"`
	Source      SourceSpec      `json:"source,omitempty"`
	Observation ObservationSpec `json:"observation,omitempty"`

	// Legacy command shorthand. It remains accepted on read and through older
	// callers, but explicit source config is the preferred persisted shape.
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	CWD      string            `json:"cwd,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Streams  []string          `json:"streams,omitempty"`
	Defaults Defaults          `json:"defaults,omitempty"`
	Parser   *ParserSpec       `json:"parser,omitempty"`
	Filters  []FilterSpec      `json:"filters,omitempty"`
	Batch    BatchSpec         `json:"batch,omitempty"`
	OnExit   OnExitSpec        `json:"on_exit,omitempty"`
}

type SourceSpec struct {
	Type string `json:"type,omitempty"`

	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	CWD     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Streams []string          `json:"streams,omitempty"`
	Parser  *ParserSpec       `json:"parser,omitempty"`
	Filters []FilterSpec      `json:"filters,omitempty"`
	Batch   BatchSpec         `json:"batch,omitempty"`
	OnExit  OnExitSpec        `json:"on_exit,omitempty"`

	Timezone string            `json:"timezone,omitempty"`
	Once     *OnceSchedule     `json:"once,omitempty"`
	Daily    *DailySchedule    `json:"daily,omitempty"`
	Interval *IntervalSchedule `json:"interval,omitempty"`
	CatchUp  CatchUpSpec       `json:"catch_up,omitempty"`
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

type ObservationSpec struct {
	Kind     string `json:"kind,omitempty"`
	Severity string `json:"severity,omitempty"`
	Content  string `json:"content,omitempty"`
}

func (spec Spec) MarshalJSON() ([]byte, error) {
	type specJSON struct {
		ID          string            `json:"id"`
		Name        string            `json:"name,omitempty"`
		Source      *SourceSpec       `json:"source,omitempty"`
		Observation *ObservationSpec  `json:"observation,omitempty"`
		Command     string            `json:"command,omitempty"`
		Args        []string          `json:"args,omitempty"`
		CWD         string            `json:"cwd,omitempty"`
		Env         map[string]string `json:"env,omitempty"`
		Streams     []string          `json:"streams,omitempty"`
		Defaults    *Defaults         `json:"defaults,omitempty"`
		Parser      *ParserSpec       `json:"parser,omitempty"`
		Filters     []FilterSpec      `json:"filters,omitempty"`
		Batch       *BatchSpec        `json:"batch,omitempty"`
		OnExit      *OnExitSpec       `json:"on_exit,omitempty"`
	}
	out := specJSON{
		ID:      spec.ID,
		Name:    spec.Name,
		Command: spec.Command,
		Args:    spec.Args,
		CWD:     spec.CWD,
		Env:     spec.Env,
		Streams: spec.Streams,
		Parser:  spec.Parser,
		Filters: spec.Filters,
	}
	if spec.Source.Type != "" {
		out.Source = &spec.Source
	}
	if spec.Observation != (ObservationSpec{}) {
		out.Observation = &spec.Observation
	}
	if spec.Defaults != (Defaults{}) {
		out.Defaults = &spec.Defaults
	}
	if spec.Batch != (BatchSpec{}) {
		out.Batch = &spec.Batch
	}
	if spec.OnExit != (OnExitSpec{}) {
		out.OnExit = &spec.OnExit
	}
	return json.Marshal(out)
}

func (source SourceSpec) MarshalJSON() ([]byte, error) {
	type sourceJSON struct {
		Type     string            `json:"type,omitempty"`
		Command  string            `json:"command,omitempty"`
		Args     []string          `json:"args,omitempty"`
		CWD      string            `json:"cwd,omitempty"`
		Env      map[string]string `json:"env,omitempty"`
		Streams  []string          `json:"streams,omitempty"`
		Parser   *ParserSpec       `json:"parser,omitempty"`
		Filters  []FilterSpec      `json:"filters,omitempty"`
		Batch    *BatchSpec        `json:"batch,omitempty"`
		OnExit   *OnExitSpec       `json:"on_exit,omitempty"`
		Timezone string            `json:"timezone,omitempty"`
		Once     *OnceSchedule     `json:"once,omitempty"`
		Daily    *DailySchedule    `json:"daily,omitempty"`
		Interval *IntervalSchedule `json:"interval,omitempty"`
		CatchUp  *CatchUpSpec      `json:"catch_up,omitempty"`
	}
	out := sourceJSON{
		Type:     source.Type,
		Command:  source.Command,
		Args:     source.Args,
		CWD:      source.CWD,
		Env:      source.Env,
		Streams:  source.Streams,
		Parser:   source.Parser,
		Filters:  source.Filters,
		Timezone: source.Timezone,
		Once:     source.Once,
		Daily:    source.Daily,
		Interval: source.Interval,
	}
	if source.Batch != (BatchSpec{}) {
		out.Batch = &source.Batch
	}
	if source.OnExit != (OnExitSpec{}) {
		out.OnExit = &source.OnExit
	}
	if source.CatchUp != (CatchUpSpec{}) {
		out.CatchUp = &source.CatchUp
	}
	return json.Marshal(out)
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
	cfg = PreferredConfig(cfg)
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
	spec.Command = strings.TrimSpace(spec.Command)
	spec.Source.Type = strings.TrimSpace(spec.Source.Type)
	if !validID(spec.ID) {
		return Spec{}, fmt.Errorf("id must use lower-case letters, digits, '_' or '-'")
	}
	if spec.Source.Type == "" {
		if hasLegacyCommandShorthand(spec) {
			spec.Source.Type = SourceTypeCommand
			spec.Source.Command = spec.Command
			spec.Source.Args = append([]string(nil), spec.Args...)
			spec.Source.CWD = spec.CWD
			spec.Source.Env = cloneStringMap(spec.Env)
			spec.Source.Streams = append([]string(nil), spec.Streams...)
			spec.Source.Parser = cloneParserSpec(spec.Parser)
			spec.Source.Filters = append([]FilterSpec(nil), spec.Filters...)
			spec.Source.Batch = spec.Batch
			spec.Source.OnExit = spec.OnExit
		} else {
			return Spec{}, fmt.Errorf("source.type is required")
		}
	} else if hasLegacyCommandShorthand(spec) && !legacyMirrorsCommandSource(spec) {
		return Spec{}, fmt.Errorf("legacy command shorthand cannot be mixed with explicit source")
	}
	switch spec.Source.Type {
	case SourceTypeCommand:
		return validateCommandSpec(spec)
	case SourceTypeSchedule:
		return validateScheduleSpec(spec)
	default:
		return Spec{}, fmt.Errorf("source.type must be command or schedule, got %q", spec.Source.Type)
	}
}

func validateCommandSpec(spec Spec) (Spec, error) {
	spec.Source.Command = strings.TrimSpace(spec.Source.Command)
	if err := rejectInactiveScheduleFields(spec.Source); err != nil {
		return Spec{}, err
	}
	if spec.Source.Command == "" {
		return Spec{}, fmt.Errorf("source.command is required")
	}
	if len(spec.Source.Streams) == 0 {
		spec.Source.Streams = []string{StreamStdout, StreamStderr}
	}
	for _, stream := range spec.Source.Streams {
		switch stream {
		case StreamStdout, StreamStderr:
		default:
			return Spec{}, fmt.Errorf("stream must be stdout or stderr, got %q", stream)
		}
	}
	if spec.Observation.Kind != "" && spec.Defaults.Kind == "" {
		spec.Defaults.Kind = spec.Observation.Kind
	}
	if spec.Observation.Severity != "" && spec.Defaults.Severity == "" {
		spec.Defaults.Severity = spec.Observation.Severity
	}
	if err := validateSeverity("defaults.severity", spec.Defaults.Severity); err != nil {
		return Spec{}, err
	}
	if spec.Source.Parser != nil {
		parserType := spec.Source.Parser.Type
		if parserType == "" {
			parserType = ParserText
			spec.Source.Parser.Type = parserType
		}
		switch parserType {
		case ParserText, ParserJSONL:
		default:
			return Spec{}, fmt.Errorf("parser.type must be text or jsonl, got %q", parserType)
		}
	}
	if spec.Source.Batch.IntervalSeconds < MinBatchIntervalSeconds || spec.Source.Batch.IntervalSeconds > MaxBatchIntervalSeconds {
		return Spec{}, fmt.Errorf("batch.interval_seconds must be between %d and %d", MinBatchIntervalSeconds, MaxBatchIntervalSeconds)
	}
	if spec.Source.Batch.MaxChars < 1 || spec.Source.Batch.MaxChars > MaxBatchChars {
		return Spec{}, fmt.Errorf("batch.max_chars must be between 1 and %d", MaxBatchChars)
	}
	for i, filter := range spec.Source.Filters {
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
	switch spec.Source.OnExit.Notify {
	case "", "never", "always", "nonzero":
	default:
		return Spec{}, fmt.Errorf("on_exit.notify must be never, always, or nonzero, got %q", spec.Source.OnExit.Notify)
	}
	spec.Command = spec.Source.Command
	spec.Args = append([]string(nil), spec.Source.Args...)
	spec.CWD = spec.Source.CWD
	spec.Env = cloneStringMap(spec.Source.Env)
	spec.Streams = append([]string(nil), spec.Source.Streams...)
	spec.Parser = cloneParserSpec(spec.Source.Parser)
	spec.Filters = append([]FilterSpec(nil), spec.Source.Filters...)
	spec.Batch = spec.Source.Batch
	spec.OnExit = spec.Source.OnExit
	return spec, nil
}

func validateScheduleSpec(spec Spec) (Spec, error) {
	if err := rejectInactiveCommandFields(spec.Source); err != nil {
		return Spec{}, err
	}
	if strings.TrimSpace(spec.Defaults.Kind) != "" || strings.TrimSpace(spec.Defaults.Severity) != "" {
		return Spec{}, fmt.Errorf("schedule source cannot set defaults; use observation.kind and observation.severity")
	}
	spec.Observation.Kind = strings.TrimSpace(spec.Observation.Kind)
	spec.Observation.Severity = strings.TrimSpace(spec.Observation.Severity)
	spec.Observation.Content = strings.TrimSpace(spec.Observation.Content)
	if spec.Observation.Kind == "" {
		spec.Observation.Kind = DefaultScheduleKind
	}
	if err := validateSeverity("observation.severity", spec.Observation.Severity); err != nil {
		return Spec{}, err
	}
	if spec.Observation.Severity == "" {
		spec.Observation.Severity = DefaultSeverity
	}
	if spec.Observation.Content == "" {
		return Spec{}, fmt.Errorf("observation.content is required for schedule sources")
	}
	if len([]rune(spec.Observation.Content)) > MaxScheduleContentChars {
		return Spec{}, fmt.Errorf("observation.content must be at most %d characters", MaxScheduleContentChars)
	}
	enabled := 0
	if spec.Source.Once != nil {
		enabled++
		if _, err := parseOnceAt(spec.Source.Once.At); err != nil {
			return Spec{}, err
		}
	}
	if spec.Source.Daily != nil {
		enabled++
		spec.Source.Timezone = strings.TrimSpace(spec.Source.Timezone)
		if spec.Source.Timezone == "" {
			return Spec{}, fmt.Errorf("source.timezone is required for daily schedules")
		}
		if _, err := time.LoadLocation(spec.Source.Timezone); err != nil {
			return Spec{}, fmt.Errorf("source.timezone must be a valid IANA timezone: %w", err)
		}
		if len(spec.Source.Daily.Times) == 0 {
			return Spec{}, fmt.Errorf("source.daily.times is required")
		}
		for _, value := range spec.Source.Daily.Times {
			if _, err := parseDailyClock(value); err != nil {
				return Spec{}, err
			}
		}
		for _, value := range spec.Source.Daily.Weekdays {
			if _, ok := weekdayNumber(value); !ok {
				return Spec{}, fmt.Errorf("source.daily.weekdays contains invalid weekday %q", value)
			}
		}
	}
	if spec.Source.Interval != nil {
		enabled++
		if spec.Source.Interval.EverySeconds < MinIntervalScheduleSecond {
			return Spec{}, fmt.Errorf("source.interval.every_seconds must be at least %d", MinIntervalScheduleSecond)
		}
	}
	if enabled != 1 {
		return Spec{}, fmt.Errorf("schedule source must set exactly one of once, daily, or interval")
	}
	if spec.Source.CatchUp.Mode == "" {
		spec.Source.CatchUp.Mode = ScheduleCatchUpNone
	}
	switch spec.Source.CatchUp.Mode {
	case ScheduleCatchUpNone:
	case ScheduleCatchUpLatest:
		if spec.Source.CatchUp.MaxLatenessMinutes < 1 || spec.Source.CatchUp.MaxLatenessMinutes > 1440 {
			return Spec{}, fmt.Errorf("source.catch_up.max_lateness_minutes must be between 1 and 1440")
		}
	default:
		return Spec{}, fmt.Errorf("source.catch_up.mode must be none or latest, got %q", spec.Source.CatchUp.Mode)
	}
	return spec, nil
}

func rejectInactiveCommandFields(source SourceSpec) error {
	var fields []string
	if strings.TrimSpace(source.Command) != "" {
		fields = append(fields, "source.command")
	}
	if len(source.Args) > 0 {
		fields = append(fields, "source.args")
	}
	if strings.TrimSpace(source.CWD) != "" {
		fields = append(fields, "source.cwd")
	}
	if len(source.Env) > 0 {
		fields = append(fields, "source.env")
	}
	if len(source.Streams) > 0 {
		fields = append(fields, "source.streams")
	}
	if source.Parser != nil {
		fields = append(fields, "source.parser")
	}
	if len(source.Filters) > 0 {
		fields = append(fields, "source.filters")
	}
	if source.Batch != (BatchSpec{}) {
		fields = append(fields, "source.batch")
	}
	if source.OnExit != (OnExitSpec{}) {
		fields = append(fields, "source.on_exit")
	}
	if len(fields) > 0 {
		return fmt.Errorf("schedule source cannot set command fields: %s", strings.Join(fields, ", "))
	}
	return nil
}

func rejectInactiveScheduleFields(source SourceSpec) error {
	var fields []string
	if strings.TrimSpace(source.Timezone) != "" {
		fields = append(fields, "source.timezone")
	}
	if source.Once != nil {
		fields = append(fields, "source.once")
	}
	if source.Daily != nil {
		fields = append(fields, "source.daily")
	}
	if source.Interval != nil {
		fields = append(fields, "source.interval")
	}
	if source.CatchUp != (CatchUpSpec{}) {
		fields = append(fields, "source.catch_up")
	}
	if len(fields) > 0 {
		return fmt.Errorf("command source cannot set schedule fields: %s", strings.Join(fields, ", "))
	}
	return nil
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

func PreferredConfig(cfg FileConfig) FileConfig {
	out := FileConfig{Observables: make([]Spec, 0, len(cfg.Observables))}
	for _, spec := range cfg.Observables {
		out.Observables = append(out.Observables, PreferredSpec(spec))
	}
	return out
}

func PreferredSpec(spec Spec) Spec {
	switch spec.Source.Type {
	case SourceTypeCommand:
		spec.Source.Command = spec.Command
		spec.Source.Args = append([]string(nil), spec.Args...)
		spec.Source.CWD = spec.CWD
		spec.Source.Env = cloneStringMap(spec.Env)
		spec.Source.Streams = append([]string(nil), spec.Streams...)
		spec.Source.Parser = cloneParserSpec(spec.Parser)
		spec.Source.Filters = append([]FilterSpec(nil), spec.Filters...)
		spec.Source.Batch = spec.Batch
		spec.Source.OnExit = spec.OnExit
		spec.Command = ""
		spec.Args = nil
		spec.CWD = ""
		spec.Env = nil
		spec.Streams = nil
		spec.Parser = nil
		spec.Filters = nil
		spec.Batch = BatchSpec{}
		spec.OnExit = OnExitSpec{}
	case SourceTypeSchedule:
		spec.Command = ""
		spec.Args = nil
		spec.CWD = ""
		spec.Env = nil
		spec.Streams = nil
		spec.Defaults = Defaults{}
		spec.Parser = nil
		spec.Filters = nil
		spec.Batch = BatchSpec{}
		spec.OnExit = OnExitSpec{}
	}
	return spec
}

func hasLegacyCommandShorthand(spec Spec) bool {
	return strings.TrimSpace(spec.Command) != "" ||
		len(spec.Args) > 0 ||
		strings.TrimSpace(spec.CWD) != "" ||
		len(spec.Env) > 0 ||
		len(spec.Streams) > 0 ||
		spec.Parser != nil ||
		len(spec.Filters) > 0 ||
		spec.Batch != (BatchSpec{}) ||
		spec.OnExit != (OnExitSpec{})
}

func legacyMirrorsCommandSource(spec Spec) bool {
	if spec.Source.Type != SourceTypeCommand {
		return false
	}
	if strings.TrimSpace(spec.Command) != strings.TrimSpace(spec.Source.Command) {
		return false
	}
	if !stringSlicesEqual(spec.Args, spec.Source.Args) {
		return false
	}
	if spec.CWD != spec.Source.CWD || !stringMapsEqual(spec.Env, spec.Source.Env) {
		return false
	}
	if !stringSlicesEqual(spec.Streams, spec.Source.Streams) {
		return false
	}
	if !parserSpecEqual(spec.Parser, spec.Source.Parser) {
		return false
	}
	if !filterSpecsEqual(spec.Filters, spec.Source.Filters) {
		return false
	}
	return spec.Batch == spec.Source.Batch && spec.OnExit == spec.Source.OnExit
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		if b[key] != av {
			return false
		}
	}
	return true
}

func parserSpecEqual(a, b *ParserSpec) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func filterSpecsEqual(a, b []FilterSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
