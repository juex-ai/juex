package serviceendpoint

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestControlCallsRequireIdentityOnEveryRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{FleetID: "fleet", ServiceID: "memory", InstanceID: "instance"}
	stopped := make(chan struct{})
	server := ControlServer(listener, identity, func() { close(stopped) })
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	defer func() { _ = server.Stop(); <-done }()
	record := Record{Identity: identity, Network: "tcp", Address: listener.Addr().String()}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := Probe(context.Background(), record); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, wrong := range []Identity{{FleetID: "other", ServiceID: "memory", InstanceID: "instance"}, {FleetID: "fleet", ServiceID: "other", InstanceID: "instance"}, {FleetID: "fleet", ServiceID: "memory", InstanceID: "old"}} {
		target := record
		target.Identity = wrong
		if err := Probe(context.Background(), target); err == nil {
			t.Fatal("identity mismatch accepted")
		}
		if err := Stop(context.Background(), target); err == nil {
			t.Fatal("wrong identity stopped server")
		}
	}
	select {
	case <-stopped:
		t.Fatal("invalid call stopped server")
	default:
	}
	if err := Stop(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("matching stop not delivered")
	}
}
