package managementcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/entrypoints/webassets"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
)

type serveConfig struct {
	HTTPAddress, RPCAddress, RuntimeAddress, PublicURL string
	Credentials                                        platformrpc.Credentials
	InsecureHTTP                                       bool
}

func serveManagement(ctx context.Context, app *managed.Management, config serveConfig, out io.Writer) error {
	runtime, err := runtimerpc.NewClient(config.RuntimeAddress, config.Credentials)
	if err != nil {
		return err
	}
	handler, err := managementhttp.New(managementhttp.Options{Auth: app.Auth, Directory: app.Directory, Runtime: runtime, PublicURL: config.PublicURL, InsecureHTTP: config.InsecureHTTP, MailEnabled: app.Mailer != nil, Static: webassets.Handler(), Health: app.Pool.Ping})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", config.HTTPAddress)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	rpcListener, err := net.Listen("tcp", config.RPCAddress)
	if err != nil {
		return err
	}
	defer func() { _ = rpcListener.Close() }()
	rpcServer, err := serverrpc.NewManagement(rpcListener, config.Credentials, app.Authority)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	backgroundDone := make(chan struct{})
	go func() { defer close(backgroundDone); app.RunBackground(runCtx) }()
	defer func() { cancel(); <-backgroundDone }()
	httpDone, rpcDone := make(chan error, 1), make(chan error, 1)
	go func() { httpDone <- server.Serve(listener) }()
	go func() { rpcDone <- serverrpc.Run(runCtx, rpcServer) }()
	fmt.Fprintln(out, "Management listening on", listener.Addr())
	var httpErr, rpcErr error
	var httpExited, rpcExited bool
	select {
	case httpErr = <-httpDone:
		httpExited = true
	case rpcErr = <-rpcDone:
		rpcExited = true
	case <-ctx.Done():
	}
	// Stop HTTP admission before cancelling private RPC so in-flight requests
	// can settle using the same service graph during the shutdown window.
	shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	shutdownErr := server.Shutdown(shutdown)
	if shutdownErr != nil {
		_ = server.Close()
	}
	cancel()
	if !httpExited {
		httpErr = <-httpDone
	}
	if !rpcExited {
		rpcErr = <-rpcDone
	}
	if errors.Is(httpErr, http.ErrServerClosed) {
		httpErr = nil
	}
	return errors.Join(httpErr, rpcErr, shutdownErr)
}
