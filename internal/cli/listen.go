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
	cmd := &cobra.Command{
		Use:    "listen",
		Short:  "Run one Agent Runtime (internal Fleet command)",
		Hidden: true,
		Args:   usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			if strings.TrimSpace(flags.agentID) == "" {
				return &usageError{msg: "juex listen: --agent-id is required"}
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
			srv := web.NewServer(web.Options{
				Cfg:     cfg,
				Stderr:  cmd.ErrOrStderr(),
				OnReady: func(info web.ReadyInfo) { reportListenReady(cmd, info) },
			})

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			return srv.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&flags.agentID, "agent-id", "", "registered Agent ID")
	_ = cmd.Flags().MarkHidden("agent-id")
	return cmd
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
