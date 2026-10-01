package managementcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/spf13/cobra"
)

func Execute(ctx context.Context, args []string, out, errOut io.Writer) error {
	gate, err := maintenance.Open(os.Getenv("JUEX_MAINTENANCE_DIR"))
	if err != nil {
		return err
	}
	var publicURL, listen, email, name string
	var credentials, rpcListen, runtimeAddress, executionAddress, memoryAddress, calendarAddress string
	var insecure bool
	var auditDays int
	root := &cobra.Command{Use: "juex-management", Short: "Run and administer the JueX management service", SilenceUsage: true, SilenceErrors: true}
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "serve" || cmd.Annotations["maintenance"] == "true" {
			return nil
		}
		var err error
		release, err = gate.Enter()
		return err
	}
	root.PersistentFlags().IntVar(&auditDays, "audit-days", 90, "Retain operation audit facts for this many days (1–3650)")
	root.PersistentFlags().StringVar(&publicURL, "public-url", os.Getenv("JUEX_PUBLIC_URL"), "Public HTTPS origin")
	root.PersistentFlags().BoolVar(&insecure, "insecure-http", false, "Allow HTTP for a local development deployment")
	root.PersistentFlags().StringVar(&credentials, "credentials", os.Getenv("JUEX_SERVICE_CERTS"), "Directory containing the CA and Management service identity")
	open := func(cmd *cobra.Command) (*managed.Management, error) {
		config := managed.ManagementConfig{Maintenance: gate, DatabaseURL: os.Getenv("JUEX_DATABASE_URL"), MasterKey: os.Getenv("JUEX_MASTER_KEY"), PublicURL: strings.TrimSuffix(publicURL, "/"), InsecureHTTP: insecure, AuditDays: auditDays}
		config.SMTP = os.Getenv("JUEX_SMTP_CONFIG")
		return managed.OpenManagement(cmd.Context(), config)
	}
	root.AddCommand(&cobra.Command{Use: "migrate", Short: "Apply platform schema migrations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		app, err := open(cmd)
		if err != nil {
			return err
		}
		defer app.Close()
		return json.NewEncoder(out).Encode(map[string]string{"status": "ready"})
	}})
	bootstrap := &cobra.Command{Use: "bootstrap", Short: "Issue the one-use first-administrator setup link", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if publicURL == "" {
			return errors.New("--public-url is required")
		}
		app, err := open(cmd)
		if err != nil {
			return err
		}
		defer app.Close()
		link, err := app.Auth.BeginBootstrap(cmd.Context(), name, email)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"setup_url": link})
	}}
	bootstrap.Flags().StringVar(&name, "tenant", "Default", "Default tenant name")
	bootstrap.Flags().StringVar(&email, "email", "", "First administrator email")
	if err := bootstrap.MarkFlagRequired("email"); err != nil {
		return err
	}
	root.AddCommand(bootstrap)
	tenant := &cobra.Command{Use: "tenant", Short: "Operator tenant provisioning"}
	var tenantName, adminEmail string
	createTenant := &cobra.Command{Use: "create", Short: "Create another tenant and its first administrator", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		app, err := open(cmd)
		if err != nil {
			return err
		}
		defer app.Close()
		result, err := app.Auth.ProvisionTenant(cmd.Context(), tenantName, adminEmail)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(result)
	}}
	createTenant.Flags().StringVar(&tenantName, "name", "", "Tenant name")
	createTenant.Flags().StringVar(&adminEmail, "admin-email", "", "First administrator email")
	for _, flag := range []string{"name", "admin-email"} {
		if err := createTenant.MarkFlagRequired(flag); err != nil {
			return err
		}
	}
	tenant.AddCommand(createTenant)
	root.AddCommand(tenant)
	root.AddCommand(modelCommand(open, out))
	root.AddCommand(smtpCommand(out))
	root.AddCommand(maintenanceCommand(out), recoveryCommand(out))
	root.AddCommand(usageCommand(&credentials, out))
	services := &cobra.Command{Use: "services", Short: "Operator private service identities"}
	var directory string
	initialize := &cobra.Command{Use: "init", Short: "Create the platform CA and private service certificates in a new directory", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := platformrpc.CreateCredentials(directory); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"directory": directory})
	}}
	initialize.Flags().StringVar(&directory, "directory", "", "New private directory for service identities and the operator CA key")
	if err := initialize.MarkFlagRequired("directory"); err != nil {
		return err
	}
	services.AddCommand(initialize)
	services.AddCommand(healthCommand(&credentials, out))
	root.AddCommand(services)
	recovery := &cobra.Command{Use: "recover", Short: "Issue a one-use recovery link after operator identity verification", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if publicURL == "" {
			return errors.New("--public-url is required")
		}
		app, err := open(cmd)
		if err != nil {
			return err
		}
		defer app.Close()
		link, err := app.Auth.OperatorRecovery(cmd.Context(), email)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"recovery_url": link})
	}}
	recovery.Flags().StringVar(&email, "email", "", "Verified account identity to recover")
	if err := recovery.MarkFlagRequired("email"); err != nil {
		return err
	}
	root.AddCommand(recovery)
	serve := &cobra.Command{Use: "serve", Short: "Serve the authenticated Management Web and API", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		app, err := open(cmd)
		if err != nil {
			return err
		}
		defer app.Close()
		return serveManagement(cmd.Context(), app, serveConfig{HTTPAddress: listen, RPCAddress: rpcListen, RuntimeAddress: runtimeAddress, ExecutionAddress: executionAddress, MemoryAddress: memoryAddress, CalendarAddress: calendarAddress, Credentials: platformrpc.CredentialsAt(credentials, "management"), PublicURL: publicURL, InsecureHTTP: insecure}, out)
	}}
	serve.Flags().StringVar(&listen, "listen", "0.0.0.0:8680", "Management HTTP listen address (use an HTTPS proxy for public access)")
	serve.Flags().StringVar(&rpcListen, "rpc-listen", "0.0.0.0:8781", "Private Management RPC listen address")
	serve.Flags().StringVar(&runtimeAddress, "runtime", os.Getenv("JUEX_RUNTIME_RPC"), "Private Runtime RPC address")
	serve.Flags().StringVar(&memoryAddress, "memory", os.Getenv("JUEX_MEMORY_RPC"), "Private Memory RPC address")
	serve.Flags().StringVar(&calendarAddress, "calendar", os.Getenv("JUEX_CALENDAR_RPC"), "Private Calendar RPC address")
	serve.Flags().StringVar(&executionAddress, "execution", os.Getenv("JUEX_EXECUTION_RPC"), "Private Execution RPC address")
	root.AddCommand(serve)
	return root.ExecuteContext(ctx)
}
