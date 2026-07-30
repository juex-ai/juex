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
	workDirEnvKey     = "WORKDIR"
	juexWorkDirEnvKey = "JUEX_WORKDIR"
	extDirEnvKey      = "JUEX_EXT_DIR"
)

// Credential is a sensitive MCP authentication value. Value is the explicit
// boundary where transport construction may access the secret; diagnostics and
// JSON projections remain redacted.
type Credential struct {
	value string
}

func (c *Credential) UnmarshalJSON(data []byte) error {
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

// Value returns the credential only for authentication transport construction.
func (c Credential) Value() string {
	return c.value
}

// AuthSpec selects one supported non-interactive authentication mode.
type AuthSpec struct {
	Token   *Credential      `json:"token,omitempty"`
	Refresh *RefreshAuthSpec `json:"refresh,omitempty"`
}

// RefreshAuthSpec contains the inputs required by oauth2.Config.TokenSource.
type RefreshAuthSpec struct {
	TokenURL     string      `json:"token_url"`
	ClientID     string      `json:"client_id"`
	ClientSecret *Credential `json:"client_secret,omitempty"`
	RefreshToken Credential  `json:"refresh_token"`
	Scopes       []string    `json:"scopes,omitempty"`
}

// ServerSpec mirrors a single entry in mcp.json's mcpServers map. Exactly one
// of Command or URL is valid.
type ServerSpec struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Auth    *AuthSpec         `json:"auth,omitempty"`

	commandSet bool
	argsSet    bool
	envSet     bool
	urlSet     bool
	authSet    bool
}

func (s *ServerSpec) UnmarshalJSON(data []byte) error {
	var raw struct {
		Command json.RawMessage `json:"command"`
		Args    json.RawMessage `json:"args"`
		Env     json.RawMessage `json:"env"`
		URL     json.RawMessage `json:"url"`
		Auth    json.RawMessage `json:"auth"`
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
	if raw.Auth != nil {
		decoded.authSet = true
		if err := decodeStrictConfigValue(raw.Auth, &decoded.Auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	*s = decoded
	return nil
}

func decodeStrictConfigValue(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return ensureConfigEOF(decoder)
}

// Config mirrors the mcp.json file root.
type Config struct {
	MCPServers map[string]ServerSpec `json:"mcpServers"`
}

// LoadConfig reads and validates mcp.json. A missing file produces an empty
// config. Unknown fields are rejected so transport and authentication settings
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

// ValidateConfig validates the local-or-remote tagged union and remote
// authentication shape without including credential values in errors.
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
	hasCommand := spec.commandSet || spec.Command != ""
	hasURL := spec.urlSet || spec.URL != ""
	if hasCommand == hasURL {
		return fmt.Errorf("exactly one of command or url is required")
	}
	if hasCommand {
		if strings.TrimSpace(spec.Command) == "" {
			return fmt.Errorf("command must not be empty")
		}
		if spec.authSet || spec.Auth != nil {
			return fmt.Errorf("auth is only valid for remote servers")
		}
		return nil
	}
	if strings.TrimSpace(spec.URL) == "" {
		return fmt.Errorf("url must not be empty")
	}
	if spec.argsSet || spec.Args != nil {
		return fmt.Errorf("args are only valid for command servers")
	}
	if spec.envSet || spec.Env != nil {
		return fmt.Errorf("env is only valid for command servers")
	}
	if err := validateSecureEndpoint(spec.URL); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if spec.authSet && spec.Auth == nil {
		return fmt.Errorf("auth must not be null")
	}
	return validateAuthSpec(spec.Auth)
}

func validateAuthSpec(auth *AuthSpec) error {
	if auth == nil {
		return nil
	}
	hasToken := auth.Token != nil
	hasRefresh := auth.Refresh != nil
	if hasToken == hasRefresh {
		return fmt.Errorf("auth requires exactly one of token or refresh")
	}
	if hasToken {
		if auth.Token.value == "" {
			return fmt.Errorf("auth.token must not be empty")
		}
		return nil
	}
	refresh := auth.Refresh
	if strings.TrimSpace(refresh.TokenURL) == "" {
		return fmt.Errorf("auth.refresh.token_url is required")
	}
	if err := validateSecureEndpoint(refresh.TokenURL); err != nil {
		return fmt.Errorf("auth.refresh.token_url: %w", err)
	}
	if strings.TrimSpace(refresh.ClientID) == "" {
		return fmt.Errorf("auth.refresh.client_id is required")
	}
	if refresh.ClientSecret != nil && refresh.ClientSecret.value == "" {
		return fmt.Errorf("auth.refresh.client_secret must not be empty")
	}
	if refresh.RefreshToken.value == "" {
		return fmt.Errorf("auth.refresh.refresh_token is required")
	}
	return nil
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
	WorkDir      string
	ExtensionDir string
	Environment  environment.Snapshot
}

// PrepareConfig returns a runtime-ready copy of cfg for a specific Juex work
// directory. It injects workdir env defaults and expands those variables in
// command, args, and env values before MCP subprocesses are launched.
func PrepareConfig(cfg Config, workDir string) (Config, error) {
	return PrepareConfigWithOptions(cfg, PrepareOptions{WorkDir: workDir})
}

// PrepareConfigWithOptions returns a runtime-ready copy of cfg and optionally
// injects extension-specific env such as JUEX_EXT_DIR. Credential references
// are resolved only from the supplied immutable environment snapshot.
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
	out := Config{MCPServers: make(map[string]ServerSpec, len(cfg.MCPServers))}
	for _, name := range sortedServerNames(cfg.MCPServers) {
		spec := cfg.MCPServers[name]
		prepared := ServerSpec{
			Command:    expandRuntimeEnvRefs(spec.Command, runtimeEnv),
			Args:       make([]string, len(spec.Args)),
			URL:        spec.URL,
			commandSet: spec.commandSet,
			argsSet:    spec.argsSet,
			envSet:     spec.envSet,
			urlSet:     spec.urlSet,
			authSet:    spec.authSet,
		}
		if spec.Command != "" {
			prepared.Env = make(map[string]string, len(spec.Env)+len(runtimeEnv))
			for i, arg := range spec.Args {
				prepared.Args[i] = expandRuntimeEnvRefs(arg, runtimeEnv)
			}
			for k, v := range spec.Env {
				prepared.Env[k] = expandRuntimeEnvRefs(v, runtimeEnv)
			}
			for k, v := range runtimeEnv {
				prepared.Env[k] = v
			}
		} else {
			prepared.Args = nil
		}
		var err error
		prepared.Auth, err = prepareAuthSpec(spec.Auth, opts.Environment)
		if err != nil {
			return Config{}, fmt.Errorf("mcp: server %q: %w", name, err)
		}
		out.MCPServers[name] = prepared
	}
	return out, nil
}

func prepareAuthSpec(auth *AuthSpec, snapshot environment.Snapshot) (*AuthSpec, error) {
	if auth == nil {
		return nil, nil
	}
	out := &AuthSpec{}
	if auth.Token != nil {
		token, err := resolveCredential(*auth.Token, snapshot, "auth.token")
		if err != nil {
			return nil, err
		}
		out.Token = &token
	}
	if auth.Refresh != nil {
		refresh := auth.Refresh
		out.Refresh = &RefreshAuthSpec{
			TokenURL: refresh.TokenURL,
			ClientID: refresh.ClientID,
			Scopes:   append([]string(nil), refresh.Scopes...),
		}
		if refresh.ClientSecret != nil {
			clientSecret, err := resolveCredential(*refresh.ClientSecret, snapshot, "auth.refresh.client_secret")
			if err != nil {
				return nil, err
			}
			out.Refresh.ClientSecret = &clientSecret
		}
		refreshToken, err := resolveCredential(refresh.RefreshToken, snapshot, "auth.refresh.refresh_token")
		if err != nil {
			return nil, err
		}
		out.Refresh.RefreshToken = refreshToken
	}
	return out, nil
}

func resolveCredential(credential Credential, snapshot environment.Snapshot, field string) (Credential, error) {
	name, isReference := credentialEnvironmentReference(credential.value)
	if !isReference {
		return credential, nil
	}
	value, ok := snapshot.Lookup(name)
	if !ok || value == "" {
		return Credential{}, fmt.Errorf("%s references unset or empty environment variable %s", field, name)
	}
	return Credential{value: value}, nil
}

func credentialEnvironmentReference(value string) (string, bool) {
	if len(value) < 4 || !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return "", false
	}
	name := value[2 : len(value)-1]
	if name == "" || !isEnvNameStart(name[0]) {
		return "", false
	}
	for i := 1; i < len(name); i++ {
		if !isEnvNameByte(name[i]) {
			return "", false
		}
	}
	return name, true
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
	for _, key := range []string{extDirEnvKey, juexWorkDirEnvKey, workDirEnvKey} {
		value := env[key]
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
