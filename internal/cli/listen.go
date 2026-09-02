package cli

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/web"
)

func newListenCmd(flags *persistentFlags) *cobra.Command {
	var (
		addr          string
		unsafeBindAny bool
	)
	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Listen for the current WorkDir agent JSON/SSE API",
		Long: `Starts the current workspace agent and exposes its JSON/SSE API.
The canonical local agent endpoint is always published. Pass --addr explicitly
to also listen for the same API on TCP.

This command does not serve the React SPA. Use juex fleet serve for the fleet
browser UI, agent switcher, and per-agent API proxy.

Hit Ctrl-C to shut down. In-flight turns receive context cancellation
and the server flushes Thread Journal before exit.`,
		Example: `  juex listen
  juex listen --addr 127.0.0.1:9000`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			addrChanged := cmd.Flags().Changed("addr")
			if err := validateListenOptions(addr, addrChanged, unsafeBindAny); err != nil {
				return err
			}
			cfg, lifecycle, err := loadRuntimeConfigForCommand(cmd, flags)
			if err != nil {
				return err
			}
			if lifecycle != nil {
				defer func() {
					runErr = lifecycle.finish(cmd, runErr)
				}()
			}
			if err := ensureSelectedRuntimeConfig(cfg); err != nil {
				return err
			}
			if addr != "" && !unsafeBindAny && !isLoopbackAddr(addr) {
				return &usageError{msg: "juex listen: --addr must bind to loopback (got " + addr + "). Pass --unsafe-bind-any if you have your own network protection."}
			}
			if unsafeBindAny {
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: --unsafe-bind-any in use; juex has no authentication. Anyone who can reach this address can run shell commands.")
			}
			srv := web.NewServer(web.Options{
				Cfg:          cfg,
				Addr:         addr,
				AllowAnyBind: unsafeBindAny,
				Verbose:      flags.verbose,
				Debug:        flags.debug,
				LogLevel:     flags.logLevel,
				Stderr:       cmd.ErrOrStderr(),
				OnReady:      func(info web.ReadyInfo) { reportListenReady(cmd, info) },
			})

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			return srv.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "loopback address (host:port); enables the TCP API listener")
	cmd.Flags().BoolVar(&unsafeBindAny, "unsafe-bind-any", false, "allow --addr to bind beyond loopback (no auth — use only on trusted networks)")
	declareAgentStatePolicy(cmd, agentStateMint)
	return cmd
}

func validateListenOptions(addr string, addrChanged, unsafeBindAny bool) error {
	if unsafeBindAny && !addrChanged {
		return &usageError{msg: "juex listen: --unsafe-bind-any requires --addr"}
	}
	if addrChanged && strings.TrimSpace(addr) == "" {
		return &usageError{msg: "juex listen: --addr must not be empty"}
	}
	return nil
}

func reportListenReady(cmd *cobra.Command, info web.ReadyInfo) {
	if info.FallbackReason != "" {
		fmt.Fprintf(
			cmd.ErrOrStderr(),
			"WARNING: agent unix endpoint unavailable (%s); using %s\n",
			info.FallbackReason,
			info.AgentEndpoint,
		)
	}
	cmdPrintln(cmd, "juex listen agent endpoint listening on "+info.AgentEndpoint)
	if info.TCPAddress != "" {
		cmdPrintln(cmd, "juex listen agent JSON/SSE API (no web UI) listening on http://"+info.TCPAddress)
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
