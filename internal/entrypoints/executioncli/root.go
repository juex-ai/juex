// Package executioncli hosts the platform Execution service.
package executioncli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/entrypoints/executionhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/spf13/cobra"
)

func Execute(ctx context.Context, args []string, out, errOut io.Writer) error {
	var address, httpAddress, management, credentials, hostedConfiguration, hostedAddress string
	root := &cobra.Command{Use: "juex-execution", Short: "Run the managed execution service", SilenceUsage: true, SilenceErrors: true}
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentFlags().StringVar(&management, "management", os.Getenv("JUEX_MANAGEMENT_RPC"), "Private Management RPC address")
	root.PersistentFlags().StringVar(&credentials, "credentials", os.Getenv("JUEX_SERVICE_CERTS"), "Directory containing the CA and Execution service identity")
	serve := &cobra.Command{Use: "serve", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		identity := platformrpc.CredentialsAt(credentials, "execution")
		app, err := managed.OpenExecution(cmd.Context(), managed.ExecutionConfig{DatabaseURL: os.Getenv("JUEX_DATABASE_URL"), ManagementAddress: management, Credentials: identity, HostedConfiguration: hostedConfiguration, HostedListen: hostedAddress})
		if err != nil {
			return err
		}
		defer app.Close()
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return err
		}
		defer func() { _ = listener.Close() }()
		public, err := net.Listen("tcp", httpAddress)
		if err != nil {
			return err
		}
		defer func() { _ = public.Close() }()
		service, err := serverrpc.NewExecution(listener, identity, app.Service, app.Pool.Ping)
		if err != nil {
			return err
		}
		run, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		handler := executionhttp.New(run, app.Service)
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}
		var guestServer *http.Server
		var guestDone chan error
		if app.Service.Hosted != nil {
			pair, err := tls.LoadX509KeyPair(identity.Certificate, identity.Key)
			if err != nil {
				return err
			}
			guestListener, err := net.Listen("tcp", hostedAddress)
			if err != nil {
				return err
			}
			defer func() { _ = guestListener.Close() }()
			guestServer = &http.Server{Handler: handler.HostedHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}
			guestDone = make(chan error, 1)
			secured := tls.NewListener(guestListener, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}})
			go func() { guestDone <- guestServer.Serve(secured) }()
			fmt.Fprintln(out, "Hosted device TLS listening on", guestListener.Addr())
		}
		done := make(chan struct{})
		go func() { defer close(done); app.Run(run) }()
		defer func() { cancel(); <-done }()
		rpcDone, httpDone := make(chan error, 1), make(chan error, 1)
		go func() { rpcDone <- serverrpc.Run(run, service) }()
		go func() { httpDone <- server.Serve(public) }()
		fmt.Fprintln(out, "Execution RPC listening on", listener.Addr(), "device HTTP on", public.Addr())
		var rpcErr, httpErr, guestErr error
		var rpcExited, httpExited, guestExited bool
		select {
		case rpcErr = <-rpcDone:
			rpcExited = true
		case httpErr = <-httpDone:
			httpExited = true
		case guestErr = <-guestDone:
			guestExited = true
		case <-run.Done():
		}
		cancel()
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		connectionErr := handler.Shutdown(shutdown)
		shutdownErr := server.Shutdown(shutdown)
		if shutdownErr != nil {
			_ = server.Close()
		}
		if guestServer != nil {
			if err := guestServer.Shutdown(shutdown); err != nil {
				shutdownErr = errors.Join(shutdownErr, err)
				_ = guestServer.Close()
			}
			if !guestExited {
				guestErr = <-guestDone
			}
			if errors.Is(guestErr, http.ErrServerClosed) {
				guestErr = nil
			}
		}
		if !rpcExited {
			rpcErr = <-rpcDone
		}
		if !httpExited {
			httpErr = <-httpDone
		}
		if errors.Is(httpErr, http.ErrServerClosed) {
			httpErr = nil
		}
		return errors.Join(rpcErr, httpErr, guestErr, connectionErr, shutdownErr)
	}}
	serve.Flags().StringVar(&address, "listen", "0.0.0.0:8783", "Private Execution RPC listen address")
	serve.Flags().StringVar(&httpAddress, "device-listen", "0.0.0.0:8683", "Device HTTP listen address behind the HTTPS reverse proxy")
	serve.Flags().StringVar(&hostedConfiguration, "hosted-config", os.Getenv("JUEX_HOSTED_CONFIG"), "Operator-owned hosted backend JSON configuration")
	serve.Flags().StringVar(&hostedAddress, "hosted-listen", "0.0.0.0:8684", "Dedicated hosted device TLS endpoint (connect only)")
	root.AddCommand(serve)
	return root.ExecuteContext(ctx)
}
