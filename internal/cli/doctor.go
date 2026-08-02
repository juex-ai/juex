package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/providerreadiness"
	"github.com/juex-ai/juex/internal/skills"
	toolruntime "github.com/juex-ai/juex/internal/tools"
)

type doctorStatus string

const (
	doctorStatusOK   doctorStatus = "ok"
	doctorStatusWarn doctorStatus = "warn"
	doctorStatusFail doctorStatus = "fail"
)

type doctorCheck struct {
	Name       string         `json:"name"`
	Status     doctorStatus   `json:"status"`
	Message    string         `json:"message"`
	Suggestion string         `json:"suggestion,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

type doctorResult struct {
	Status      doctorStatus         `json:"status"`
	Checks      []doctorCheck        `json:"checks"`
	environment environment.Snapshot `json:"-"`
}

type doctorExitError struct {
	status doctorStatus
}

func (e *doctorExitError) Error() string {
	return "juex doctor: " + string(e.status)
}

func (e *doctorExitError) ExitCode() int {
	if e == nil {
		return ExitSuccess
	}
	switch e.status {
	case doctorStatusWarn:
		return ExitDoctorWarning
	case doctorStatusFail:
		return ExitDoctorFailure
	default:
		return ExitSuccess
	}
}

func newDoctorCmd(flags *persistentFlags) *cobra.Command {
	var (
		format  string
		offline bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check Juex runtime config, credentials, and local resources",
		Example: `  juex doctor
  juex doctor --offline
  juex doctor --format json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format == "table" {
				format = "text"
			}
			if format != "text" && format != "json" {
				return &usageError{msg: "--format must be text, table, or json"}
			}
			result := runDoctor(cmd, flags, offline)
			renderDoctorResult(cmd, format, result)
			if result.Status == doctorStatusOK {
				return nil
			}
			return &doctorExitError{status: result.Status}
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, table, or json")
	cmd.Flags().BoolVar(&offline, "offline", false, "skip network connectivity checks")
	declareAgentStatePolicy(cmd, agentStateNone)
	return cmd
}

func runDoctor(cmd *cobra.Command, flags *persistentFlags, offline bool) doctorResult {
	ctx := cmd.Context()
	var checks []doctorCheck
	workDir, workErr := initWorkDir(flags)
	if workErr != nil {
		checks = append(checks, doctorCheck{
			Name:       "workdir",
			Status:     doctorStatusFail,
			Message:    workErr.Error(),
			Suggestion: "pass an existing directory with --cwd",
		})
		return doctorResult{Status: worstDoctorStatus(checks), Checks: checks}
	}
	checks = append(checks, doctorAgentCheck(workDir))
	cfg, err := loadConfigForCommand(cmd, flags)
	if err != nil {
		checks = append(checks, doctorCheck{
			Name:       "config",
			Status:     doctorStatusFail,
			Message:    err.Error(),
			Suggestion: "fix juex.yaml or " + initNoConfigSuggestion,
		})
		checks = append(checks, doctorWorkdirCheck(workDir))
		return doctorResult{Status: worstDoctorStatus(checks), Checks: checks, environment: cfg.EnvironmentSnapshot()}
	}
	cfg.WorkDir = workDir
	agentAvailable := false
	if resolution, resolveErr := agentstate.ResolveExisting(agentstate.Options{HomeDir: cfg.HomeJuexDir, WorkDir: workDir}); resolveErr == nil {
		cfg.AgentID = resolution.Agent.ID
		cfg.AgentName = resolution.Agent.Name
		cfg.AgentStateDir = resolution.Address.StateDir()
		cfg.AgentAddress = resolution.Address
		agentAvailable = true
	}
	if err := ensureSelectedRuntimeConfig(cfg); err != nil {
		checks = append(checks, doctorCheck{
			Name:       "config",
			Status:     doctorStatusFail,
			Message:    err.Error(),
			Suggestion: initNoConfigSuggestion,
		})
		checks = append(checks, doctorWorkdirCheck(workDir))
		return doctorResult{Status: worstDoctorStatus(checks), Checks: checks, environment: cfg.EnvironmentSnapshot()}
	}

	var agentRuntime app.AgentRuntimeResolution
	var agentRuntimeErr error
	if agentAvailable {
		agentRuntime, agentRuntimeErr = app.ResolveAgentRuntime(cfg)
	} else {
		agentRuntime, agentRuntimeErr = app.InspectAgentRuntime(cfg)
	}
	runtimeEnvironment := cfg.EnvironmentSnapshot()
	if agentAvailable && agentRuntimeErr == nil {
		runtimeEnvironment = agentRuntime.Environment()
	}
	checks = append(checks, doctorConfigCheck(cfg))
	checks = append(checks, doctorEnvironmentCheck(cfg, agentRuntime, agentRuntimeErr))
	checks = append(checks, doctorCredentialsCheck(cfg))
	checks = append(checks, doctorConnectivityCheck(ctx, cfg, offline))
	checks = append(checks, doctorShellCheck(cfg))
	checks = append(checks, doctorRipgrepCheck(func() (toolruntime.ResolvedRipgrep, error) {
		return toolruntime.ResolveRipgrepWithEnvironment(runtimeEnvironment)
	}))
	checks = append(checks, doctorWorkdirCheck(workDir))
	checks = append(checks, doctorMCPCheck(ctx, cfg, agentRuntime, agentRuntimeErr, offline || !agentAvailable))
	checks = append(checks, doctorSkillsCheck(cfg))
	redactionEnvironment := runtimeEnvironment
	if agentRuntimeErr == nil {
		redactionEnvironment = agentRuntime.Environment()
	}
	return doctorResult{Status: worstDoctorStatus(checks), Checks: checks, environment: redactionEnvironment}
}

func doctorAgentCheck(workDir string) doctorCheck {
	resolution, err := agentstate.ResolveExisting(agentstate.Options{WorkDir: workDir})
	if err == nil {
		return doctorCheck{
			Name:    "agent",
			Status:  doctorStatusOK,
			Message: fmt.Sprintf("workspace agent %s (%s)", resolution.Agent.Name, resolution.Agent.ID),
			Details: map[string]any{
				"id":        resolution.Agent.ID,
				"name":      resolution.Agent.Name,
				"state_dir": resolution.Address.StateDir(),
			},
		}
	}
	var noAgent *agentstate.NoAgentError
	if errors.As(err, &noAgent) {
		return doctorCheck{
			Name:       "agent",
			Status:     doctorStatusWarn,
			Message:    noAgent.Error(),
			Suggestion: "run juex run, repl, or listen to create a durable workspace agent",
		}
	}
	var rebind *agentstate.RebindRequiredError
	if errors.As(err, &rebind) {
		return doctorCheck{
			Name:       "agent",
			Status:     doctorStatusFail,
			Message:    rebind.Error(),
			Suggestion: "run juex run, repl, or listen once to automatically rebind the workspace agent",
		}
	}
	var copied *agentstate.WorkspaceCopyError
	if errors.As(err, &copied) {
		return doctorCheck{
			Name:       "agent",
			Status:     doctorStatusFail,
			Message:    copied.Error(),
			Suggestion: "remove the copied workspace marker to mint a new identity",
		}
	}
	return doctorCheck{
		Name:       "agent",
		Status:     doctorStatusFail,
		Message:    err.Error(),
		Suggestion: "repair the workspace marker or its matching JUEX_HOME registry entry",
	}
}

func doctorConfigCheck(cfg config.Config) doctorCheck {
	_, result := providerreadiness.ResolveProfile(cfg)
	check := doctorCheckFromReadiness("config", result)
	if result.Status != providerreadiness.StatusOK {
		check.Suggestion = "check top-level model and providers[] entries in juex.yaml"
	}
	if check.Details == nil {
		check.Details = map[string]any{}
	}
	check.Details["default_home_config_path"] = cfg.DefaultHomeRuntimeConfigPath()
	check.Details["effective_home_config_path"] = cfg.HomeRuntimeConfigPath()
	return check
}

func doctorCredentialsCheck(cfg config.Config) doctorCheck {
	return doctorCheckFromReadiness("credentials", providerreadiness.CheckCredentials(cfg.ProviderSelection()))
}

func doctorEnvironmentCheck(cfg config.Config, agentRuntime app.AgentRuntimeResolution, runtimeErr error) doctorCheck {
	status := cfg.EnvironmentStatus()
	metadata := cfg.EnvironmentSnapshot().ConfiguredMetadata()
	variables := make([]map[string]string, 0, len(metadata))
	for _, item := range metadata {
		detail := map[string]string{
			"key":    item.Key,
			"source": string(item.Source),
		}
		if item.Path != "" {
			detail["path"] = item.Path
		}
		variables = append(variables, detail)
	}
	extensionVariables := make([]map[string]string, 0, len(agentRuntime.EnvironmentDeclarations()))
	for _, item := range agentRuntime.EnvironmentDeclarations() {
		detail := map[string]string{
			"key":    item.Name,
			"source": item.Source,
			"status": string(item.Status),
		}
		if item.ManifestPath != "" {
			detail["path"] = item.ManifestPath
		}
		if item.ShadowedBySource != "" {
			detail["shadowed_by_source"] = item.ShadowedBySource
		}
		if item.ShadowedByPath != "" {
			detail["shadowed_by_path"] = item.ShadowedByPath
		}
		extensionVariables = append(extensionVariables, detail)
	}
	message := fmt.Sprintf("%d configured environment variable(s)", len(metadata))
	switch {
	case !status.DotenvEnabled:
		message += "; dotenv loading disabled"
	case status.DotenvLoaded:
		message += "; loaded " + status.DotenvPath
	default:
		message += "; no " + status.DotenvPath
	}
	message += fmt.Sprintf("; %d Extension default declaration(s)", len(extensionVariables))
	checkStatus := doctorStatusOK
	suggestion := ""
	if runtimeErr != nil {
		checkStatus = doctorStatusFail
		message += "; " + runtimeErr.Error()
		suggestion = "fix selected Extension environment declarations and retry"
	}
	return doctorCheck{
		Name:       "environment",
		Status:     checkStatus,
		Message:    message,
		Suggestion: suggestion,
		Details: map[string]any{
			"dotenv_path":                 status.DotenvPath,
			"dotenv_enabled":              status.DotenvEnabled,
			"dotenv_loaded":               status.DotenvLoaded,
			"configured_count":            status.ConfiguredVariables,
			"variables":                   variables,
			"extension_default_count":     len(extensionVariables),
			"extension_default_variables": extensionVariables,
		},
	}
}

func doctorConnectivityCheck(ctx context.Context, cfg config.Config, offline bool) doctorCheck {
	return doctorConnectivityCheckWithOptions(
		ctx,
		cfg,
		providerreadiness.ConnectivityOptions{Offline: offline},
	)
}

func doctorConnectivityCheckWithOptions(
	ctx context.Context,
	cfg config.Config,
	opts providerreadiness.ConnectivityOptions,
) doctorCheck {
	if opts.Offline {
		return doctorCheckFromReadiness(
			"connectivity",
			providerreadiness.CheckConnectivity(ctx, cfg, opts),
		)
	}
	// Official provider SDKs read standard proxy and transport settings from
	// the process environment. Scope the same snapshot activation used by
	// runtime-bearing commands to the probe, then restore it immediately.
	restore, err := cfg.EnvironmentSnapshot().Activate()
	if err != nil {
		return doctorCheck{
			Name:       "connectivity",
			Status:     doctorStatusFail,
			Message:    "activate runtime environment: " + err.Error(),
			Suggestion: "fix the configured runtime environment and retry",
		}
	}
	check := doctorCheckFromReadiness(
		"connectivity",
		providerreadiness.CheckConnectivity(ctx, cfg, opts),
	)
	if err := restore(); err != nil {
		check.Status = doctorStatusFail
		check.Message += "; restore runtime environment: " + err.Error()
		check.Suggestion = "check process environment permissions and retry"
	}
	return check
}

func doctorShellCheck(cfg config.Config) doctorCheck {
	if strings.TrimSpace(cfg.Shell.Binary) == "" {
		return doctorCheck{Name: "shell", Status: doctorStatusFail, Message: "shell binary is empty", Suggestion: "set shell.profile or shell.profile: custom in juex.yaml"}
	}
	return doctorCheck{
		Name:    "shell",
		Status:  doctorStatusOK,
		Message: fmt.Sprintf("%s shell at %s", cfg.Shell.Profile, cfg.Shell.Binary),
		Details: map[string]any{
			"profile": cfg.Shell.Profile,
			"family":  cfg.Shell.Family,
			"binary":  cfg.Shell.Binary,
		},
	}
}

func doctorRipgrepCheck(resolve func() (toolruntime.ResolvedRipgrep, error)) doctorCheck {
	resolved, err := resolve()
	if err != nil {
		return doctorCheck{
			Name:       "ripgrep",
			Status:     doctorStatusWarn,
			Message:    err.Error(),
			Suggestion: "install JueX from a release package, set JUEX_RG, or install rg for source development",
		}
	}
	version := resolved.Version
	if version == "" {
		version = "unmanaged"
	}
	return doctorCheck{
		Name:    "ripgrep",
		Status:  doctorStatusOK,
		Message: fmt.Sprintf("%s ripgrep %s at %s", resolved.Source, version, resolved.Path),
		Details: map[string]any{
			"path":    resolved.Path,
			"source":  string(resolved.Source),
			"version": resolved.Version,
		},
	}
}

func doctorWorkdirCheck(workDir string) doctorCheck {
	st, err := os.Stat(workDir)
	if err != nil || !st.IsDir() {
		return doctorCheck{Name: "workdir", Status: doctorStatusFail, Message: "workdir is not a directory: " + workDir, Suggestion: "pass an existing directory with --cwd"}
	}
	juexDir := filepath.Join(workDir, ".juex")
	if st, err := os.Stat(juexDir); err != nil {
		if os.IsNotExist(err) {
			return doctorCheck{Name: "workdir", Status: doctorStatusOK, Message: ".juex directory does not exist yet"}
		}
		return doctorCheck{Name: "workdir", Status: doctorStatusFail, Message: err.Error(), Suggestion: "check workdir permissions"}
	} else if !st.IsDir() {
		return doctorCheck{Name: "workdir", Status: doctorStatusFail, Message: ".juex exists but is not a directory", Suggestion: "move or remove the conflicting path"}
	}
	return doctorCheck{Name: "workdir", Status: doctorStatusOK, Message: "workdir and .juex are readable"}
}

func doctorMCPCheck(ctx context.Context, cfg config.Config, agentRuntime app.AgentRuntimeResolution, runtimeErr error, offline bool) doctorCheck {
	opts := mcp.RemoteReadinessOptions{Offline: offline}
	if runtimeErr != nil {
		return doctorCheck{Name: "mcp", Status: doctorStatusFail, Message: runtimeErr.Error(), Suggestion: "fix selected Extension environment declarations and retry"}
	}
	if offline {
		return doctorMCPCheckWithAgentRuntimeOptions(ctx, cfg, agentRuntime, opts)
	}
	restore, err := cfg.EnvironmentSnapshot().Activate()
	if err != nil {
		return doctorCheck{
			Name:       "mcp",
			Status:     doctorStatusFail,
			Message:    "activate runtime environment: " + err.Error(),
			Suggestion: "fix the configured runtime environment and retry",
		}
	}
	check := doctorMCPCheckWithAgentRuntimeOptions(ctx, cfg, agentRuntime, opts)
	if err := restore(); err != nil {
		check.Status = doctorStatusFail
		check.Message += "; restore runtime environment: " + err.Error()
		check.Suggestion = "check process environment permissions and retry"
	}
	return check
}

func doctorMCPCheckWithOptions(
	ctx context.Context,
	cfg config.Config,
	opts mcp.RemoteReadinessOptions,
) doctorCheck {
	agentRuntime, err := app.ResolveAgentRuntime(cfg)
	if err != nil {
		return doctorCheck{Name: "mcp", Status: doctorStatusFail, Message: err.Error(), Suggestion: "fix selected Extension environment declarations and retry"}
	}
	return doctorMCPCheckWithAgentRuntimeOptions(ctx, cfg, agentRuntime, opts)
}

func doctorMCPCheckWithAgentRuntimeOptions(
	ctx context.Context,
	cfg config.Config,
	agentRuntime app.AgentRuntimeResolution,
	opts mcp.RemoteReadinessOptions,
) doctorCheck {
	configs, err := app.LoadMCPConfigs(agentRuntime, cfg.WorkDir)
	if err != nil {
		if stage, ok := mcp.ErrorReadinessStage(err); ok {
			suggestion := "configure exactly one valid command or url for the named MCP server"
			if stage == mcp.ReadinessStageCredentials {
				suggestion = "configure the named MCP credential environment variable"
			}
			return doctorCheck{
				Name:       "mcp",
				Status:     doctorStatusFail,
				Message:    string(stage) + ": " + err.Error(),
				Suggestion: suggestion,
				Details:    map[string]any{"stage": stage},
			}
		}
		return doctorCheck{Name: "mcp", Status: doctorStatusFail, Message: err.Error(), Suggestion: "fix mcp.json, credential environment, or extension MCP conflicts"}
	}
	servers := mcp.MergeConfigs(configs).MCPServers
	if len(servers) == 0 {
		return doctorCheck{Name: "mcp", Status: doctorStatusOK, Message: "no MCP servers configured"}
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	opts.ConnectOptions.Environment = agentRuntime.Environment()
	var failures []string
	var suggestions []string
	var details []map[string]any
	hasRemote := false
	for _, name := range names {
		spec := servers[name]
		if spec.URL != "" {
			hasRemote = true
			result := mcp.CheckRemoteReadiness(ctx, name, spec, opts)
			details = append(details, map[string]any{
				"name":      name,
				"transport": "remote",
				"stage":     result.Stage,
				"status":    result.Status,
				"message":   result.Message,
			})
			if result.Status != mcp.ReadinessStatusOK {
				failures = append(failures, fmt.Sprintf("%s: %s: %s", name, result.Stage, result.Message))
				suggestions = appendUniqueString(suggestions, result.Suggestion)
			}
			continue
		}
		if err := commandExecutable(cfg, spec.Command); err != nil {
			failures = append(failures, name+": "+err.Error())
			suggestions = appendUniqueString(suggestions, "install missing MCP commands or update mcp.json")
			details = append(details, map[string]any{
				"name":      name,
				"transport": "stdio",
				"status":    mcp.ReadinessStatusFail,
				"message":   err.Error(),
			})
			continue
		}
		details = append(details, map[string]any{
			"name":      name,
			"transport": "stdio",
			"status":    mcp.ReadinessStatusOK,
			"message":   "command available",
		})
	}
	if len(failures) > 0 {
		return doctorCheck{
			Name:       "mcp",
			Status:     doctorStatusFail,
			Message:    strings.Join(failures, "; "),
			Suggestion: strings.Join(suggestions, "; "),
			Details:    map[string]any{"servers": details},
		}
	}
	message := fmt.Sprintf("%d MCP server(s) ready", len(servers))
	if opts.Offline && hasRemote {
		message = fmt.Sprintf("%d MCP server(s) configured; remote connectivity skipped", len(servers))
	}
	return doctorCheck{
		Name:    "mcp",
		Status:  doctorStatusOK,
		Message: message,
		Details: map[string]any{"servers": details},
	}
}

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func doctorSkillsCheck(cfg config.Config) doctorCheck {
	graph, err := app.ResolveRuntimeResourceGraph(cfg)
	if err != nil {
		return doctorCheck{Name: "skills", Status: doctorStatusFail, Message: err.Error(), Suggestion: "fix extension resource configuration"}
	}
	loader := skills.NewLoaderFromDirs(graph.SkillDirs())
	if err := loader.Load(); err != nil {
		return doctorCheck{Name: "skills", Status: doctorStatusFail, Message: err.Error(), Suggestion: "fix duplicate or unreadable skill directories"}
	}
	return doctorCheck{Name: "skills", Status: doctorStatusOK, Message: fmt.Sprintf("%d skill(s) loaded", len(loader.All()))}
}

func commandExecutable(cfg config.Config, command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("command is empty")
	}
	_, err := cfg.EnvironmentSnapshot().LookPath(command)
	return err
}

func worstDoctorStatus(checks []doctorCheck) doctorStatus {
	worst := doctorStatusOK
	for _, check := range checks {
		switch check.Status {
		case doctorStatusFail:
			return doctorStatusFail
		case doctorStatusWarn:
			worst = doctorStatusWarn
		}
	}
	return worst
}

func renderDoctorResult(cmd *cobra.Command, format string, result doctorResult) {
	if format == "json" {
		data, _, err := result.environment.RedactConfiguredJSON([]byte(mustJSON(result)))
		if err != nil {
			cmdPrintln(cmd, `{"status":"fail","checks":[]}`)
			return
		}
		var redacted doctorResult
		if err := json.Unmarshal(data, &redacted); err != nil {
			cmdPrintln(cmd, `{"status":"fail","checks":[]}`)
			return
		}
		restoreDoctorExtensionEnvironmentMetadata(&redacted, result)
		cmdPrintln(cmd, mustJSON(redacted))
		return
	}
	var output strings.Builder
	for _, check := range result.Checks {
		line := fmt.Sprintf("%-4s %-14s %s", strings.ToUpper(string(check.Status)), check.Name, check.Message)
		if check.Suggestion != "" {
			line += " (" + check.Suggestion + ")"
		}
		output.WriteString(line)
		output.WriteByte('\n')
	}
	output.WriteString("status: " + string(result.Status))
	data, _ := result.environment.RedactConfiguredValues([]byte(output.String()))
	cmdPrintln(cmd, string(data))
}

func restoreDoctorExtensionEnvironmentMetadata(redacted *doctorResult, public doctorResult) {
	const detailsKey = "extension_default_variables"
	for i := range redacted.Checks {
		if i >= len(public.Checks) || redacted.Checks[i].Name != "environment" || public.Checks[i].Name != "environment" {
			continue
		}
		publicRows, ok := public.Checks[i].Details[detailsKey].([]map[string]string)
		if !ok {
			return
		}
		redactedRows, ok := redacted.Checks[i].Details[detailsKey].([]any)
		if !ok {
			return
		}
		for rowIndex, publicRow := range publicRows {
			if rowIndex >= len(redactedRows) {
				break
			}
			redactedRow, ok := redactedRows[rowIndex].(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"key", "source", "status", "path", "shadowed_by_source", "shadowed_by_path"} {
				if value, exists := publicRow[key]; exists {
					redactedRow[key] = value
				}
			}
		}
		return
	}
}
