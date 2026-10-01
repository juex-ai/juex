package memorycli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/juex-ai/juex/internal/app/managed"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/spf13/cobra"
)

func Execute(ctx context.Context, args []string, out, errOut io.Writer) error {
	gate, err := maintenance.Open(os.Getenv("JUEX_MAINTENANCE_DIR"))
	if err != nil {
		return err
	}
	var listen, managementAddress, runtimeAddress, credentials string
	root := &cobra.Command{Use: "juex-memory", Short: "Run the independent Fleet Memory service", SilenceUsage: true, SilenceErrors: true}
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentFlags().StringVar(&managementAddress, "management", os.Getenv("JUEX_MANAGEMENT_RPC"), "Private Management RPC address")
	root.PersistentFlags().StringVar(&runtimeAddress, "runtime", os.Getenv("JUEX_RUNTIME_RPC"), "Private Runtime RPC address")
	root.PersistentFlags().StringVar(&credentials, "credentials", os.Getenv("JUEX_SERVICE_CERTS"), "Directory containing the CA and Memory service identity")
	serve := &cobra.Command{Use: "serve", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		identity := platformrpc.CredentialsAt(credentials, "memory")
		app, err := managed.OpenMemory(cmd.Context(), managed.MemoryConfig{Admission: gate.Enter, DatabaseURL: os.Getenv("JUEX_DATABASE_URL"), ManagementAddress: managementAddress, RuntimeAddress: runtimeAddress, Credentials: identity})
		if err != nil {
			return err
		}
		defer app.Close()
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			return err
		}
		defer func() { _ = listener.Close() }()
		service, err := serverrpc.NewMemory(listener, identity, app.Service, app.Pool.Ping)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "Memory listening on", listener.Addr())
		runCtx, cancel := context.WithCancel(cmd.Context())
		done := make(chan struct{})
		go func() { defer close(done); app.Service.Run(runCtx) }()
		defer func() { cancel(); <-done }()
		return serverrpc.Run(runCtx, service)
	}}
	serve.Flags().StringVar(&listen, "listen", "0.0.0.0:8784", "Private Memory RPC listen address")
	root.AddCommand(serve)
	return root.ExecuteContext(ctx)
}
