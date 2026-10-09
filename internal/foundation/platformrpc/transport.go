// Package platformrpc owns neutral Kitex transport and service identities.
package platformrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/gopkg/bufiox"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/cloudwego/kitex/pkg/remote"
	"github.com/cloudwego/kitex/pkg/remote/trans/gonet"
	transheader "github.com/cloudwego/kitex/pkg/remote/transmeta"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/transport"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
)

var ErrUnavailable = errors.New("platform service unavailable")

type Credentials struct{ CA, Certificate, Key string }

func CredentialsAt(directory, role string) Credentials {
	return Credentials{CA: filepath.Join(directory, "ca.pem"), Certificate: filepath.Join(directory, role+".pem"), Key: filepath.Join(directory, role+".key")}
}

func (c Credentials) load() (*tls.Config, error) {
	root, err := os.ReadFile(c.CA)
	if err != nil {
		return nil, errors.New("read platform CA certificate")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(root) {
		return nil, errors.New("invalid platform CA certificate")
	}
	pair, err := tls.LoadX509KeyPair(c.Certificate, c.Key)
	if err != nil {
		return nil, errors.New("invalid platform service certificate or private key")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, ClientCAs: pool, Certificates: []tls.Certificate{pair}}, nil
}

func ServerOptions(listener net.Listener, credentials Credentials, clients ...string) ([]server.Option, error) {
	config, err := credentials.load()
	if err != nil {
		return nil, err
	}
	config.ClientAuth = tls.RequireAndVerifyClientCert
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || len(state.PeerCertificates[0].DNSNames) != 1 {
			return errors.New("service identity required")
		}
		for _, name := range state.PeerCertificates[0].DNSNames {
			if slices.Contains(clients, name) {
				return nil
			}
		}
		return errors.New("service identity not authorized")
	}
	secured := &drainingListener{Listener: tls.NewListener(listener, config), connections: make(map[*drainingConnection]struct{})}
	return []server.Option{server.WithListener(secured), server.WithTransServerFactory(gonet.NewTransServerFactory()), server.WithTransHandlerFactory(gonet.NewSvrTransHandlerFactory()), server.WithMetaHandler(transmeta.ServerTTHeaderHandler), server.WithEnableContextTimeout(true), server.WithBoundHandler(&peerIdentity{listener: secured}), server.WithExitWaitTime(10 * time.Second), server.WithExitSignal(func() <-chan error { return make(chan error) })}, nil
}

type identityKey struct{}

// CallerRole comes from the verified TLS connection, never caller metadata.
func CallerRole(ctx context.Context) string {
	role, _ := ctx.Value(identityKey{}).(string)
	return role
}

type peerIdentity struct{ listener *drainingListener }

func (p *peerIdentity) OnActive(ctx context.Context, _ net.Conn) (context.Context, error) {
	return ctx, nil
}
func (p *peerIdentity) OnInactive(ctx context.Context, _ net.Conn) context.Context { return ctx }
func (p *peerIdentity) OnMessage(ctx context.Context, _, _ remote.Message) (context.Context, error) {
	return ctx, nil
}
func (p *peerIdentity) OnRead(ctx context.Context, connection net.Conn) (context.Context, error) {
	p.listener.mu.Lock()
	defer p.listener.mu.Unlock()
	for candidate := range p.listener.connections {
		if candidate.RemoteAddr().String() != connection.RemoteAddr().String() {
			continue
		}
		secure, ok := candidate.Conn.(*tls.Conn)
		if !ok {
			break
		}
		state := secure.ConnectionState()
		if !state.HandshakeComplete || len(state.VerifiedChains) == 0 || len(state.PeerCertificates[0].DNSNames) != 1 {
			break
		}
		return context.WithValue(ctx, identityKey{}, state.PeerCertificates[0].DNSNames[0]), nil
	}
	return ctx, errors.New("verified service identity unavailable")
}

// Kitex's gonet shutdown stops accepting but otherwise waits for its two-minute
// idle read deadline. Drain reads immediately while letting admitted handlers
// finish their response writes within the server's shutdown budget.
type drainingListener struct {
	net.Listener
	mu          sync.Mutex
	closed      bool
	connections map[*drainingConnection]struct{}
}

func (l *drainingListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = connection.Close()
		return nil, net.ErrClosed
	}
	tracked := &drainingConnection{Conn: connection, listener: l}
	l.connections[tracked] = struct{}{}
	return tracked, nil
}

func (l *drainingListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return net.ErrClosed
	}
	l.closed = true
	err := l.Listener.Close()
	for connection := range l.connections {
		_ = connection.Conn.SetReadDeadline(time.Now())
	}
	return err
}

type drainingConnection struct {
	net.Conn
	listener *drainingListener
}

func (c *drainingConnection) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	if err != nil {
		c.listener.mu.Lock()
		draining := c.listener.closed
		c.listener.mu.Unlock()
		if draining {
			err = io.EOF
		}
	}
	return n, err
}

func (c *drainingConnection) SetReadDeadline(deadline time.Time) error {
	c.listener.mu.Lock()
	defer c.listener.mu.Unlock()
	if c.listener.closed {
		deadline = time.Now()
	}
	return c.Conn.SetReadDeadline(deadline)
}

func (c *drainingConnection) Close() error {
	c.listener.mu.Lock()
	delete(c.listener.connections, c)
	c.listener.mu.Unlock()
	return c.Conn.Close()
}

type tlsDialer struct{ config *tls.Config }

func (d tlsDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: timeout}, Config: d.config}
	connection, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &bufferedConnection{Conn: connection, reader: bufiox.NewDefaultReader(connection), writer: bufiox.NewDefaultWriter(connection)}, nil
}

// The Kitex gonet handler consumes bufiox buffers even when a custom dialer
// supplies the underlying TLS connection. Its built-in dialer wraps plain TCP.
type bufferedConnection struct {
	net.Conn
	reader *bufiox.DefaultReader
	writer *bufiox.DefaultWriter
	closed atomic.Bool
}

func (c *bufferedConnection) Reader() *bufiox.DefaultReader { return c.reader }
func (c *bufferedConnection) Writer() *bufiox.DefaultWriter { return c.writer }
func (c *bufferedConnection) Read(data []byte) (int, error) { return c.reader.Read(data) }
func (c *bufferedConnection) Close() error {
	if c.closed.Swap(true) {
		return net.ErrClosed
	}
	_ = c.reader.Release(nil)
	return c.Conn.Close()
}

func ClientOptions(address, service string, credentials Credentials) ([]client.Option, error) {
	if address == "" || service == "" {
		return nil, errors.New("platform service address and identity are required")
	}
	if host, port, err := net.SplitHostPort(address); err != nil || host == "" || port == "" {
		return nil, errors.New("platform service requires a TCP host:port address")
	}
	config, err := credentials.load()
	if err != nil {
		return nil, err
	}
	config.ServerName = service
	// Kitex WithHostPorts eagerly resolves DNS and falls back to Unix sockets
	// on lookup failure. Containers may start before their peers enter DNS;
	// retain the TCP hostname and resolve it on each connection instead.
	resolver := &discovery.SynthesizedResolver{
		NameFunc:   func() string { return "platform-tcp:" + address },
		TargetFunc: func(context.Context, rpcinfo.EndpointInfo) string { return address },
		ResolveFunc: func(context.Context, string) (discovery.Result, error) {
			return discovery.Result{Cacheable: true, CacheKey: address, Instances: []discovery.Instance{discovery.NewInstance("tcp", address, discovery.DefaultWeight, nil)}}, nil
		},
	}
	return []client.Option{client.WithResolver(resolver), client.WithDialer(tlsDialer{config: config}), client.WithTransHandlerFactory(gonet.NewCliTransHandlerFactory()), client.WithTransportProtocol(transport.TTHeader), client.WithMetaHandler(deadlineMetadata{transmeta.ClientTTHeaderHandler}), client.WithConnectTimeout(3 * time.Second), client.WithRPCTimeout(10 * time.Second)}, nil
}

type deadlineMetadata struct{ remote.MetaHandler }

func (m deadlineMetadata) WriteMeta(ctx context.Context, message remote.Message) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return ctx, err
	}
	ctx, err := m.MetaHandler.WriteMeta(ctx, message)
	if err != nil {
		return ctx, err
	}
	// Kitex's standard header carries the configured budget, not the remaining
	// parent deadline. Each RPC hop must preserve the caller's shorter budget.
	budget := message.RPCInfo().Config().RPCTimeout()
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if budget <= 0 || remaining < budget {
			budget = remaining
		}
	}
	if budget < time.Millisecond {
		// A zero millisecond header disables server cancellation.
		return ctx, context.DeadlineExceeded
	}
	message.TransInfo().PutTransIntInfo(map[uint16]string{transheader.RPCTimeout: strconv.FormatInt(budget.Milliseconds(), 10)})
	return ctx, nil
}

func Reply(value any, code string) *platform.Reply {
	if code != "" {
		return &platform.Reply{Code: code, Json: "null"}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return &platform.Reply{Code: "unavailable", Json: "null"}
	}
	return &platform.Reply{Code: "ok", Json: string(encoded)}
}

func Decode(reply *platform.Reply, callErr error, value any, errorCode func(string) error) error {
	if callErr != nil || reply == nil {
		return ErrUnavailable
	}
	if reply.Code != "ok" {
		return errorCode(reply.Code)
	}
	if value == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(reply.Json), value); err != nil {
		return ErrUnavailable
	}
	return nil
}
