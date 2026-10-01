package platformrpc

import (
	"context"
	"errors"
	"sync"

	"github.com/cloudwego/kitex/server"
)

type lifecycleServer struct {
	server.Server
	exit chan error
	done chan struct{}
	once sync.Once
}

func newLifecycleServer(factory func(...server.Option) server.Server, options []server.Option) server.Server {
	stop := make(chan error, 1)
	options = append(options, server.WithExitSignal(func() <-chan error { return stop }))
	return &lifecycleServer{Server: factory(options...), exit: stop, done: make(chan struct{})}
}

func (s *lifecycleServer) Run() error {
	defer close(s.done)
	return s.Server.Run()
}

func (s *lifecycleServer) Stop() error {
	// Kitex Stop before Run initializes its transport consumes a sync.Once and
	// can leave the later listener running. Its exit signal is safe at startup.
	s.once.Do(func() { s.exit <- nil })
	<-s.done
	return nil
}

// Run owns the server lifecycle; callers cancel its context before closing
// the stores used by admitted requests.
func Run(ctx context.Context, service server.Server) error {
	done := make(chan error, 1)
	go func() { done <- service.Run() }()
	select {
	case err := <-done:
		if err == nil && ctx.Err() == nil {
			return errors.New("platform RPC server stopped unexpectedly")
		}
		return err
	case <-ctx.Done():
		err := service.Stop()
		return errors.Join(err, <-done)
	}
}
