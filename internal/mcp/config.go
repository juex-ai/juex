package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/environment"
)

const (
	workDirEnvKey              = "WORKDIR"
	juexWorkDirEnvKey          = "JUEX_WORKDIR"
	extDirEnvKey               = "JUEX_EXT_DIR"
	extDataDirEnvKey           = "JUEX_EXT_DATA_DIR"
	mcpTransportStdio          = "stdio"
	mcpTransportHTTP           = "http"
	mcpTransportStreamableHTTP = "streamable-http"
)

// Credential is a sensitive MCP header value. Value is the explicit
// boundary where transport construction may access the secret; diagnostics and
// JSON projections remain redacted.
type Credential struct {
	value string
}

// ReadinessConfigError classifies an MCP configuration failure by the
// readiness stage that can repair it without changing its public error text.
type ReadinessConfigError struct {
	Stage ReadinessStage
	Err   error
}

func (e *ReadinessConfigError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ReadinessConfigError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// CredentialResolutionError identifies a missing environment-backed MCP header
// without retaining or rendering the credential expression.
type CredentialResolutionError struct {
	Field               string
	EnvironmentVariable string
}

func (e *CredentialResolutionError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(
		"%s references unset or empty environment variable %s",
		e.Field,
		e.EnvironmentVariable,
	)
}

func (c *Credential) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("must be a string")
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	c.value = value
	return nil
}

func (c Credential) MarshalJSON() ([]byte, error) {
	return json.Marshal("[REDACTED]")
}

func (c Credential) String() string {
	return "[REDACTED]"
}

func (c Credential) GoString() string {
	return "[REDACTED]"
}

func (c Credential) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[REDACTED]")
}

// Value returns the header value only for HTTP transport construction.
func (c Credential) Value() string {
	return c.value
}

// ServerSpec mirrors the Claude MCP JSON shape supported by Juex. An omitted
// type selects stdio; http and streamable-http both select Streamable HTTP.
type ServerSpec struct {
	Type    string                `json:"type,omitempty"`
	Command string                `json:"command,omitempty"`
	Args    []string              `json:"args,omitempty"`
	Env     map[string]string     `json:"env,omitempty"`
	URL     string                `json:"url,omitempty"`
	Headers map[string]Credential `json:"headers,omitempty"`

	typeSet    bool
	commandSet bool
	argsSet    bool
	envSet     bool
	urlSet     bool
	headersSet bool

	extensionDataDirSet bool
}

func (s *ServerSpec) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type    json.RawMessage `json:"type"`
		Command json.RawMessage `json:"command"`
		Args    json.RawMessage `json:"args"`
		Env     json.RawMessage `json:"env"`
		URL     json.RawMessage `json:"url"`
		Headers json.RawMessage `json:"headers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := ensureConfigEOF(decoder); err != nil {
		return err
	}

	var decoded ServerSpec
	if raw.Type != nil {
		decoded.typeSet = true
		if err := json.Unmarshal(raw.Type, &decoded.Type); err != nil {
			return fmt.Errorf("type: %w", err)
		}
	}
	if raw.Command != nil {
		decoded.commandSet = true
		if err := json.Unmarshal(raw.Command, &decoded.Command); err != nil {
			return fmt.Errorf("command: %w", err)
		}
	}
	if raw.Args != nil {
		decoded.argsSet = true
		if err := json.Unmarshal(raw.Args, &decoded.Args); err != nil {
			return fmt.Errorf("args: %w", err)
		}
	}
	if raw.Env != nil {
		decoded.envSet = true
		if err := json.Unmarshal(raw.Env, &decoded.Env); err != nil {
			return fmt.Errorf("env: %w", err)
		}
	}
	if raw.URL != nil {
		decoded.urlSet = true
		if err := json.Unmarshal(raw.URL, &decoded.URL); err != nil {
			return fmt.Errorf("url: %w", err)
		}
	}
	if raw.Headers != nil {
		decoded.headersSet = true
		if err := json.Unmarshal(raw.Headers, &decoded.Headers); err != nil {
			return fmt.Errorf("headers: %w", err)
		}
	}
	*s = decoded
	return nil
}

// Config mirrors the mcp.json file root.
type Config struct {
	MCPServers map[string]ServerSpec `json:"mcpServers"`
}

// HasLocalServers reports whether the config contains a process-backed stdio
// server. Transport classification remains owned by the MCP package.
func (c Config) HasLocalServers() bool {
	for _, spec := range c.MCPServers {
		transport, err := serverTransport(spec)
		if err == nil && transport == mcpTransportStdio {
			return true
		}
	}
	return false
}

// LoadConfig reads and validates mcp.json. A missing file produces an empty
// config. Unknown fields are rejected so transport and security settings
// cannot be silently ignored.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("mcp: %s: %w", path, err)
	}
	if err := ensureConfigEOF(decoder); err != nil {
		return Config{}, fmt.Errorf("mcp: %s: %w", path, err)
	}
	if err := ValidateConfig(cfg); err != nil {
		return Config{}, fmt.Errorf("mcp: %s: %w", path, err)
	}
	return cfg, nil
}

func ensureConfigEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}

// ValidateConfig validates the supported Claude transport subset without
// including header values in errors.
func ValidateConfig(cfg Config) error {
	for _, name := range sortedServerNames(cfg.MCPServers) {
		if err := validateServerSpec(cfg.MCPServers[name]); err != nil {
			return fmt.Errorf("server %q: %w", name, err)
		}
	}
	return nil
}

func sortedServerNames(servers map[string]ServerSpec) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateServerSpec(spec ServerSpec) error {
	transport, err := serverTransport(spec)
	if err != nil {
		return selectionConfigError(err)
	}
	switch transport {
	case mcpTransportStdio:
		if strings.TrimSpace(spec.Command) == "" {
			return selectionConfigError(fmt.Errorf("command is required for stdio servers"))
		}
		if spec.urlSet || spec.URL != "" {
			return selectionConfigError(fmt.Errorf("url is not valid for stdio servers"))
		}
		if spec.headersSet || spec.Headers != nil {
			return selectionConfigError(fmt.Errorf("headers are only valid for HTTP servers"))
		}
		return nil
	case mcpTransportHTTP:
		if strings.TrimSpace(spec.URL) == "" {
			return selectionConfigError(fmt.Errorf("url is required for HTTP servers"))
		}
		if spec.commandSet || spec.Command != "" {
			return selectionConfigError(fmt.Errorf("command is not valid for HTTP servers"))
		}
		if spec.argsSet || spec.Args != nil {
			return selectionConfigError(fmt.Errorf("args are only valid for stdio servers"))
		}
		if spec.envSet || spec.Env != nil {
			return selectionConfigError(fmt.Errorf("env is only valid for stdio servers"))
		}
		if err := validateSecureEndpoint(spec.URL); err != nil {
			return selectionConfigError(fmt.Errorf("url: %w", err))
		}
		if spec.headersSet && spec.Headers == nil {
			return credentialsConfigError(fmt.Errorf("headers must not be null"))
		}
		if err := validateHeaders(spec.Headers); err != nil {
			return credentialsConfigError(err)
		}
		return nil
	default:
		panic("unreachable MCP transport")
	}
}

func serverTransport(spec ServerSpec) (string, error) {
	transport := strings.TrimSpace(spec.Type)
	if !spec.typeSet && transport == "" {
		if spec.urlSet {
			return "", fmt.Errorf(`server has a url but no type; add "type": "http"`)
		}
		if spec.URL != "" {
			// Programmatic internal specs predate the JSON type field.
			return mcpTransportHTTP, nil
		}
		return mcpTransportStdio, nil
	}
	switch transport {
	case mcpTransportStdio:
		return mcpTransportStdio, nil
	case mcpTransportHTTP, mcpTransportStreamableHTTP:
		return mcpTransportHTTP, nil
	case "sse":
		return "", fmt.Errorf(`transport type "sse" is not supported; use "http" for Streamable HTTP`)
	case "ws":
		return "", fmt.Errorf(`transport type "ws" is not supported; Juex does not implement Claude's WebSocket extension`)
	default:
		return "", fmt.Errorf("unsupported transport type %q", transport)
	}
}

func selectionConfigError(err error) error {
	return &ReadinessConfigError{Stage: ReadinessStageSelection, Err: err}
}

func credentialsConfigError(err error) error {
	return &ReadinessConfigError{Stage: ReadinessStageCredentials, Err: err}
}

func validateHeaders(headers map[string]Credential) error {
	seen := make(map[string]string, len(headers))
	for _, name := range sortedCredentialNames(headers) {
		if !validHTTPHeaderName(name) {
			return fmt.Errorf("invalid header name %q", name)
		}
		canonical := strings.ToLower(name)
		if previous, ok := seen[canonical]; ok {
			return fmt.Errorf("duplicate header names %q and %q", previous, name)
		}
		seen[canonical] = name
		if strings.ContainsAny(headers[name].value, "\r\n") {
			return fmt.Errorf("header %q must not contain newlines", name)
		}
	}
	return nil
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(b)) {
			continue
		}
		return false
	}
	return true
}

func sortedCredentialNames(values map[string]Credential) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateSecureEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("must be a valid absolute URL")
	}
	if !parsed.IsAbs() {
		return fmt.Errorf("must be an absolute URL")
	}
	if parsed.Host == "" {
		return fmt.Errorf("must include a host")
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("must include a hostname")
	}
	if parsed.User != nil {
		return fmt.Errorf("must not include userinfo")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("must not include a fragment")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return fmt.Errorf("must use https unless the host is loopback")
	default:
		return fmt.Errorf("must use https unless the host is loopback")
	}
}

func isLoopbackHost(host string) bool {
	normalized := strings.TrimSuffix(strings.ToLower(host), ".")
	if normalized == "localhost" {
		return true
	}
	ip := net.ParseIP(normalized)
	return ip != nil && ip.IsLoopback()
}

type PrepareOptions struct {
	WorkDir          string
	ExtensionDir     string
	ExtensionDataDir string
	Environment      environment.Snapshot
}

// PrepareConfig returns a runtime-ready copy of cfg for a specific Juex work
// directory. It injects workdir env defaults and expands those variables in
// command, args, and env values before MCP subprocesses are launched.
func PrepareConfig(cfg Config, workDir string) (Config, error) {
	return PrepareConfigWithOptions(cfg, PrepareOptions{WorkDir: workDir})
}

// PrepareConfigWithOptions returns a runtime-ready copy of cfg and optionally
// injects extension-specific env such as JUEX_EXT_DIR. Header references are
// resolved only from the supplied immutable environment snapshot.
func PrepareConfigWithOptions(cfg Config, opts PrepareOptions) (Config, error) {
	if len(cfg.MCPServers) == 0 {
		return Config{}, nil
	}
	if err := ValidateConfig(cfg); err != nil {
		return Config{}, fmt.Errorf("mcp: %w", err)
	}
	runtimeEnv := RuntimeEnv(opts.WorkDir)
	if opts.ExtensionDir != "" {
		extDir := opts.ExtensionDir
		if abs, err := filepath.Abs(extDir); err == nil {
			extDir = abs
		}
		runtimeEnv[extDirEnvKey] = extDir
	}
	if opts.ExtensionDataDir != "" {
		dataDir := opts.ExtensionDataDir
		if abs, err := filepath.Abs(dataDir); err == nil {
			dataDir = abs
		}
		runtimeEnv[extDataDirEnvKey] = dataDir
	}
	out := Config{MCPServers: make(map[string]ServerSpec, len(cfg.MCPServers))}
	for _, name := range sortedServerNames(cfg.MCPServers) {
		spec := cfg.MCPServers[name]
		prepared := ServerSpec{
			Type:       spec.Type,
			Command:    expandRuntimeEnvRefs(spec.Command, runtimeEnv),
			Args:       make([]string, len(spec.Args)),
			URL:        spec.URL,
			typeSet:    spec.typeSet,
			commandSet: spec.commandSet,
			argsSet:    spec.argsSet,
			envSet:     spec.envSet,
			urlSet:     spec.urlSet,
			headersSet: spec.headersSet,
		}
		if spec.Command != "" {
			prepared.extensionDataDirSet = opts.ExtensionDataDir != ""
			prepared.Env = make(map[string]string, len(spec.Env)+len(runtimeEnv))
			for i, arg := range spec.Args {
				prepared.Args[i] = expandRuntimeEnvRefs(arg, runtimeEnv)
			}
			for k, v := range spec.Env {
				if strings.EqualFold(k, extDataDirEnvKey) {
					continue
				}
				prepared.Env[k] = expandRuntimeEnvRefs(v, runtimeEnv)
			}
			for k, v := range runtimeEnv {
				prepared.Env[k] = v
			}
		} else {
			prepared.Args = nil
		}
		var err error
		prepared.Headers, err = prepareHeaders(spec.Headers, opts.Environment)
		if err != nil {
			return Config{}, fmt.Errorf("mcp: server %q: %w", name, err)
		}
		out.MCPServers[name] = prepared
	}
	if err := ValidateConfig(out); err != nil {
		return Config{}, fmt.Errorf("mcp: %w", err)
	}
	return out, nil
}

func prepareHeaders(headers map[string]Credential, snapshot environment.Snapshot) (map[string]Credential, error) {
	if headers == nil {
		return nil, nil
	}
	out := make(map[string]Credential, len(headers))
	for _, name := range sortedCredentialNames(headers) {
		value, err := resolveCredentialTemplate(headers[name], snapshot, "headers."+name)
		if err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, nil
}

func resolveCredentialTemplate(
	credential Credential,
	snapshot environment.Snapshot,
	field string,
) (Credential, error) {
	input := credential.value
	var out strings.Builder
	for offset := 0; offset < len(input); {
		start := strings.Index(input[offset:], "${")
		if start < 0 {
			out.WriteString(input[offset:])
			break
		}
		start += offset
		out.WriteString(input[offset:start])
		end := strings.IndexByte(input[start+2:], '}')
		if end < 0 {
			out.WriteString(input[start:])
			break
		}
		end += start + 2
		expression := input[start+2 : end]
		name, fallback, hasFallback := strings.Cut(expression, ":-")
		if !validEnvironmentName(name) {
			out.WriteString(input[start : end+1])
			offset = end + 1
			continue
		}
		value, ok := snapshot.Lookup(name)
		if !ok || value == "" {
			if !hasFallback {
				return Credential{}, &CredentialResolutionError{
					Field:               field,
					EnvironmentVariable: name,
				}
			}
			value = fallback
		}
		out.WriteString(value)
		offset = end + 1
	}
	return Credential{value: out.String()}, nil
}

func validEnvironmentName(name string) bool {
	if name == "" || !isEnvNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isEnvNameByte(name[i]) {
			return false
		}
	}
	return true
}

func isEnvNameStart(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// RuntimeEnv returns the environment variables Juex injects into MCP servers.
func RuntimeEnv(workDir string) map[string]string {
	absWorkDir := workDir
	if abs, err := filepath.Abs(workDir); err == nil {
		absWorkDir = abs
	}
	return map[string]string{
		workDirEnvKey:     absWorkDir,
		juexWorkDirEnvKey: absWorkDir,
	}
}

func expandRuntimeEnvRefs(s string, env map[string]string) string {
	for _, key := range []string{extDataDirEnvKey, extDirEnvKey, juexWorkDirEnvKey, workDirEnvKey} {
		value, ok := env[key]
		if !ok {
			continue
		}
		s = strings.ReplaceAll(s, "${"+key+"}", value)
		s = replaceUnbracedEnvRef(s, key, value)
	}
	return s
}

func replaceUnbracedEnvRef(s, key, value string) string {
	needle := "$" + key
	if !strings.Contains(s, needle) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	start := 0
	for {
		idx := strings.Index(s[start:], needle)
		if idx < 0 {
			b.WriteString(s[start:])
			break
		}
		idx += start
		end := idx + len(needle)
		if end < len(s) && isEnvNameByte(s[end]) {
			b.WriteString(s[start:end])
			start = end
			continue
		}
		b.WriteString(s[start:idx])
		b.WriteString(value)
		start = end
	}
	return b.String()
}

func isEnvNameByte(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}
