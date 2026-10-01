package managementcli

import (
	"encoding/json"
	"io"
	"os"

	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/spf13/cobra"
)

func usageCommand(credentials *string, out io.Writer) *cobra.Command {
	var address string
	var q managedruntime.UsageQuery
	root := &cobra.Command{Use: "usage", Short: "Operator usage reports and prospective reporting policy"}
	root.PersistentFlags().StringVar(&address, "runtime", os.Getenv("JUEX_RUNTIME_RPC"), "Private Runtime RPC address")
	client := func() (*runtimerpc.Client, error) {
		return runtimerpc.NewClient(address, platformrpc.CredentialsAt(*credentials, "management"))
	}
	report := &cobra.Command{Use: "report", Short: "Report incurred usage across tenants (dates use each historical reporting timezone)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client()
		if err != nil {
			return err
		}
		v, err := c.OperatorUsage(cmd.Context(), q)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(v)
	}}
	report.Flags().StringVar(&q.TenantID, "tenant", "", "Tenant ID; empty includes all tenants")
	report.Flags().StringVar(&q.UserID, "user", "", "Owner user ID within the selected tenant")
	report.Flags().StringVar(&q.From, "from", "", "Inclusive local reporting date YYYY-MM-DD; omit both dates for the latest 30 days in the deployment reporting timezone")
	report.Flags().StringVar(&q.Until, "until", "", "Exclusive local reporting date YYYY-MM-DD")
	report.Flags().StringVar(&q.Group, "group", "day", "day or month")
	report.Flags().IntVar(&q.Offset, "offset", 0, "Grouped row offset")
	report.Flags().IntVar(&q.Limit, "limit", 100, "Grouped row page size (1–200); totals cover every matching row")
	var policy managedruntime.UsagePolicy
	configure := &cobra.Command{Use: "configure", Short: "Start a new timezone period without rewriting prior usage", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, err := client()
		if err != nil {
			return err
		}
		v, err := c.ConfigureUsage(cmd.Context(), policy)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(v)
	}}
	configure.Flags().StringVar(&policy.Timezone, "timezone", "Asia/Shanghai", "IANA timezone for subsequent attempts")
	configure.Flags().IntVar(&policy.DetailDays, "detail-days", 90, "Settled usage detail retention; daily and monthly aggregates remain")
	root.AddCommand(report, configure)
	return root
}
