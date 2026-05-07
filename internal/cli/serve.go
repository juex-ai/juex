package cli

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/web"
)

func newServeCmd(flags *persistentFlags) *cobra.Command {
	var (
		addr          string
		unsafeBindAny bool
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
			if !unsafeBindAny && !isLoopbackAddr(addr) {
				return &usageError{msg: "juex serve: --addr must bind to loopback (got " + addr + "). Pass --unsafe-bind-any if you have your own network protection."}
			}
			if unsafeBindAny {
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: --unsafe-bind-any in use; juex has no authentication. Anyone who can reach this address can run shell commands.")
			}
			srv := web.NewServer(web.Options{
				Cfg:          cfg,
				Addr:         addr,
				AllowAnyBind: unsafeBindAny,
			})

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			cmdPrintln(cmd, "juex serve listening on http://"+addr)
			return srv.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "loopback address (host:port)")
	cmd.Flags().BoolVar(&unsafeBindAny, "unsafe-bind-any", false, "allow --addr to bind beyond loopback (no auth — use only on trusted networks)")
	return cmd
}

// isLoopbackAddr returns true if addr's host portion is a loopback
// destination ("localhost" or any IP in 127.0.0.0/8 or ::1). Accepts
// either "host:port" or "host" form. Returns false on parse failures —
// the caller turns that into a usage error.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Maybe the user passed just a host. Try treating addr as host.
		host = addr
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
