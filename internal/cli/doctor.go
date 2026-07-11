package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/providerreadiness"
	"github.com/juex-ai/juex/internal/skills"
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
	Status doctorStatus  `json:"status"`
	Checks []doctorCheck `json:"checks"`
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
			result := runDoctor(cmd.Context(), flags, offline)
			renderDoctorResult(cmd, format, result)
			if result.Status == doctorStatusOK {
				return nil
			}
			return &doctorExitError{status: result.Status}
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, table, or json")
	cmd.Flags().BoolVar(&offline, "offline", false, "skip provider connectivity checks")
	return cmd
}

func runDoctor(ctx context.Context, flags *persistentFlags, offline bool) doctorResult {
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
	cfg, err := loadConfig(flags)
	if err != nil {
		checks = append(checks, doctorCheck{
			Name:       "config",
			Status:     doctorStatusFail,
			Message:    err.Error(),
			Suggestion: "fix juex.yaml or " + initNoConfigSuggestion,
		})
		checks = append(checks, doctorWorkdirCheck(workDir))
		return doctorResult{Status: worstDoctorStatus(checks), Checks: checks}
	}
	cfg.WorkDir = workDir
	if err := ensureSelectedRuntimeConfig(cfg); err != nil {
		checks = append(checks, doctorCheck{
			Name:       "config",
			Status:     doctorStatusFail,
			Message:    err.Error(),
			Suggestion: initNoConfigSuggestion,
		})
		checks = append(checks, doctorWorkdirCheck(workDir))
		return doctorResult{Status: worstDoctorStatus(checks), Checks: checks}
	}

	checks = append(checks, doctorConfigCheck(cfg))
	checks = append(checks, doctorCredentialsCheck(cfg))
	checks = append(checks, doctorConnectivityCheck(ctx, cfg, offline))
	checks = append(checks, doctorShellCheck(cfg))
	checks = append(checks, doctorWorkdirCheck(workDir))
	checks = append(checks, doctorMCPCheck(cfg))
	checks = append(checks, doctorSkillsCheck(cfg))
	return doctorResult{Status: worstDoctorStatus(checks), Checks: checks}
}

func doctorConfigCheck(cfg config.Config) doctorCheck {
	_, result := providerreadiness.ResolveProfile(cfg)
	if result.Status != providerreadiness.StatusOK {
		check := doctorCheckFromReadiness("config", result)
		check.Suggestion = "check top-level model and providers[] entries in juex.yaml"
		return check
	}
	return doctorCheckFromReadiness("config", result)
}

func doctorCredentialsCheck(cfg config.Config) doctorCheck {
	return doctorCheckFromReadiness("credentials", providerreadiness.CheckCredentials(cfg.ProviderSelection()))
}

func doctorConnectivityCheck(ctx context.Context, cfg config.Config, offline bool) doctorCheck {
	return doctorCheckFromReadiness("connectivity", providerreadiness.CheckConnectivity(ctx, cfg, providerreadiness.ConnectivityOptions{Offline: offline}))
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

func doctorMCPCheck(cfg config.Config) doctorCheck {
	configs, err := app.LoadMCPConfigs(cfg, cfg.WorkDir)
	if err != nil {
		return doctorCheck{Name: "mcp", Status: doctorStatusFail, Message: err.Error(), Suggestion: "fix mcp.json syntax or extension MCP conflicts"}
	}
	var servers []mcp.ServerSpec
	for _, c := range configs {
		for _, spec := range c.MCPServers {
			servers = append(servers, spec)
		}
	}
	if len(servers) == 0 {
		return doctorCheck{Name: "mcp", Status: doctorStatusOK, Message: "no MCP servers configured"}
	}
	var failures []string
	for _, spec := range servers {
		if err := commandExecutable(spec.Command); err != nil {
			failures = append(failures, spec.Command+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return doctorCheck{Name: "mcp", Status: doctorStatusFail, Message: strings.Join(failures, "; "), Suggestion: "install missing MCP commands or update mcp.json"}
	}
	return doctorCheck{Name: "mcp", Status: doctorStatusOK, Message: fmt.Sprintf("%d MCP server command(s) executable", len(servers))}
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

func commandExecutable(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("command is empty")
	}
	_, err := exec.LookPath(command)
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
		cmdPrintln(cmd, mustJSON(result))
		return
	}
	for _, check := range result.Checks {
		line := fmt.Sprintf("%-4s %-14s %s", strings.ToUpper(string(check.Status)), check.Name, check.Message)
		if check.Suggestion != "" {
			line += " (" + check.Suggestion + ")"
		}
		cmdPrintln(cmd, line)
	}
	cmdPrintln(cmd, "status: "+string(result.Status))
}
