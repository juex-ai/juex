package platformrpc_test

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	memoryrpc "github.com/juex-ai/juex/internal/memory/rpc"
	"golang.org/x/net/dns/dnsmessage"
)

func TestClientRecoversWhenServiceDNSAppearsAfterConstruction(t *testing.T) {
	dns, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var available atomic.Bool
	dnsDone := make(chan struct{})
	go func() {
		defer close(dnsDone)
		buf := make([]byte, 4096)
		for {
			n, peer, err := dns.ReadFrom(buf)
			if err != nil {
				return
			}
			var request dnsmessage.Message
			if request.Unpack(buf[:n]) != nil {
				continue
			}
			response := dnsmessage.Message{Header: dnsmessage.Header{ID: request.ID, Response: true, RecursionDesired: true, RecursionAvailable: true}, Questions: request.Questions}
			if !available.Load() {
				response.RCode = dnsmessage.RCodeNameError
			} else {
				for _, q := range request.Questions {
					if q.Type == dnsmessage.TypeA {
						response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}})
					}
				}
			}
			data, err := response.Pack()
			if err == nil {
				_, _ = dns.WriteTo(data, peer)
			}
		}
	}()
	t.Cleanup(func() { _ = dns.Close(); <-dnsDone })
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", dns.LocalAddr().String())
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })
	directory := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(directory); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	service, err := serverrpc.NewMemory(listener, platformrpc.CredentialsAt(directory, "memory"), nil, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Run() }()
	t.Cleanup(func() {
		if err := service.Stop(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("RPC stop timed out")
		}
	})
	client, err := memoryrpc.NewClient("memory-startup.test:"+port, platformrpc.CredentialsAt(directory, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Health(context.Background()); err == nil {
		t.Fatal("unavailable DNS unexpectedly connected")
	}
	available.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := client.Health(context.Background()); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("same client remained pinned to unavailable DNS or a Unix socket")
}
