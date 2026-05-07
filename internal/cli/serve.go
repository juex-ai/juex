package cli

import (
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/web"
)

func newServeCmd(flags *persistentFlags) *cobra.Command {
	var addr string
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
			if !isLoopbackAddr(addr) {
				return &usageError{msg: "juex serve: --addr must bind to loopback (got " + addr + ")"}
			}
			srv := web.NewServer(web.Options{
				Cfg:  cfg,
				Addr: addr,
			})

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			cmdPrintln(cmd, "juex serve listening on http://"+addr)
			return srv.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "loopback address (host:port)")
	return cmd
}

// isLoopbackAddr reports whether addr is a host:port that binds to a
// loopback interface. We accept the three syntactic forms that net/http
// resolves to lo0 without surprises.
func isLoopbackAddr(addr string) bool {
	for _, prefix := range []string{"127.0.0.1:", "[::1]:", "localhost:"} {
		if strings.HasPrefix(addr, prefix) {
			return true
		}
	}
	return false
}
