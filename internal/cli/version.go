package cli

import (
	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/version"
)

func newVersionCmd(flags *persistentFlags) *cobra.Command {
	var (
		verbose bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print build info; with --verbose also dump runtime context (workdir, config, provider)",
		Args:  cobra.NoArgs,
		Example: `  juex version                  # short: "juex 0.0.1"
  juex version --verbose        # multi-line: build + runtime context
  juex version --json           # machine-readable`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Either form of --verbose triggers runtime context. The local
			// `-v` short binds to `verbose`; `--verbose` may bind to the
			// root persistent flag (cobra resolves same-named flags to the
			// closest declared one, which is undefined for our case), so
			// honour both.
			showRuntime := verbose || flags.verbose || jsonOut
			info := version.Build()
			if showRuntime {
				// Soft-fail: if config can't load (missing override file, etc.) we
				// still surface as much runtime context as we can. cfg's
				// WorkDir / HomeAgentsDir come from Load() which only fails
				// on os.Getwd, so they are usually populated even when the
				// config override is missing.
				cfg, _ := loadConfig(flags)
				info.WorkDir = cfg.WorkDir
				info.ConfigFile = configFileForPlan(flags)
				if cfg.ProviderID != "" || cfg.ProviderType != "" || cfg.ProviderProtocol != "" {
					if profile, err := cfg.ProviderProfile(); err == nil {
						info.ProviderID = profile.ID
						info.ProviderType = profile.Type
						info.Protocol = string(profile.Protocol)
					} else {
						info.ProviderID = cfg.ProviderID
						info.ProviderType = cfg.ProviderType
						info.Protocol = cfg.ProviderProtocol
					}
				}
				info.Model = cfg.Model
				info.BaseURL = cfg.BaseURL
				info.ProviderAuth = cfg.ProviderAuth
			}
			switch {
			case jsonOut:
				cmdPrintln(cmd, info.JSON())
			case showRuntime:
				cmdPrintln(cmd, info.Verbose())
			default:
				cmdPrintln(cmd, version.String())
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "include commit, build time, go version, runtime context")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output as JSON (implies runtime context)")
	return cmd
}
