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
		headless      bool
		unsafeBindAny bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the JSON/SSE API for the current WorkDir",
		Long: `Starts the current workspace agent and exposes its JSON/SSE API.
The canonical local agent endpoint is always published. Unless --headless is
set, the same API also listens on the loopback --addr.

This command does not serve the React SPA. Use juex fleet serve for the fleet
browser UI, agent switcher, and per-agent API proxy.

Hit Ctrl-C to shut down. In-flight turns receive context cancellation
and the server flushes session jsonl before exit.`,
		Example: `  juex serve
  juex serve --addr 127.0.0.1:9000
  juex serve --headless`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if headless && cmd.Flags().Changed("addr") {
				return &usageError{msg: "juex serve: --headless cannot be combined with --addr"}
			}
			if headless && unsafeBindAny {
				return &usageError{msg: "juex serve: --headless cannot be combined with --unsafe-bind-any"}
			}
			cfg, err := loadConfigForCommand(cmd, flags)
			if err != nil {
				return err
			}
			if err := ensureSelectedRuntimeConfig(cfg); err != nil {
				return err
			}
			if !headless && !unsafeBindAny && !isLoopbackAddr(addr) {
				return &usageError{msg: "juex serve: --addr must bind to loopback (got " + addr + "). Pass --unsafe-bind-any if you have your own network protection."}
			}
			if unsafeBindAny {
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: --unsafe-bind-any in use; juex has no authentication. Anyone who can reach this address can run shell commands.")
			}
			srv := web.NewServer(web.Options{
				Cfg:          cfg,
				Addr:         addr,
				AllowAnyBind: unsafeBindAny,
				Headless:     headless,
				Verbose:      flags.verbose,
				Debug:        flags.debug,
				LogLevel:     flags.logLevel,
				Stderr:       cmd.ErrOrStderr(),
				OnReady:      func(info web.ReadyInfo) { reportServeReady(cmd, info) },
			})

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			return srv.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "loopback address (host:port)")
	cmd.Flags().BoolVar(&headless, "headless", false, "serve only the canonical agent endpoint without a TCP API listener")
	cmd.Flags().BoolVar(&unsafeBindAny, "unsafe-bind-any", false, "allow --addr to bind beyond loopback (no auth — use only on trusted networks)")
	return cmd
}

func reportServeReady(cmd *cobra.Command, info web.ReadyInfo) {
	if info.FallbackReason != "" {
		fmt.Fprintf(
			cmd.ErrOrStderr(),
			"WARNING: agent unix endpoint unavailable (%s); using %s\n",
			info.FallbackReason,
			info.AgentEndpoint,
		)
	}
	cmdPrintln(cmd, "juex serve agent endpoint listening on "+info.AgentEndpoint)
	if info.TCPAddress != "" {
		cmdPrintln(cmd, "juex serve API listening on http://"+info.TCPAddress)
	}
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
