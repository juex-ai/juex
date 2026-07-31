package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/errorclass"
)

const defaultRemoteReadinessTimeout = 10 * time.Second

// ReadinessStage identifies the configuration or request step that failed.
type ReadinessStage string

const (
	ReadinessStageSelection    ReadinessStage = "selection"
	ReadinessStageCredentials  ReadinessStage = "credentials"
	ReadinessStageConnectivity ReadinessStage = "connectivity"
)

// ReadinessStatus reports whether a readiness stage passed.
type ReadinessStatus string

const (
	ReadinessStatusOK   ReadinessStatus = "ok"
	ReadinessStatusFail ReadinessStatus = "fail"
)

// ReadinessResult is one staged remote MCP readiness outcome.
type ReadinessResult struct {
	Server     string
	Stage      ReadinessStage
	Status     ReadinessStatus
	Message    string
	Suggestion string
	Details    map[string]any
	Err        error
}

// RemoteReadinessProbe verifies a remote server with an MCP request.
type RemoteReadinessProbe interface {
	Probe(ctx context.Context, name string, spec ServerSpec, opts ConnectOptions) error
}

// RemoteReadinessProbeFunc adapts a function into a RemoteReadinessProbe.
type RemoteReadinessProbeFunc func(
	ctx context.Context,
	name string,
	spec ServerSpec,
	opts ConnectOptions,
) error

func (f RemoteReadinessProbeFunc) Probe(
	ctx context.Context,
	name string,
	spec ServerSpec,
	opts ConnectOptions,
) error {
	return f(ctx, name, spec, opts)
}

// RemoteReadinessOptions controls bounded remote MCP diagnostics.
type RemoteReadinessOptions struct {
	Offline        bool
	Timeout        time.Duration
	Probe          RemoteReadinessProbe
	ConnectOptions ConnectOptions
}

// SDKRemoteReadinessProbe opens an SDK session and requests the tool catalog.
type SDKRemoteReadinessProbe struct{}

func (SDKRemoteReadinessProbe) Probe(
	ctx context.Context,
	name string,
	spec ServerSpec,
	opts ConnectOptions,
) error {
	client, err := ConnectWithOptions(ctx, name, spec, opts)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.Close()
	}()
	_, err = client.ListTools(ctx)
	return err
}

// CheckRemoteSelection validates that spec selects one secure remote endpoint.
func CheckRemoteSelection(name string, spec ServerSpec) ReadinessResult {
	details := remoteReadinessDetails(name)
	transport, err := serverTransport(spec)
	if err != nil {
		return readinessFailure(name, ReadinessStageSelection, err, "configure type as http or streamable-http", details)
	}
	if transport != mcpTransportHTTP {
		err := fmt.Errorf("server is not configured with an HTTP transport")
		return readinessFailure(name, ReadinessStageSelection, err, "select a remote MCP server with type http", details)
	}
	if strings.TrimSpace(spec.URL) == "" {
		err := fmt.Errorf("server is not configured with a remote URL")
		return readinessFailure(name, ReadinessStageSelection, err, "select a remote MCP server with a valid url", details)
	}
	if strings.TrimSpace(spec.Command) != "" {
		err := fmt.Errorf("remote server must not also configure command")
		return readinessFailure(name, ReadinessStageSelection, err, "configure exactly one of command or url", details)
	}
	if spec.argsSet || spec.Args != nil {
		err := fmt.Errorf("remote server must not configure command args")
		return readinessFailure(name, ReadinessStageSelection, err, "remove args from the remote MCP server", details)
	}
	if spec.envSet || spec.Env != nil {
		err := fmt.Errorf("remote server must not configure command env")
		return readinessFailure(name, ReadinessStageSelection, err, "remove env from the remote MCP server", details)
	}
	if err := validateSecureEndpoint(spec.URL); err != nil {
		return readinessFailure(name, ReadinessStageSelection, fmt.Errorf("url: %w", err), "fix the remote MCP server url", details)
	}
	return ReadinessResult{
		Server:  name,
		Stage:   ReadinessStageSelection,
		Status:  ReadinessStatusOK,
		Message: "remote MCP server selected",
		Details: details,
	}
}

// CheckRemoteCredentials validates configured remote header credentials.
func CheckRemoteCredentials(name string, spec ServerSpec) ReadinessResult {
	details := remoteReadinessDetails(name)
	if spec.Headers == nil {
		if spec.headersSet {
			err := fmt.Errorf("headers must not be null")
			return readinessFailure(name, ReadinessStageCredentials, err, "configure headers or remove them for anonymous access", details)
		}
		return ReadinessResult{
			Server:  name,
			Stage:   ReadinessStageCredentials,
			Status:  ReadinessStatusOK,
			Message: "anonymous remote MCP access selected",
			Details: details,
		}
	}
	if err := validateHeaders(spec.Headers); err != nil {
		return readinessFailure(
			name,
			ReadinessStageCredentials,
			err,
			"configure valid static HTTP headers",
			details,
		)
	}
	return ReadinessResult{
		Server:  name,
		Stage:   ReadinessStageCredentials,
		Status:  ReadinessStatusOK,
		Message: "remote MCP credentials available",
		Details: details,
	}
}

// CheckRemoteConnectivity runs one bounded remote MCP request.
func CheckRemoteConnectivity(
	ctx context.Context,
	name string,
	spec ServerSpec,
	opts RemoteReadinessOptions,
) ReadinessResult {
	details := remoteReadinessDetails(name)
	if opts.Offline {
		return ReadinessResult{
			Server:  name,
			Stage:   ReadinessStageConnectivity,
			Status:  ReadinessStatusOK,
			Message: "remote MCP connectivity skipped because --offline was set",
			Details: details,
		}
	}
	probe := opts.Probe
	if probe == nil {
		probe = SDKRemoteReadinessProbe{}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultRemoteReadinessTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := probe.Probe(probeCtx, name, spec, opts.ConnectOptions); err != nil {
		return remoteReadinessFailure(name, err)
	}
	return ReadinessResult{
		Server:  name,
		Stage:   ReadinessStageConnectivity,
		Status:  ReadinessStatusOK,
		Message: "remote MCP request passed",
		Details: details,
	}
}

// CheckRemoteReadiness checks selection, credentials, then connectivity.
func CheckRemoteReadiness(
	ctx context.Context,
	name string,
	spec ServerSpec,
	opts RemoteReadinessOptions,
) ReadinessResult {
	if result := CheckRemoteSelection(name, spec); result.Status != ReadinessStatusOK {
		return result
	}
	if result := CheckRemoteCredentials(name, spec); result.Status != ReadinessStatusOK {
		return result
	}
	return CheckRemoteConnectivity(ctx, name, spec, opts)
}

func remoteReadinessFailure(name string, err error) ReadinessResult {
	stage := readinessStageForRemoteError(err)
	suggestion := "check network, DNS, TLS, proxy settings, and the remote MCP service"
	switch stage {
	case ReadinessStageSelection:
		suggestion = "check the remote MCP url and endpoint path"
	case ReadinessStageCredentials:
		suggestion = "check the configured remote MCP headers and credentials"
	}
	return readinessFailure(name, stage, err, suggestion, remoteReadinessDetails(name))
}

func readinessFailure(
	name string,
	stage ReadinessStage,
	err error,
	suggestion string,
	details map[string]any,
) ReadinessResult {
	return ReadinessResult{
		Server:     name,
		Stage:      stage,
		Status:     ReadinessStatusFail,
		Message:    err.Error(),
		Suggestion: suggestion,
		Details:    details,
		Err:        err,
	}
}

func readinessStageForRemoteError(err error) ReadinessStage {
	if stage, ok := ErrorReadinessStage(err); ok {
		return stage
	}
	switch errorclass.Classify(err).Kind {
	case errorclass.KindAuth, errorclass.KindPermission:
		return ReadinessStageCredentials
	case errorclass.KindWrongEndpoint:
		return ReadinessStageSelection
	default:
		return ReadinessStageConnectivity
	}
}

// ErrorReadinessStage returns a stage retained by MCP config error wrapping.
func ErrorReadinessStage(err error) (ReadinessStage, bool) {
	var configErr *ReadinessConfigError
	if errors.As(err, &configErr) && configErr.Stage != "" {
		return configErr.Stage, true
	}
	var credentialErr *CredentialResolutionError
	if errors.As(err, &credentialErr) {
		return ReadinessStageCredentials, true
	}
	return "", false
}

func remoteReadinessDetails(name string) map[string]any {
	return map[string]any{
		"server":    name,
		"transport": "remote",
	}
}

func remoteReadinessServerError(name string, spec ServerSpec, op string, err error) *ServerError {
	if strings.TrimSpace(spec.URL) == "" {
		return &ServerError{Server: name, Op: op, Err: err}
	}
	return &ServerError{
		Server: name,
		Op:     "readiness " + string(readinessStageForRemoteError(err)),
		Err:    err,
	}
}
