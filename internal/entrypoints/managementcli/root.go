package managementcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	"github.com/juex-ai/juex/internal/entrypoints/webassets"
	"github.com/juex-ai/juex/internal/foundation/maildelivery"
	"github.com/spf13/cobra"
)

func Execute(ctx context.Context, args []string, out, errOut io.Writer) error {
	var publicURL, listen, email, name string
	var insecure bool
	root := &cobra.Command{Use: "juex-management", Short: "Run and administer the JueX management service", SilenceUsage: true, SilenceErrors: true}
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentFlags().StringVar(&publicURL, "public-url", os.Getenv("JUEX_PUBLIC_URL"), "Public HTTPS origin")
	root.PersistentFlags().BoolVar(&insecure, "insecure-http", false, "Allow HTTP for a local development deployment")
	open := func(cmd *cobra.Command) (*managed.Management, error) {
		config := managed.ManagementConfig{DatabaseURL: os.Getenv("JUEX_DATABASE_URL"), MasterKey: os.Getenv("JUEX_MASTER_KEY"), PublicURL: strings.TrimSuffix(publicURL, "/"), InsecureHTTP: insecure}
		if address := os.Getenv("JUEX_SMTP_ADDRESS"); address != "" {
			config.SMTP = &maildelivery.Config{Address: address, From: os.Getenv("JUEX_SMTP_FROM"), Username: os.Getenv("JUEX_SMTP_USERNAME"), Password: os.Getenv("JUEX_SMTP_PASSWORD"), TLSMode: os.Getenv("JUEX_SMTP_TLS_MODE")}
		}
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
		handler, err := managementhttp.New(managementhttp.Options{Auth: app.Auth, Directory: app.Directory, Runtime: app.Runtime, PublicURL: publicURL, InsecureHTTP: insecure, MailEnabled: app.Mailer != nil, Static: webassets.Handler(), Health: app.Pool.Ping})
		if err != nil {
			return err
		}
		server := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			return err
		}
		done := make(chan error, 1)
		backgroundCtx, stopBackground := context.WithCancel(cmd.Context())
		backgroundDone := make(chan struct{})
		go func() { defer close(backgroundDone); app.RunBackground(backgroundCtx) }()
		defer func() { stopBackground(); <-backgroundDone }()
		go func() { done <- server.Serve(listener) }()
		fmt.Fprintln(out, "Management listening on", listener.Addr())
		select {
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-cmd.Context().Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return server.Shutdown(shutdown)
		}
	}}
	serve.Flags().StringVar(&listen, "listen", "0.0.0.0:8680", "Management HTTP listen address (use an HTTPS proxy for public access)")
	root.AddCommand(serve)
	return root.ExecuteContext(ctx)
}
