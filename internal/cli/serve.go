package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/web"
)

func newServeCmd(flags *persistentFlags) *cobra.Command {
	var (
		addr string
		cors bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run a local HTTP server for the current WorkDir",
		Long: `Starts a loopback-only HTTP server that lists, shows, and drives
sessions through a browser. No authentication; bind only to 127.0.0.1.

Hit Ctrl-C to shut down. In-flight turns receive context cancellation
and the server flushes session jsonl before exit.`,
		Example: `  juex serve
  juex serve --addr 127.0.0.1:9000`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}
			srv := web.NewServer(web.Options{
				Cfg:  cfg,
				Addr: addr,
				CORS: cors,
			})

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			cmdPrintln(cmd, "juex serve listening on http://"+addr)
			return srv.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "loopback address (host:port)")
	cmd.Flags().BoolVar(&cors, "cors", false, "allow CORS from http://localhost:*")
	return cmd
}
