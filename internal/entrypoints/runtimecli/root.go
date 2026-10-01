package runtimecli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/juex-ai/juex/internal/app/managed"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/spf13/cobra"
)

func Execute(ctx context.Context, args []string, out, errOut io.Writer) error {
	var listen, managementAddress, executionAddress, memoryAddress, calendarAddress, credentials string
	var threads int
	var idle time.Duration
	root := &cobra.Command{Use: "juex-runtime", Short: "Run the managed Agent Runtime service", SilenceUsage: true, SilenceErrors: true}
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentFlags().StringVar(&managementAddress, "management", os.Getenv("JUEX_MANAGEMENT_RPC"), "Private Management RPC address")
	root.PersistentFlags().StringVar(&executionAddress, "execution", os.Getenv("JUEX_EXECUTION_RPC"), "Private Execution RPC address")
	root.PersistentFlags().StringVar(&memoryAddress, "memory", os.Getenv("JUEX_MEMORY_RPC"), "Private Memory RPC address")
	root.PersistentFlags().StringVar(&calendarAddress, "calendar", os.Getenv("JUEX_CALENDAR_RPC"), "Private Calendar RPC address")
	root.PersistentFlags().StringVar(&credentials, "credentials", os.Getenv("JUEX_SERVICE_CERTS"), "Directory containing the CA and Runtime service identity")
	serve := &cobra.Command{Use: "serve", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		identity := platformrpc.CredentialsAt(credentials, "runtime")
		app, err := managed.OpenRuntime(cmd.Context(), managed.RuntimeConfig{DatabaseURL: os.Getenv("JUEX_DATABASE_URL"), ManagementAddress: managementAddress, ExecutionAddress: executionAddress, MemoryAddress: memoryAddress, CalendarAddress: calendarAddress, Credentials: identity, Runner: managedruntime.RunnerConfig{Concurrency: threads, IdleTimeout: idle}})
		if err != nil {
			return err
		}
		defer app.Close()
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			return err
		}
		defer func() { _ = listener.Close() }()
		service, err := serverrpc.NewRuntime(listener, identity, app.Service, app.Pool.Ping)
		if err != nil {
			return err
		}
		runCtx, cancel := context.WithCancel(cmd.Context())
		done := make(chan struct{})
		go func() { defer close(done); app.Runner.Run(runCtx) }()
		defer func() { cancel(); <-done }()
		fmt.Fprintln(out, "Runtime listening on", listener.Addr())
		return serverrpc.Run(runCtx, service)
	}}
	serve.Flags().StringVar(&listen, "listen", "0.0.0.0:8782", "Private Runtime RPC listen address")
	serve.Flags().IntVar(&threads, "max-active-threads", 10, "Maximum concurrently executing Main and Worker Threads")
	serve.Flags().DurationVar(&idle, "idle-timeout", 5*time.Minute, "Idle Activation release delay")
	root.AddCommand(serve)
	return root.ExecuteContext(ctx)
}
