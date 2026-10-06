// Package migrationcli adapts the private offline operator migration command.
package migrationcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/juex-ai/juex/internal/app/migration"
	"github.com/spf13/cobra"
)

func Execute(ctx context.Context, args []string, out, errOut io.Writer) error {
	var directory, bundle, digest string
	var descriptor int
	var target migration.BundleTarget
	root := &cobra.Command{Use: "juex-migrate", Short: "Import fixed legacy state into an offline Host deployment", SilenceUsage: true, SilenceErrors: true}
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	apply := &cobra.Command{Use: "apply", Short: "Apply a verified bundle while retaining source locks and target maintenance", Long: "The operator must first stop the old Fleet HTTP service, Agents, Memory and their children, disable source autostart, and stop target writers/executors. Pass the held deployment maintenance descriptor across exec. Locks do not prove process shutdown. This command keeps maintenance enabled and does not install or activate extension resources.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if descriptor < 3 {
			return errors.New("an inherited maintenance descriptor is required")
		}
		lock := os.NewFile(uintptr(descriptor), "migration-maintenance-lock")
		if lock == nil {
			return errors.New("an open inherited maintenance descriptor is required")
		}
		defer func() { _ = lock.Close() }()
		// Validate before opening anything that could reuse a closed FD number.
		if _, err := lock.Stat(); err != nil {
			return errors.New("an open inherited maintenance descriptor is required")
		}
		config, err := migration.HostTarget(directory, target)
		if err != nil {
			return err
		}
		frozen, err := migration.LoadBundle(bundle, digest, config.Target)
		if err != nil {
			return err
		}
		guard, err := frozen.GuardSource()
		if err != nil {
			return err
		}
		defer func() { _ = guard.Close() }()
		config.Source, config.Lock = guard, lock
		report, err := migration.Apply(cmd.Context(), frozen, config)
		if writeErr := json.NewEncoder(out).Encode(report); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return err
	}}
	apply.Flags().StringVar(&directory, "deployment", "", "Existing private Host deployment directory")
	apply.Flags().StringVar(&bundle, "bundle", "", "Private fixed migration bundle directory")
	apply.Flags().StringVar(&digest, "sha256", "", "Independently recorded manifest SHA-256")
	apply.Flags().StringVar(&target.ActorID, "actor", "", "Current target administrator UUID")
	apply.Flags().StringVar(&target.TenantID, "tenant", "", "Target Tenant UUID")
	apply.Flags().StringVar(&target.UserID, "user", "", "Target Fleet owner UUID")
	apply.Flags().StringVar(&target.FleetID, "fleet", "", "Expected retained target Fleet UUID")
	apply.Flags().IntVar(&descriptor, "lock-fd", -1, "Inherited exclusive maintenance descriptor (at least 3)")
	for _, name := range []string{"deployment", "bundle", "sha256", "actor", "tenant", "user", "fleet", "lock-fd"} {
		if err := apply.MarkFlagRequired(name); err != nil {
			return err
		}
	}
	root.AddCommand(apply)
	return root.ExecuteContext(ctx)
}
